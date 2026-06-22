import Foundation

/// Supplies bearer tokens to the API client. AuthManager conforms to this.
protocol TokenProviding: Sendable {
    func token() async -> String?
    func refreshedToken() async -> String?
}

enum APIError: Error, LocalizedError {
    case notAuthenticated
    case invalidRequest(String)
    case http(Int, String)
    case decoding(String)

    var errorDescription: String? {
        switch self {
        case .notAuthenticated: return "You're signed out. Please sign in again."
        case let .invalidRequest(msg): return msg
        case let .http(code, msg): return "Request failed (\(code)): \(msg)"
        case let .decoding(msg): return "Couldn't read the server response: \(msg)"
        }
    }
}

/// Talks to the debate-bot engine. Attaches the rxlab bearer token and, on a
/// 401, refreshes once and retries — the pattern RxCode's SecretsService uses.
final class APIClient: Sendable {
    let baseURL: URL
    private let tokens: TokenProviding
    private let session: URLSession

    private enum HLSPlaylistState {
        case ready
        case unauthorized
        case notReady
    }

    init(baseURL: URL = AppConfig.apiBaseURL, tokens: TokenProviding, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.tokens = tokens
        self.session = session
    }

    // MARK: - Planning

    func plan(_ req: PlanRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan", body: req)
    }

    func improve(_ req: PlanImproveRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan/improve", body: req)
    }

    // MARK: - Server-owned discussions

    func discussions(limit: Int = 20, offset: Int = 0) async throws -> [Discussion] {
        try await get("/api/discussions", query: [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ])
    }

    func discussion(id: String) async throws -> Discussion {
        try await get("/api/discussions/\(id)")
    }

    func planDiscussion(_ req: PlanRequest) async throws -> Discussion {
        try await send("POST", "/api/discussions/plan", body: req)
    }

    func improveDiscussion(id: String, instruction: String,
                           attachments: [Attachment] = []) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/improve",
                       body: DiscussionImproveRequest(instruction: instruction,
                                                      attachments: attachments.isEmpty ? nil : attachments))
    }

    /// Re-research: adds the given links to the plan's sources and updates the
    /// plan to incorporate them. Backs the sources sheet's "add a link" action.
    func addDiscussionSources(id: String, urls: [String]) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/sources", body: AddSourcesRequest(urls: urls))
    }

    /// Uploads a reference file via a presigned object-storage URL, then asks
    /// the engine to return an attachment payload. Documents come back as
    /// markdown; images come back as direct image URLs for the model.
    func uploadFile(data: Data, filename: String, mimeType: String) async throws -> UploadResponse {
        let presign: UploadPresignResponse = try await send(
            "POST",
            "/api/uploads/presign",
            body: UploadPresignRequest(filename: filename, mimeType: mimeType)
        )

        var uploadReq = URLRequest(url: presign.uploadURL)
        uploadReq.httpMethod = presign.method
        uploadReq.httpBody = data
        uploadReq.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        for (name, value) in presign.headers {
            uploadReq.setValue(value, forHTTPHeaderField: name)
        }
        let (uploadData, uploadResp) = try await session.data(for: uploadReq)
        guard let uploadHTTP = uploadResp as? HTTPURLResponse,
              (200..<300).contains(uploadHTTP.statusCode) else {
            let status = (uploadResp as? HTTPURLResponse)?.statusCode ?? 0
            throw APIError.http(status, String(decoding: uploadData, as: UTF8.self))
        }

        return try await send(
            "POST",
            "/api/uploads/complete",
            body: UploadCompleteRequest(key: presign.key, filename: filename, mimeType: mimeType)
        )
    }

    func generateDiscussion(id: String, language: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/generate",
                       body: DiscussionGenerateRequest(language: language))
    }

    func deleteDiscussion(id: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/discussions/\(id)"))
    }

    func appendDiscussionLine(id: String, line: DiscussionLineRequest) async throws {
        let payload = try JSONEncoder().encode(line)
        _ = try await perform(request(method: "POST", path: "/api/discussions/\(id)/lines", body: payload))
    }

    // MARK: - Jobs

    func submitJob(_ req: JobSubmitRequest) async throws -> JobSubmitResponse {
        try await send("POST", "/api/jobs/json", body: req)
    }

    func jobStatus(id: String) async throws -> JobStatusDTO {
        try await get("/api/jobs/\(id)")
    }

    func sendJobMessage(id: String, text: String, username: String, discussionID: String) async throws {
        let req = JobMessageRequest(text: text, username: username, discussionID: discussionID)
        try await sendNoContent("POST", "/api/jobs/\(id)/messages", body: req)
    }

    func forceStopJob(id: String) async throws {
        try await sendNoContent("POST", "/api/jobs/\(id)/stop", body: EmptyRequest())
    }

    /// Persisted transcript snapshot for a running or finished job.
    func jobTranscript(id: String) async throws -> [TranscriptDTO] {
        try await get("/api/jobs/\(id)/transcript")
    }

    /// Current captions (WebVTT text) for a running or finished job.
    func liveSubtitles(id: String) async throws -> String {
        let (data, _) = try await perform(request(method: "GET", path: "/api/jobs/\(id)/subtitles/live"))
        return String(decoding: data, as: UTF8.self)
    }

    // MARK: - Streaming URLs (consumed by AVPlayer; bearer set via asset headers)

    func hlsURL(jobID: String) -> URL {
        baseURL.appendingPathComponent("api/jobs/\(jobID)/hls/stream.m3u8")
    }

    func finalAudioURL(jobID: String) -> URL {
        baseURL.appendingPathComponent("api/jobs/\(jobID)/audio")
    }

    func downloadPodcastAudio(sourceURL: URL?,
                              jobID: String?,
                              title: String,
                              progress: @escaping (Double) -> Void) async throws -> URL {
        guard let primaryURL = sourceURL ?? jobID.map({ finalAudioURL(jobID: $0) }) else {
            throw APIError.invalidRequest("Podcast download is not ready yet.")
        }

        do {
            return try await downloadPodcastAudio(from: primaryURL,
                                                  title: title,
                                                  progress: progress)
        } catch let error as APIError {
            if case .http(404, _) = error, sourceURL != nil, let jobID {
                return try await downloadPodcastAudio(from: finalAudioURL(jobID: jobID),
                                                      title: title,
                                                      progress: progress)
            }
            throw error
        }
    }

    func hlsPlaylistReady(jobID: String) async -> Bool {
        guard let token = await tokens.token() else { return false }
        switch await hlsPlaylistState(jobID: jobID, token: token) {
        case .ready:
            return true
        case .unauthorized:
            guard let fresh = await tokens.refreshedToken() else {
                return false
            }
            return await hlsPlaylistState(jobID: jobID, token: fresh) == .ready
        case .notReady:
            return false
        }
    }

    private func hlsPlaylistState(jobID: String, token: String) async -> HLSPlaylistState {
        var req = URLRequest(url: hlsURL(jobID: jobID))
        req.httpMethod = "GET"
        req.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        do {
            let (data, resp) = try await session.data(for: req)
            guard let http = resp as? HTTPURLResponse else {
                return .notReady
            }
            if http.statusCode == 401 {
                return .unauthorized
            }
            guard (200..<300).contains(http.statusCode) else {
                return .notReady
            }
            let playlist = String(decoding: data, as: UTF8.self)
            guard playlist.contains("#EXTM3U"),
                  playlist.contains("#EXTINF"),
                  let segment = Self.firstHLSMediaSegment(in: playlist) else {
                return .notReady
            }
            return await hlsSegmentAvailable(jobID: jobID, segment: segment, token: token) ? .ready : .notReady
        } catch {
            return .notReady
        }
    }

    nonisolated static func firstHLSMediaSegment(in playlist: String) -> String? {
        var sawMediaInfo = false
        for rawLine in playlist.split(whereSeparator: \.isNewline) {
            let line = rawLine.trimmingCharacters(in: .whitespacesAndNewlines)
            if line.isEmpty { continue }
            if line.hasPrefix("#EXTINF") {
                sawMediaInfo = true
                continue
            }
            if line.hasPrefix("#") { continue }
            if sawMediaInfo { return line }
        }
        return nil
    }

    private func hlsSegmentAvailable(jobID: String, segment: String, token: String) async -> Bool {
        guard let url = URL(string: segment, relativeTo: hlsURL(jobID: jobID))?.absoluteURL else {
            return false
        }
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        req.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        req.setValue("bytes=0-0", forHTTPHeaderField: "Range")
        do {
            let (_, resp) = try await session.data(for: req)
            guard let http = resp as? HTTPURLResponse else { return false }
            return http.statusCode == 200 || http.statusCode == 206
        } catch {
            return false
        }
    }

    func webSocketURL(jobID: String) -> URL {
        var comps = URLComponents(url: baseURL.appendingPathComponent("api/jobs/\(jobID)/ws"),
                                  resolvingAgainstBaseURL: false)!
        comps.scheme = (baseURL.scheme == "https") ? "wss" : "ws"
        return comps.url!
    }

    /// The current bearer token, for callers that build their own requests
    /// (AVPlayer asset headers, the WebSocket task).
    func currentToken() async -> String? { await tokens.token() }

    // MARK: - Core

    private func downloadPodcastAudio(from url: URL,
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

    private func shouldAuthenticateDownload(_ url: URL) -> Bool {
        url.scheme == baseURL.scheme && url.host == baseURL.host && url.port == baseURL.port
    }

    private func podcastDownloadDestination(title: String, sourceURL: URL) throws -> URL {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("PodcastDownloads", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: " -_"))
        let sanitized = title.unicodeScalars.map { allowed.contains($0) ? Character($0) : "-" }
        let baseName = String(sanitized)
            .trimmingCharacters(in: CharacterSet(charactersIn: " -_"))
        let name = baseName.isEmpty ? "Podcast" : String(baseName.prefix(80))
        let ext = sourceURL.pathExtension.isEmpty ? "mp3" : sourceURL.pathExtension
        return directory
            .appendingPathComponent(name)
            .appendingPathExtension(ext)
    }

    private func performDownload(_ request: URLRequest,
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

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> T {
        let (data, _) = try await perform(request(method: "GET", path: path, query: query))
        return try decode(data)
    }

    private func send<B: Encodable, T: Decodable>(_ method: String, _ path: String, body: B) async throws -> T {
        let payload = try JSONEncoder().encode(body)
        let (data, _) = try await perform(request(method: method, path: path, body: payload))
        return try decode(data)
    }

    private func sendNoContent<B: Encodable>(_ method: String, _ path: String, body: B) async throws {
        let payload = try JSONEncoder().encode(body)
        _ = try await perform(request(method: method, path: path, body: payload))
    }

    private func request(method: String, path: String, body: Data? = nil, query: [URLQueryItem] = []) -> URLRequest {
        var url = baseURL.appendingPathComponent(String(path.dropFirst()))
        if !query.isEmpty,
           var comps = URLComponents(url: url, resolvingAgainstBaseURL: false) {
            comps.queryItems = query
            url = comps.url ?? url
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return req
    }

    /// Performs a request with bearer auth and one refresh-and-retry on 401.
    private func perform(_ base: URLRequest) async throws -> (Data, HTTPURLResponse) {
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
            throw APIError.http(http.statusCode, String(decoding: data, as: UTF8.self))
        }
        return (data, http)
    }

    private func decode<T: Decodable>(_ data: Data) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: data) }
        catch { throw APIError.decoding(error.localizedDescription) }
    }
}

private final class PodcastDownloadDelegate: NSObject, URLSessionDownloadDelegate {
    let destinationURL: URL
    let progress: (Double) -> Void
    var continuation: CheckedContinuation<URL, Error>?
    private var completionResult: Result<URL, Error>?

    init(destinationURL: URL, progress: @escaping (Double) -> Void) {
        self.destinationURL = destinationURL
        self.progress = progress
    }

    func urlSession(_ session: URLSession,
                    downloadTask: URLSessionDownloadTask,
                    didWriteData bytesWritten: Int64,
                    totalBytesWritten: Int64,
                    totalBytesExpectedToWrite: Int64) {
        guard totalBytesExpectedToWrite > 0 else { return }
        progress(min(1, max(0, Double(totalBytesWritten) / Double(totalBytesExpectedToWrite))))
    }

    func urlSession(_ session: URLSession,
                    downloadTask: URLSessionDownloadTask,
                    didFinishDownloadingTo location: URL) {
        do {
            try FileManager.default.createDirectory(
                at: destinationURL.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            if FileManager.default.fileExists(atPath: destinationURL.path) {
                try FileManager.default.removeItem(at: destinationURL)
            }
            try FileManager.default.copyItem(at: location, to: destinationURL)
            completionResult = .success(destinationURL)
        } catch {
            completionResult = .failure(error)
        }
    }

    func urlSession(_ session: URLSession,
                    task: URLSessionTask,
                    didCompleteWithError error: Error?) {
        if let error {
            continuation?.resume(throwing: error)
            continuation = nil
            return
        }
        if let http = task.response as? HTTPURLResponse,
           !(200..<300).contains(http.statusCode) {
            continuation?.resume(throwing: APIError.http(http.statusCode, HTTPURLResponse.localizedString(forStatusCode: http.statusCode)))
            continuation = nil
            return
        }
        switch completionResult {
        case let .success(url):
            progress(1)
            continuation?.resume(returning: url)
        case let .failure(error):
            continuation?.resume(throwing: error)
        case .none:
            continuation?.resume(throwing: APIError.invalidRequest("Download did not produce a file."))
        }
        continuation = nil
    }
}
