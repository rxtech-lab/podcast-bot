import Foundation
import JSONSchemaForm
import OSLog

extension APIClient {
    // MARK: - Points

    /// The signed-in user's current points balance.
    func pointsBalance() async throws -> Int {
        let resp: PointsBalanceResponse = try await get("/api/points/balance")
        return resp.balance
    }

    /// The user's points balance plus recent ledger entries (newest first) for
    /// the points-usage history view.
    func pointsHistory(limit: Int = 50, offset: Int = 0) async throws -> PointsHistoryResponse {
        try await get("/api/points/history", query: [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ])
    }

    // MARK: - Jobs

    func submitJob(_ req: JobSubmitRequest) async throws -> JobSubmitResponse {
        try await send("POST", "/api/jobs/json", body: req)
    }

    func jobStatus(id: String) async throws -> JobStatusDTO {
        try await get("/api/jobs/\(id)")
    }

    func sendJobMessage(id: String, text: String, username: String, discussionID: String, shareToken: String? = nil,
                        audioURL: String? = nil, audioKey: String? = nil) async throws {
        let req = JobMessageRequest(text: text, username: username, discussionID: discussionID, shareToken: shareToken,
                                    audioURL: audioURL, audioKey: audioKey)
        try await sendNoContent("POST", "/api/jobs/\(id)/messages", body: req)
    }

    func forceStopJob(id: String) async throws {
        try await sendNoContent("POST", "/api/jobs/\(id)/stop", body: EmptyRequest())
    }

    /// Persisted transcript snapshot for a running or finished job.
    func jobTranscript(id: String) async throws -> [TranscriptDTO] {
        try await get("/api/jobs/\(id)/transcript")
    }

    /// Canonical audiobook illustration timeline for a running or finished
    /// job — the only source of timed-artwork switching; the client never
    /// reconstructs it from transcript lines. `durationMS` (when known) lets
    /// the server synthesize an even split for legacy audiobooks that
    /// recorded no per-image offsets.
    func jobIllustrations(id: String, durationMS: Int? = nil) async throws -> [IllustrationCueDTO] {
        var query: [URLQueryItem] = []
        if let durationMS, durationMS > 0 {
            query.append(URLQueryItem(name: "duration_ms", value: String(durationMS)))
        }
        let response: IllustrationsResponseDTO = try await get("/api/jobs/\(id)/illustrations", query: query)
        return response.illustrations
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
            throw APIError.invalidRequest(String(localized: "\(AppStringLiteral.stationNameRaw) download is not ready yet.",
                                                  comment: "Shown when the user tries to download a podcast before it is available"))
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

    func hlsPlaylistState(jobID: String, token: String) async -> HLSPlaylistState {
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

    nonisolated static func isCancellation(_ error: Error) -> Bool {
        if error is CancellationError {
            return true
        }
        if let urlError = error as? URLError {
            return urlError.code == .cancelled
        }
        let nsError = error as NSError
        return nsError.domain == NSURLErrorDomain && nsError.code == NSURLErrorCancelled
    }

    func hlsSegmentAvailable(jobID: String, segment: String, token: String) async -> Bool {
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

}
