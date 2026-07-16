import Foundation
import JSONSchemaForm
import OSLog

extension APIClient {
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
    func streamPlanning<B: Encodable>(path: String, body: B) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
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

    func streamPlanningRequest(method: String, path: String, body payload: Data?) -> AsyncThrowingStream<PlanningStreamEvent, Error> {
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

    func consumePlanningSSE(method: String, path: String, body payload: Data?,
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

    static func dispatchPlanningSSE(event: String, data: String,
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

    func waitForDiscussionUpdate(id: String, after baseline: Discussion) async throws -> Discussion {
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

    func marketList(path: String, limit: Int, offset: Int, query: String?) async throws -> [Discussion] {
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

    /// Uploads a full-length podcast audio file for the upload-own-audio flow.
    /// Streams straight from disk with an upload task (files can be hundreds of
    /// MB, so they are never loaded into memory) using kind "podcast-audio",
    /// which the server gates by entitlement and caps per subscription tier.
    func uploadPodcastAudio(fileURL: URL, filename: String, mimeType: String) async throws -> UploadResponse {
        let kind = "podcast-audio"
        let presign: UploadPresignResponse = try await send(
            "POST",
            "/api/uploads/presign",
            body: UploadPresignRequest(filename: filename, mimeType: mimeType, kind: kind)
        )

        var uploadReq = URLRequest(url: presign.uploadURL)
        uploadReq.httpMethod = presign.method
        uploadReq.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        for (name, value) in presign.headers {
            uploadReq.setValue(value, forHTTPHeaderField: name)
        }
        let (uploadData, uploadResp) = try await session.upload(for: uploadReq, fromFile: fileURL)
        guard let uploadHTTP = uploadResp as? HTTPURLResponse,
              (200..<300).contains(uploadHTTP.statusCode) else {
            let status = (uploadResp as? HTTPURLResponse)?.statusCode ?? 0
            throw APIError.http(status, String(decoding: uploadData, as: UTF8.self))
        }

        return try await send(
            "POST",
            "/api/uploads/complete",
            body: UploadCompleteRequest(key: presign.key, filename: filename, mimeType: mimeType, kind: kind)
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
                               docType: String = "summary", language: String? = nil) async throws -> NotionExportResponse {
        try await send(
            "POST",
            "/api/discussions/\(id)/summary/notion",
            body: NotionExportRequest(parentPageID: parentPageID,
                                      docType: docType == "summary" ? nil : docType,
                                      language: language)
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

    func generateDiscussion(id: String, language: String, chapters: [Int]? = nil) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/generate",
                       body: DiscussionGenerateRequest(language: language, chapters: chapters))
    }

    /// Persists the plan-view language selection immediately, so the choice
    /// survives leaving the plan without generating.
    func updateDiscussionLanguage(id: String, language: String) async throws -> Discussion {
        try await send("PUT", "/api/discussions/\(id)/language",
                       body: DiscussionLanguageRequest(language: language))
    }

    /// Fetches the root plan's full chapter list annotated with per-chapter
    /// generation progress, for the chapter-checklist sheet.
    func discussionChapters(id: String) async throws -> ChaptersResponse {
        try await get("/api/discussions/\(id)/chapters")
    }

    /// Creates and starts a follow-up chapter batch: a new podcast linked to
    /// the audiobook root that narrates the selected pending chapters (max 5).
    /// The server returns 400 when the selection exceeds the limit or overlaps
    /// generated chapters.
    func generateChapters(id: String, chapters: [Int], language: String? = nil) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/chapters/generate",
                       body: ChaptersGenerateRequest(chapters: chapters, language: language))
    }

}
