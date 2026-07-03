import Foundation
import JSONSchemaForm
import OSLog

private let apiLog = Logger(subsystem: "com.debatebot.ios", category: "APIClient")

/// Supplies bearer tokens to the API client. AuthManager conforms to this.
protocol TokenProviding: Sendable {
    func token() async -> String?
    func refreshedToken() async -> String?
}

enum APIError: Error, LocalizedError {
    case notAuthenticated
    case invalidRequest(String)
    case http(Int, String)
    /// HTTP 402: the user doesn't hold enough points to start this action. Carries
    /// the points required and the user's current balance so the UI can open the
    /// paywall with context.
    case insufficientPoints(required: Int, balance: Int)
    /// HTTP 409 on join: the discussion already has the maximum number of
    /// participants, so this user can't join.
    case participantCapReached
    case decoding(String)

    var errorDescription: String? {
        switch self {
        case .notAuthenticated:
            return String(localized: "You're signed out. Please sign in again.",
                          comment: "Error shown when the user's session has expired")
        case let .invalidRequest(msg): return msg
        case let .http(code, msg):
            return String(localized: "Request failed (\(code)): \(msg)",
                          comment: "Generic HTTP error; code is the status code, msg is the server message")
        case let .insufficientPoints(required, balance):
            return String(localized: "You need \(required) points but have \(balance). Top up to continue.",
                          comment: "Error shown when the user lacks enough points to start an action")
        case .participantCapReached:
            return String(localized: "This discussion is full. Ask the host to remove someone or try again later.",
                          comment: "Error shown when a discussion has reached its participant limit")
        case let .decoding(msg):
            return String(localized: "Couldn't read the server response: \(msg)",
                          comment: "Error shown when the server response could not be decoded")
        }
    }
}

/// Maps a non-2xx response into a typed APIError, decoding the points-shortfall
/// body into `.insufficientPoints` so callers can present the paywall.
func mapHTTPError(_ status: Int, _ data: Data) -> APIError {
    if status == 402, let shortfall = try? JSONDecoder().decode(InsufficientPointsResponse.self, from: data) {
        return .insufficientPoints(required: shortfall.requiredPoints, balance: shortfall.balance)
    }
    return .http(status, String(decoding: data, as: UTF8.self))
}

/// Talks to the debate-bot engine. Attaches the rxlab bearer token and, on a
/// 401, refreshes once and retries — the pattern RxCode's SecretsService uses.
final class APIClient: Sendable {
    let baseURL: URL
    private let tokens: TokenProviding
    private let session: URLSession
    private static let summaryExportTimeout: TimeInterval = 600

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

    func discussions(limit: Int = 20,
                     offset: Int = 0,
                     query: String? = nil,
                     visibility: DiscussionVisibility? = nil) async throws -> [Discussion] {
        var queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        if let visibility {
            queryItems.append(URLQueryItem(name: "visibility", value: visibility.rawValue))
        }
        return try await get("/api/discussions", query: queryItems)
    }

    func discussion(id: String, editLimit: Int? = nil, editBefore: Int64? = nil) async throws -> Discussion {
        var query: [URLQueryItem] = []
        if let editLimit {
            query.append(URLQueryItem(name: "edit_limit", value: String(editLimit)))
        }
        if let editBefore {
            query.append(URLQueryItem(name: "edit_before", value: String(editBefore)))
        }
        return try await get("/api/discussions/\(id)", query: query)
    }

    /// Opens a web-player/deep-link discussion ID. Private owner podcasts are
    /// only visible through the authenticated detail endpoint; public podcasts
    /// remain available through the market endpoint.
    func playerDiscussion(id: String) async throws -> Discussion {
        do {
            return try await discussion(id: id)
        } catch APIError.http(404, _) {
            let discussion = try await marketStation(id: id)
            try? await joinDiscussion(id: id, token: nil)
            return discussion
        }
    }

    func parentPodcasts(limit: Int = 50, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        var queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        return try await get("/api/discussions/parent-podcasts", query: queryItems)
    }

    func parentPodcast(id: String) async throws -> PodcastReference {
        try await get("/api/discussions/\(id)/parent-podcast")
    }

    /// Fetches the generated summary document (Markdown body) for a podcast. The
    /// detail payload only carries a content-free `summary` descriptor; this is
    /// the separate endpoint the summary view calls on mount. Throws (404) when no
    /// summary exists yet.
    func summary(id: String, docType: String = "summary") async throws -> SummaryDocument {
        var query: [URLQueryItem] = []
        if docType != "summary" {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        return try await get("/api/discussions/\(id)/summary", query: query)
    }

    /// Starts or retries summary generation for an owned, finished podcast and
    /// returns the refreshed discussion so the toolbar can show the pending state.
    func generateSummary(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/summary/generate", body: EmptyRequest())
    }

    /// Starts or retries the backend-owned audiobook companion video render.
    func generateVideo(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/video/generate", body: EmptyRequest())
    }

    func discussionUIActions(id: String,
                             surface: String,
                             docType: String? = nil,
                             supportsPoints: Bool = false,
                             supportsFollowUp: Bool = false,
                             supportsCreateFromPlan: Bool = false,
                             supportsSignOut: Bool = false) async throws -> DiscussionUIActionsResponse {
        var query = [URLQueryItem(name: "surface", value: surface)]
        if let docType, !docType.isEmpty {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        if supportsPoints {
            query.append(URLQueryItem(name: "supports_points", value: "true"))
        }
        if supportsFollowUp {
            query.append(URLQueryItem(name: "supports_follow_up", value: "true"))
        }
        if supportsCreateFromPlan {
            query.append(URLQueryItem(name: "supports_create_from_plan", value: "true"))
        }
        if supportsSignOut {
            query.append(URLQueryItem(name: "supports_sign_out", value: "true"))
        }
        return try await get("/api/discussions/\(id)/ui-actions", query: query)
    }

    /// Downloads the summary rendered as a PDF (produced server-side via
    /// Cloudflare Browser Rendering, with ```mermaid blocks drawn as real
    /// diagrams) and writes it to a temporary file, returning the local URL ready
    /// to share/export. Throws 404 when no summary exists, 503 when PDF export
    /// isn't configured on the server.
    func downloadSummaryPDF(id: String, docType: String = "summary", title: String) async throws -> URL {
        var query: [URLQueryItem] = []
        if docType != "summary" {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        var req = request(method: "GET",
                          path: "/api/discussions/\(id)/summary/pdf",
                          query: query)
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pdf")
        } catch {
            apiLog.error("summary pdf export failed discussion=\(id, privacy: .public) docType=\(docType, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Downloads the summary rendered as a PowerPoint deck. The server creates
    /// the deck on first export and reuses the cached S3 artifact afterwards.
    func downloadSummaryPPTX(id: String, title: String) async throws -> URL {
        var req = request(method: "GET", path: "/api/discussions/\(id)/summary/pptx")
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pptx")
        } catch {
            apiLog.error("summary pptx export failed discussion=\(id, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Downloads the generated summary slide deck converted to a PDF.
    func downloadSummarySlidesPDF(id: String, title: String) async throws -> URL {
        var req = request(method: "GET", path: "/api/discussions/\(id)/summary/ppt/pdf")
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pdf")
        } catch {
            apiLog.error("summary slides pdf export failed discussion=\(id, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Writes already-fetched summary Markdown to a temporary `.md` file and
    /// returns the local URL, so the Markdown export shares the PDF export's sheet.
    func writeSummaryMarkdown(_ markdown: String, title: String) throws -> URL {
        try writeSummaryFile(data: Data(markdown.utf8), title: title, ext: "md")
    }

    /// Writes summary export bytes to a uniquely-named temp file under
    /// `SummaryDownloads/`, sanitising the title into a safe base filename.
    private func writeSummaryFile(data: Data, title: String, ext: String) throws -> URL {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("SummaryDownloads", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: " -_"))
        let sanitized = title.unicodeScalars.map { allowed.contains($0) ? Character($0) : "-" }
        let baseName = String(sanitized).trimmingCharacters(in: CharacterSet(charactersIn: " -_"))
        let name = baseName.isEmpty ? "Summary" : String(baseName.prefix(80))
        let url = directory.appendingPathComponent(name).appendingPathExtension(ext)
        try data.write(to: url, options: .atomic)
        return url
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

    /// Creates an empty placeholder discussion (status "planning") and returns it
    /// with a server id, so the client can navigate to the plan page and stream
    /// the plan into it. Decouples creation from the multi-minute planning run.
    /// Creates the placeholder discussion from the raw new-discussion form values
    /// (the JSONSchemaForm output for GET /api/precheck). The server reads every
    /// field, so the client posts the form verbatim without interpreting any key.
    func createDiscussion(form: FormData,
                          referenceDiscussionID: String? = nil) async throws -> Discussion {
        try await send("POST", "/api/discussions",
                       body: DiscussionCreateRequest(form: form,
                                                     referenceDiscussionID: referenceDiscussionID))
    }

    /// Creates a private planning discussion by copying the current plan from a
    /// public or owned market discussion.
    func createDiscussionFromPlan(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/create/plan", body: EmptyRequest())
    }

    /// Streaming plan generation into an existing placeholder discussion: emits
    /// progress steps, finishing with a `.done(Discussion)` event carrying the
    /// persisted plan. The discussion is already saved server-side, so a dropped
    /// stream leaves a recoverable record instead of losing the work.
    func planDiscussionStream(id: String, _ req: PlanRequest) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(path: "/api/discussions/\(id)/plan/stream", body: req)
    }

    /// Streaming plan edit: revises the plan while emitting progress steps,
    /// finishing with a `.done(Discussion)` event carrying the updated plan.
    func improveDiscussionStream(id: String, instruction: String,
                                 attachments: [Attachment] = []) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(
            path: "/api/discussions/\(id)/improve/stream",
            body: DiscussionImproveRequest(instruction: instruction,
                                           attachments: attachments.isEmpty ? nil : attachments)
        )
    }

    /// Streaming re-research: reads the given links, folds them into the plan,
    /// and emits the updated discussion as a terminal `.done` event.
    func addDiscussionSourcesStream(id: String, urls: [String]) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(path: "/api/discussions/\(id)/sources/stream", body: AddSourcesRequest(urls: urls))
    }

    // MARK: - Conversational planning

    /// Loads the persisted conversational planning thread for history rebuild.
    func planningConversation(id: String) async throws -> PlanningConversationView {
        let view: PlanningConversationView = try await get("/api/discussions/\(id)/planning")
        apiLog.debug("planning conversation loaded id=\(id, privacy: .public) parts=\(view.parts.count, privacy: .public) needsRun=\((view.needsRun ?? false), privacy: .public) running=\((view.isRunning ?? false), privacy: .public)")
        return view
    }

    /// Starts or continues the conversational planning thread with a user
    /// message, streaming the agent's turn (text, tool cards, questions).
    func planningConversationStream(id: String, prompt: String,
                                    language: String? = nil,
                                    attachments: [Attachment] = []) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        apiLog.debug("planning SSE start kind=user id=\(id, privacy: .public) promptChars=\(prompt.count, privacy: .public) attachments=\(attachments.count, privacy: .public)")
        return streamPlanning(
            path: "/api/discussions/\(id)/planning/stream",
            body: PlanningStreamRequest(prompt: prompt, language: language, attachments: attachments.isEmpty ? nil : attachments)
        )
    }

    /// Resumes a server-seeded planning turn without appending another user
    /// message. Used when opening a freshly-created planning discussion.
    func resumePlanningConversation(id: String) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        apiLog.debug("planning SSE start kind=resume id=\(id, privacy: .public)")
        return streamPlanning(
            path: "/api/discussions/\(id)/planning/stream",
            body: PlanningStreamRequest(prompt: "", attachments: nil, resume: true)
        )
    }

    /// Reattaches to an already-running server-side planning stream. The server
    /// returns 204 when there is no active stream, which completes the sequence.
    func resumeActivePlanningStream(id: String) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        apiLog.debug("planning SSE start kind=active-resume id=\(id, privacy: .public)")
        return streamPlanningRequest(method: "GET", path: "/api/discussions/\(id)/planning/stream", body: nil)
    }

    /// Answers (or skips) a pending question, resuming the agent loop over SSE.
    func answerPlanningQuestion(id: String, questionId: String, action: String,
                                language: String? = nil,
                                answers: [[String: AnyCodable]]) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        apiLog.debug("planning SSE start kind=answer id=\(id, privacy: .public) question=\(questionId, privacy: .public) action=\(action, privacy: .public) answers=\(answers.count, privacy: .public)")
        return streamPlanning(
            path: "/api/discussions/\(id)/planning/answer",
            body: PlanningAnswerRequest(questionId: questionId, action: action, language: language, answers: answers)
        )
    }

    /// Runs a conversational-planning SSE request and re-emits its frames as
    /// `PlanningStreamEvent`s. Mirrors `streamPlan` but with the richer event set.
    private func streamPlanning<B: Encodable>(path: String, body: B) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let payload = try JSONEncoder().encode(body)
                    try await consumePlanningSSE(method: "POST", path: path, body: payload, continuation: continuation)
                } catch {
                    apiLog.error("planning SSE failed path=\(path, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func streamPlanningRequest(method: String, path: String, body payload: Data?) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await consumePlanningSSE(method: method, path: path, body: payload, continuation: continuation)
                } catch {
                    apiLog.error("planning SSE failed path=\(path, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func consumePlanningSSE(method: String, path: String, body payload: Data?,
                                    continuation: AsyncThrowingStream<PlanningStreamEvent, Error>.Continuation) async throws {
        guard let token = await tokens.token() else { throw APIError.notAuthenticated }
        var (bytes, http) = try await openSSE(method: method, path: path, body: payload, token: token)
        if http.statusCode == 401 {
            guard let fresh = await tokens.refreshedToken() else { throw APIError.notAuthenticated }
            (bytes, http) = try await openSSE(method: method, path: path, body: payload, token: fresh)
        }
        apiLog.debug("planning SSE opened path=\(path, privacy: .public) status=\(http.statusCode, privacy: .public)")
        if http.statusCode == 204 {
            continuation.finish()
            return
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
                Self.dispatchPlanningSSE(event: event, data: data, to: continuation)
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
        Self.dispatchPlanningSSE(event: event, data: data, to: continuation)
        apiLog.debug("planning SSE finished path=\(path, privacy: .public)")
        continuation.finish()
    }

    private static func dispatchPlanningSSE(event: String, data: String,
                                            to continuation: AsyncThrowingStream<PlanningStreamEvent, Error>.Continuation) {
        guard !data.isEmpty else { return }
        apiLog.debug("planning SSE received event=\(event, privacy: .public) bytes=\(data.utf8.count, privacy: .public)")
        switch event {
        case "text-delta":
            if let p = decodeSSE(PlanningTextDeltaPayload.self, data) {
                apiLog.debug("planning SSE text-delta chars=\(p.text.count, privacy: .public)")
                continuation.yield(.textDelta(p.text))
            } else {
                apiLog.error("planning SSE decode failed event=text-delta bytes=\(data.utf8.count, privacy: .public)")
            }
        case "tool-input-start":
            if let p = decodeSSE(PlanningToolInputStartPayload.self, data) {
                apiLog.debug("planning SSE tool-input-start tool=\(p.toolName, privacy: .public) id=\(p.toolCallId ?? "", privacy: .public)")
                continuation.yield(.toolInputStart(p))
            } else {
                apiLog.error("planning SSE decode failed event=tool-input-start bytes=\(data.utf8.count, privacy: .public)")
            }
        case "tool-input-delta":
            if let p = decodeSSE(PlanningToolInputDeltaPayload.self, data) {
                apiLog.debug("planning SSE tool-input-delta tool=\(p.toolName ?? "", privacy: .public) id=\(p.toolCallId ?? "", privacy: .public) chars=\(p.delta.count, privacy: .public)")
                continuation.yield(.toolInputDelta(p))
            } else {
                apiLog.error("planning SSE decode failed event=tool-input-delta bytes=\(data.utf8.count, privacy: .public)")
            }
        case "tool-call":
            if let p = decodeSSE(PlanningToolCallPayload.self, data) {
                apiLog.debug("planning SSE tool-call tool=\(p.toolName, privacy: .public) id=\(p.toolCallId, privacy: .public)")
                continuation.yield(.toolCall(p))
            } else {
                apiLog.error("planning SSE decode failed event=tool-call bytes=\(data.utf8.count, privacy: .public)")
            }
        case "tool-result":
            if let p = decodeSSE(PlanningToolResultPayload.self, data) {
                apiLog.debug("planning SSE tool-result tool=\(p.toolName, privacy: .public) id=\(p.toolCallId, privacy: .public) isError=\(p.isError ?? false, privacy: .public)")
                continuation.yield(.toolResult(p))
            } else {
                apiLog.error("planning SSE decode failed event=tool-result bytes=\(data.utf8.count, privacy: .public)")
            }
        case "plan":
            if let p = decodeSSE(PlanningPlanPayload.self, data) {
                apiLog.debug("planning SSE plan tool=\(p.toolName ?? "", privacy: .public) id=\(p.toolCallId, privacy: .public) hasScript=\((p.script != nil), privacy: .public)")
                continuation.yield(.plan(p))
            } else {
                apiLog.error("planning SSE decode failed event=plan bytes=\(data.utf8.count, privacy: .public)")
            }
        case "question_required":
            if let p = decodeSSE(QuestionPayload.self, data) {
                apiLog.debug("planning SSE question_required tool=\(p.toolName, privacy: .public) id=\(p.toolCallId, privacy: .public) questions=\(p.questions.count, privacy: .public)")
                continuation.yield(.question(p))
            } else {
                apiLog.error("planning SSE decode failed event=question_required bytes=\(data.utf8.count, privacy: .public)")
            }
        case "progress":
            if let p = decodeSSE(PlanProgressEvent.self, data) {
                apiLog.debug("planning SSE progress phase=\(p.phase, privacy: .public)")
                continuation.yield(.progress(p))
            } else {
                apiLog.error("planning SSE decode failed event=progress bytes=\(data.utf8.count, privacy: .public)")
            }
        case "done":
            if let p = decodeSSE(PlanningDonePayload.self, data) {
                apiLog.debug("planning SSE done parts=\(p.conversation.parts.count, privacy: .public)")
                continuation.yield(.done(p))
            } else {
                apiLog.error("planning SSE decode failed event=done bytes=\(data.utf8.count, privacy: .public)")
            }
        case "error":
            apiLog.error("planning SSE error event bytes=\(data.utf8.count, privacy: .public)")
            continuation.yield(.failed(sseErrorMessage(data)))
        default:
            apiLog.debug("planning SSE ignored event=\(event, privacy: .public)")
            break
        }
    }

    /// Re-research: starts a background server update for the given links, then
    /// polls the discussion until the updated plan is persisted.
    func addDiscussionSources(id: String, urls: [String]) async throws -> Discussion {
        let accepted: Discussion = try await send(
            "POST",
            "/api/discussions/\(id)/sources",
            body: AddSourcesRequest(urls: urls)
        )
        return try await waitForDiscussionUpdate(id: id, after: accepted)
    }

    func searchDiscussionSources(id: String, query: String) async throws -> [SourceDTO] {
        let response: SourceSearchResponse = try await send(
            "POST",
            "/api/discussions/\(id)/sources/search",
            body: SourceSearchRequest(query: query)
        )
        return response.sources
    }

    private func waitForDiscussionUpdate(id: String, after baseline: Discussion) async throws -> Discussion {
        let baselineUpdatedAt = baseline.updatedAt
        let baselineSourceCount = baseline.sortedSources.count
        for _ in 0..<150 {
            try await Task.sleep(for: .seconds(2))
            let current = try await discussion(id: id)
            if current.updatedAt != baselineUpdatedAt || current.sortedSources.count != baselineSourceCount {
                return current
            }
        }
        throw APIError.invalidRequest(String(localized: "The plan update is still running. Refresh this \(AppStringLiteral.stationNameRaw) in a moment.",
                                              comment: "Shown when a re-research update is still in progress after polling"))
    }

    private func marketList(path: String, limit: Int, offset: Int, query: String?) async throws -> [Discussion] {
        var queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        return try await get(path, query: queryItems)
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

    func notionStatus() async throws -> NotionStatusResponse {
        try await get("/api/notion/status")
    }

    func notionAuthURL() async throws -> URL {
        let response: NotionAuthURLResponse = try await get("/api/notion/oauth/url")
        guard let url = URL(string: response.authURL) else {
            throw APIError.decoding("invalid Notion OAuth URL")
        }
        return url
    }

    func searchNotionPages(query: String) async throws -> [NotionPageDTO] {
        let response: NotionPageSearchResponse = try await send(
            "POST",
            "/api/notion/pages/search",
            body: NotionPageSearchRequest(query: query)
        )
        return response.pages
    }

    func notionPageAttachment(pageID: String) async throws -> UploadResponse {
        try await send(
            "POST",
            "/api/notion/pages/attachment",
            body: NotionPageAttachmentRequest(pageID: pageID)
        )
    }

    /// Exports a podcast's generated summary to the user's Notion workspace,
    /// returning the created page (URL + id) so the UI can offer an "Open in
    /// Notion" action. A nil `parentPageID` creates a private root-level page.
    /// Throws 401 when Notion isn't connected, 404 when no summary exists.
    func exportSummaryToNotion(id: String, parentPageID: String?,
                               docType: String = "summary") async throws -> NotionExportResponse {
        try await send(
            "POST",
            "/api/discussions/\(id)/summary/notion",
            body: NotionExportRequest(parentPageID: parentPageID,
                                      docType: docType == "summary" ? nil : docType)
        )
    }

    /// Transcribes an already-uploaded voice message server-side (gateway whisper)
    /// when the device can't transcribe it on-device. Returns the recognized text,
    /// which may be empty if the audio held no speech.
    func transcribeAudio(key: String) async throws -> String {
        let resp: TranscribeResponse = try await send(
            "POST",
            "/api/transcribe",
            body: TranscribeRequest(audioKey: key)
        )
        return resp.text
    }

    func generateDiscussion(id: String, language: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/generate",
                       body: DiscussionGenerateRequest(language: language))
    }

    func deleteDiscussion(id: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/discussions/\(id)"))
    }

    func updateDiscussionVisibility(id: String,
                                    visibility: DiscussionVisibility,
                                    cover: DiscussionCover? = nil) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/visibility",
            body: DiscussionVisibilityRequest(visibility: visibility, cover: cover)
        )
    }

    /// Persists a cover on a discussion without publishing it, so any discussion
    /// can carry cover art (set from the new-discussion sheet or cover editor).
    func updateDiscussionCover(id: String, cover: DiscussionCover) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/cover",
            body: CoverUpdateRequest(cover: cover)
        )
    }

    /// The models available for per-speaker assignment, fetched live from the
    /// gateway and cached server-side (24h). Empty when the gateway exposes no
    /// model listing.
    func availableModels() async throws -> [ModelInfoDTO] {
        let response: ModelsResponseDTO = try await get("/api/models")
        return response.models ?? []
    }

    /// The content types currently supported by the planner. Today this is a
    /// single `discussion` entry, but the backend owns the list so clients do
    /// not hard-code future options.
    func discussionTypes() async throws -> [DiscussionTypeDTO] {
        let response: DiscussionTypesResponseDTO = try await get("/api/discussion-types")
        return response.types ?? []
    }

    /// Plan templates available for the selected content type. The server owns
    /// the available list so clients pick up new templates without an app update.
    func templates(type: String) async throws -> [PlanTemplateDTO] {
        let query = [URLQueryItem(name: "type", value: type)]
        let url = request(method: "GET", path: "/api/templates", query: query).url?.absoluteString ?? "/api/templates"
        apiLog.debug("templates request type=\(type, privacy: .public) url=\(url, privacy: .public)")
        do {
            let response: PlanTemplatesResponseDTO = try await get("/api/templates", query: query)
            let templates = response.templates ?? []
            let templateIDs = templates.map(\.id).joined(separator: ",")
            apiLog.debug("templates response type=\(type, privacy: .public) count=\(templates.count, privacy: .public) ids=\(templateIDs, privacy: .public)")
            return templates
        } catch {
            apiLog.error("templates request failed type=\(type, privacy: .public) url=\(url, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Server-owned client bootstrap metadata. The new discussion form schema,
    /// localized labels, and supported native actions are negotiated here.
    func precheck() async throws -> PrecheckResponseDTO {
        try await get("/api/precheck")
    }

    // MARK: - Push

    func registerPushToken(_ token: String, environment: String) async throws {
        try await sendNoContent("POST", "/api/push-tokens",
                                body: PushTokenRequest(token: token,
                                                       environment: environment))
    }

    func deletePushToken(_ token: String, environment: String) async throws {
        try await sendNoContent("DELETE", "/api/push-tokens",
                                body: PushTokenRequest(token: token,
                                                       environment: environment))
    }

    /// Changes the LLM model for one speaker (host or discussant, by name) in a
    /// discussion's plan and returns the updated discussion.
    func updateSpeakerModel(id: String, speaker: String, model: String) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/speaker-model",
            body: SpeakerModelRequest(speaker: speaker, model: model)
        )
    }

    func generateDiscussionCover(id: String, prompt: String) async throws -> DiscussionCover {
        let response: CoverGenerateResponse = try await send(
            "POST",
            "/api/discussions/\(id)/cover/generate",
            body: CoverGenerateRequest(prompt: prompt)
        )
        return response.cover
    }

    func appendDiscussionLine(id: String, line: DiscussionLineRequest, shareToken: String? = nil) async throws {
        let payload = try JSONEncoder().encode(line)
        var req = request(method: "POST", path: "/api/discussions/\(id)/lines", body: payload)
        if let shareToken, !shareToken.isEmpty {
            req.setValue(shareToken, forHTTPHeaderField: "X-Share-Token")
        }
        _ = try await perform(req)
    }

    // MARK: - Sharing

    /// Mints an expiring private share link for an owned discussion.
    func createShare(discussionID: String, ttlSeconds: Int) async throws -> DiscussionShareLink {
        let dto: ShareLinkDTO = try await send(
            "POST",
            "/api/discussions/\(discussionID)/shares",
            body: ShareCreateRequest(ttlSeconds: ttlSeconds)
        )
        guard let link = DiscussionShareLink(dto: dto) else {
            throw APIError.decoding("invalid share link url")
        }
        return link
    }

    /// Lists the active (non-expired, non-revoked) share links for a discussion.
    func listShares(discussionID: String) async throws -> [DiscussionShareLink] {
        let dtos: [ShareLinkDTO] = try await get("/api/discussions/\(discussionID)/shares")
        return dtos.compactMap(DiscussionShareLink.init(dto:))
    }

    /// Revokes a share token so its link stops working immediately.
    func revokeShare(discussionID: String, token: String) async throws {
        _ = try await perform(request(method: "DELETE",
                                      path: "/api/discussions/\(discussionID)/shares/\(pathComponent(token))"))
    }

    /// Records the caller as a participant, enforcing the per-discussion cap.
    /// Pass the share token when joining a private discussion via a link.
    /// Maps HTTP 409 to `APIError.participantCapReached`.
    func joinDiscussion(id: String, token: String?) async throws {
        do {
            try await sendNoContent("POST", "/api/discussions/\(id)/join",
                                    body: DiscussionJoinRequest(token: token))
        } catch APIError.http(409, _) {
            throw APIError.participantCapReached
        }
    }

    /// Resolves a private share token, joins (enforcing the cap), and returns the
    /// discussion with its transcript so the client can open the player. Maps
    /// HTTP 409 to `.participantCapReached` and 410 to a clear "link expired".
    func joinViaShare(token: String) async throws -> Discussion {
        do {
            return try await send("POST", "/api/share/\(pathComponent(token))/join", body: EmptyRequest())
        } catch APIError.http(409, _) {
            throw APIError.participantCapReached
        } catch APIError.http(410, _) {
            throw APIError.invalidRequest(String(localized: "This share link has expired or was revoked.",
                                                 comment: "Shown when opening an expired/revoked share link"))
        }
    }

    // MARK: - Marketplace

    func marketStations(limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/stations", limit: limit, offset: offset, query: query)
    }

    func likedMarketStations(limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/stations/liked", limit: limit, offset: offset, query: query)
    }

    func marketStation(id: String) async throws -> Discussion {
        try await get("/api/market/stations/\(id)")
    }

    func likeMarketStation(id: String) async throws -> Discussion {
        try await send("POST", "/api/market/stations/\(id)/like", body: EmptyRequest())
    }

    func unlikeMarketStation(id: String) async throws -> Discussion {
        let (data, _) = try await perform(request(method: "DELETE", path: "/api/market/stations/\(id)/like"))
        return try decode(data)
    }

    func marketProfile() async throws -> MarketProfile {
        try await get("/api/market/profile")
    }

    func creatorProfile(id: String) async throws -> CreatorProfile {
        try await get("/api/market/creators/\(pathComponent(id))")
    }

    func creatorStations(id: String, limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/creators/\(pathComponent(id))/stations", limit: limit, offset: offset, query: query)
    }

    func followedCreators(limit: Int = 20, offset: Int = 0) async throws -> [CreatorProfile] {
        try await get("/api/market/creators/following", query: [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ])
    }

    func followCreator(id: String) async throws -> CreatorProfile {
        try await send("POST", "/api/market/creators/\(pathComponent(id))/follow", body: EmptyRequest())
    }

    func unfollowCreator(id: String) async throws -> CreatorProfile {
        let (data, _) = try await perform(request(method: "DELETE", path: "/api/market/creators/\(pathComponent(id))/follow"))
        return try decode(data)
    }

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
        let name = baseName.isEmpty ? AppStringLiteral.stationNameRaw : String(baseName.prefix(80))
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

    /// Runs an SSE plan request and re-emits its `event:`/`data:` frames as
    /// `PlanStreamEvent`s. Handles one 401 refresh-and-retry before the stream
    /// body starts; cancelling the consuming task cancels the request.
    private func streamPlan<B: Encodable>(path: String, body: B) -> AsyncThrowingStream<PlanStreamEvent, Error> {
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

    private func openSSE(path: String, body: Data, token: String) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
        try await openSSE(method: "POST", path: path, body: body, token: token)
    }

    private func openSSE(method: String, path: String, body: Data?, token: String) async throws -> (URLSession.AsyncBytes, HTTPURLResponse) {
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

    private static func consumeSSELines(_ bytes: URLSession.AsyncBytes,
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

    private static func dispatchSSE(event: String, data: String,
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

    private static func decodeSSE<T: Decodable>(_ type: T.Type, _ data: String) -> T? {
        guard let raw = data.data(using: .utf8) else { return nil }
        do {
            return try JSONDecoder().decode(T.self, from: raw)
        } catch {
            apiLog.error("SSE JSON decode error type=\(String(describing: type), privacy: .public) bytes=\(data.utf8.count, privacy: .public) error=\(String(describing: error), privacy: .public)")
            return nil
        }
    }

    private static func sseErrorMessage(_ data: String) -> String {
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
        req.setValue(AcceptLanguage.headerValue, forHTTPHeaderField: "Accept-Language")
        req.setValue("ios", forHTTPHeaderField: "X-Client-Platform")
        req.setValue(Self.clientVersion, forHTTPHeaderField: "X-Client-Version")
        req.setValue(Self.clientBuild, forHTTPHeaderField: "X-Client-Build")
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return req
    }

    private static let clientVersion: String = {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
    }()

    private static let clientBuild: String = {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""
    }()

    private func pathComponent(_ value: String) -> String {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/?#[]@!$&'()*+,;=")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
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
            throw mapHTTPError(http.statusCode, data)
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
            continuation?.resume(throwing: APIError.invalidRequest(String(localized: "Download did not produce a file.",
                                                                          comment: "Shown when a podcast download completes without producing a file")))
        }
        continuation = nil
    }
}
