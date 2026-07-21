import Foundation
import JSONSchemaForm
import OSLog

extension APIClient {
    // MARK: - Core

    func downloadPodcastAudio(from url: URL,
                                      title: String,
                                      progress: @escaping (Double) -> Void) async throws -> URL {
        let destinationURL = try podcastDownloadDestination(title: title, sourceURL: url)
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        req.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData

        if shouldAuthenticateDownload(url) {
            guard let token = await tokens.token() else { throw APIError.notAuthenticated }
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
            do {
                return try await performDownload(req, to: destinationURL, progress: progress)
            } catch APIError.http(401, _) {
                guard let fresh = await tokens.refreshedToken() else { throw APIError.notAuthenticated }
                var retry = URLRequest(url: url)
                retry.httpMethod = "GET"
                retry.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
                retry.setValue("Bearer \(fresh)", forHTTPHeaderField: "Authorization")
                return try await performDownload(retry, to: destinationURL, progress: progress)
            }
        }

        return try await performDownload(req, to: destinationURL, progress: progress)
    }

    func shouldAuthenticateDownload(_ url: URL) -> Bool {
        url.scheme == baseURL.scheme && url.host == baseURL.host && url.port == baseURL.port
    }

    func podcastDownloadDestination(title: String, sourceURL: URL) throws -> URL {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("PodcastDownloads", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: " -_"))
        let sanitized = title.unicodeScalars.map { allowed.contains($0) ? Character($0) : "-" }
        let baseName = String(sanitized)
            .trimmingCharacters(in: CharacterSet(charactersIn: " -_"))
        let name = baseName.isEmpty ? AppStringLiteral.stationNameRaw : String(baseName.prefix(80))
        let ext = sourceURL.pathExtension.isEmpty ? "mp3" : sourceURL.pathExtension
        return directory
            .appendingPathComponent(name)
            .appendingPathExtension(ext)
    }

    func performDownload(_ request: URLRequest,
                                 to destinationURL: URL,
                                 progress: @escaping (Double) -> Void) async throws -> URL {
        let delegate = PodcastDownloadDelegate(destinationURL: destinationURL, progress: progress)
        let queue = OperationQueue()
        queue.maxConcurrentOperationCount = 1
        let downloadSession = URLSession(configuration: .default, delegate: delegate, delegateQueue: queue)
        defer { downloadSession.finishTasksAndInvalidate() }

        return try await withCheckedThrowingContinuation { continuation in
            delegate.continuation = continuation
            downloadSession.downloadTask(with: request).resume()
        }
    }

    /// Runs an SSE plan request and re-emits its `event:`/`data:` frames as
    /// `PlanStreamEvent`s. Handles one 401 refresh-and-retry before the stream
    /// body starts; cancelling the consuming task cancels the request.
    func streamPlan<B: Encodable>(path: String, body: B) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let payload = try JSONEncoder().encode(body)
                    guard let token = await tokens.token() else { throw APIError.notAuthenticated }
                    var (bytes, http) = try await openSSE(path: path, body: payload, token: token)
                    if http.statusCode == 401 {
                        guard let fresh = await tokens.refreshedToken() else { throw APIError.notAuthenticated }
                        (bytes, http) = try await openSSE(path: path, body: payload, token: fresh)
                    }
                    guard (200..<300).contains(http.statusCode) else {
                        var message = ""
                        for try await line in bytes.lines { message += line }
                        throw mapHTTPError(http.statusCode, Data(message.utf8))
                    }

                    var event = "message"
                    var data = ""
                    try await Self.consumeSSELines(bytes) { line in
                        if line.isEmpty {
                            Self.dispatchSSE(event: event, data: data, to: continuation)
                            event = "message"
                            data = ""
                        } else if line.hasPrefix(":") {
                            return
                        } else if line.hasPrefix("event:") {
                            event = String(line.dropFirst(6)).trimmingCharacters(in: .whitespaces)
                        } else if line.hasPrefix("data:") {
                            let chunk = String(line.dropFirst(5))
                            let piece = chunk.hasPrefix(" ") ? String(chunk.dropFirst()) : chunk
                            data += data.isEmpty ? piece : "\n" + piece
                        }
                    }
                    Self.dispatchSSE(event: event, data: data, to: continuation)
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    func openSSE(path: String, body: Data, token: String) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
        try await openSSE(method: "POST", path: path, body: body, token: token)
    }

    func openSSE(method: String, path: String, body: Data?, token: String) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
        var req = request(method: method, path: path, body: body)
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        // Planning can sit silently inside a long LLM call between progress
        // events; the default 60s idle timeout would kill the stream mid-plan.
        // Give it a generous idle budget (server heartbeats also keep it warm).
        req.timeoutInterval = 600
        let (bytes, resp) = try await session.bytes(for: req)
        guard let http = resp as? HTTPURLResponse else {
            throw APIError.invalidRequest(String(localized: "Invalid streaming response.",
                                                  comment: "Shown when the streaming plan response is not a valid HTTP response"))
        }
        return (bytes, http)
    }

    static func consumeSSELines(_ bytes: URLSession.AsyncBytes,
                                        _ handle: (String) -> Void) async throws {
        var lineBytes: [UInt8] = []
        lineBytes.reserveCapacity(256)
        for try await byte in bytes {
            if byte == 10 {
                if lineBytes.last == 13 {
                    lineBytes.removeLast()
                }
                handle(String(decoding: lineBytes, as: UTF8.self))
                lineBytes.removeAll(keepingCapacity: true)
            } else {
                lineBytes.append(byte)
            }
        }
        if !lineBytes.isEmpty {
            if lineBytes.last == 13 {
                lineBytes.removeLast()
            }
            handle(String(decoding: lineBytes, as: UTF8.self))
        }
    }

    static func dispatchSSE(event: String, data: String,
                                    to continuation: AsyncThrowingStream<PlanStreamEvent, Error>.Continuation) {
        guard !data.isEmpty else { return }
        switch event {
        case "progress":
            if let ev = decodeSSE(PlanProgressEvent.self, data) { continuation.yield(.progress(ev)) }
        case "done":
            if let discussion = decodeSSE(Discussion.self, data) { continuation.yield(.done(discussion)) }
        case "error":
            continuation.yield(.failed(sseErrorMessage(data)))
        default:
            break
        }
    }

    static func decodeSSE<T: Decodable>(_ type: T.Type, _ data: String) -> T? {
        guard let raw = data.data(using: .utf8) else { return nil }
        do {
            return try JSONDecoder().decode(T.self, from: raw)
        } catch {
            apiLog.error("SSE JSON decode error type=\(String(describing: type), privacy: .public) bytes=\(data.utf8.count, privacy: .public) error=\(String(describing: error), privacy: .public)")
            return nil
        }
    }

    static func sseErrorMessage(_ data: String) -> String {
        if let raw = data.data(using: .utf8),
           let obj = try? JSONDecoder().decode([String: String].self, from: raw),
           let message = obj["message"], !message.isEmpty {
            return message
        }
        return data.isEmpty
            ? String(localized: "The plan update failed. Please try again.",
                     comment: "Fallback error when an SSE error event carries no message")
            : data
    }

    func get<T: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> T {
        let (data, _) = try await perform(request(method: "GET", path: path, query: query))
        return try decode(data)
    }

    func send<B: Encodable, T: Decodable>(_ method: String, _ path: String, body: B,
                                           timeout: TimeInterval? = nil) async throws -> T {
        let payload = try JSONEncoder().encode(body)
        var req = request(method: method, path: path, body: payload)
        if let timeout {
            req.timeoutInterval = timeout
        }
        let (data, _) = try await perform(req)
        return try decode(data)
    }

    func sendNoContent<B: Encodable>(_ method: String, _ path: String, body: B) async throws {
        let payload = try JSONEncoder().encode(body)
        _ = try await perform(request(method: method, path: path, body: payload))
    }

    func request(method: String, path: String, body: Data? = nil, query: [URLQueryItem] = []) -> URLRequest {
        var url = baseURL.appendingPathComponent(String(path.dropFirst()))
        if !query.isEmpty,
           var comps = URLComponents(url: url, resolvingAgainstBaseURL: false) {
            comps.queryItems = query
            url = comps.url ?? url
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.setValue(AcceptLanguage.headerValue, forHTTPHeaderField: "Accept-Language")
        req.setValue("ios", forHTTPHeaderField: "X-Client-Platform")
        req.setValue(Self.clientVersion, forHTTPHeaderField: "X-Client-Version")
        req.setValue(Self.clientBuild, forHTTPHeaderField: "X-Client-Build")
        if AppConfig.isE2E {
            req.setValue(AppConfig.e2eUserID, forHTTPHeaderField: "X-E2E-User-ID")
        }
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return req
    }

    static let clientVersion: String = {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
    }()

    static let clientBuild: String = {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""
    }()

    func pathComponent(_ value: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#[]@!$&'()*+,;=")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }

    /// Performs a request with bearer auth and one refresh-and-retry on 401.
    func perform(_ base: URLRequest) async throws -> (Data, HTTPURLResponse) {
        guard let token = await tokens.token() else { throw APIError.notAuthenticated }
        var req = base
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        var (data, resp) = try await session.data(for: req)
        var http = resp as! HTTPURLResponse
        if http.statusCode == 401 {
            guard let fresh = await tokens.refreshedToken() else { throw APIError.notAuthenticated }
            var retry = base
            retry.setValue("Bearer \(fresh)", forHTTPHeaderField: "Authorization")
            (data, resp) = try await session.data(for: retry)
            http = resp as! HTTPURLResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            throw mapHTTPError(http.statusCode, data)
        }
        return (data, http)
    }

    func decode<T: Decodable>(_ data: Data) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: data) }
        catch { throw APIError.decoding(error.localizedDescription) }
    }
}
