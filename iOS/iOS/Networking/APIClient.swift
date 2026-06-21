import Foundation

/// Supplies bearer tokens to the API client. AuthManager conforms to this.
protocol TokenProviding: Sendable {
    func token() async -> String?
    func refreshedToken() async -> String?
}

enum APIError: Error, LocalizedError {
    case notAuthenticated
    case http(Int, String)
    case decoding(String)

    var errorDescription: String? {
        switch self {
        case .notAuthenticated: return "You're signed out. Please sign in again."
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

    init(baseURL: URL = AppConfig.apiBaseURL, tokens: TokenProviding) {
        self.baseURL = baseURL
        self.tokens = tokens
        self.session = .shared
    }

    // MARK: - Planning

    func plan(_ req: PlanRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan", body: req)
    }

    func improve(_ req: PlanImproveRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan/improve", body: req)
    }

    // MARK: - Jobs

    func submitJob(_ req: JobSubmitRequest) async throws -> JobSubmitResponse {
        try await send("POST", "/api/jobs/json", body: req)
    }

    func jobStatus(id: String) async throws -> JobStatusDTO {
        try await get("/api/jobs/\(id)")
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
            return playlist.contains("#EXTM3U") && playlist.contains("#EXTINF") ? .ready : .notReady
        } catch {
            return .notReady
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

    private func get<T: Decodable>(_ path: String) async throws -> T {
        let (data, _) = try await perform(request(method: "GET", path: path))
        return try decode(data)
    }

    private func send<B: Encodable, T: Decodable>(_ method: String, _ path: String, body: B) async throws -> T {
        let payload = try JSONEncoder().encode(body)
        let (data, _) = try await perform(request(method: method, path: path, body: payload))
        return try decode(data)
    }

    private func request(method: String, path: String, body: Data? = nil) -> URLRequest {
        var req = URLRequest(url: baseURL.appendingPathComponent(String(path.dropFirst())))
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
