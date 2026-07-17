import Foundation
import OSLog

extension APIClient {
    // MARK: - Q&A / global chat

    private static func qaBasePath(_ discussionID: String?) -> String {
        if let discussionID, !discussionID.isEmpty {
            return "/api/discussions/\(discussionID)/qa"
        }
        return "/api/chat"
    }

    /// Loads the persisted Q&A conversation (podcast scope) or the user's
    /// global chat (nil discussionID) for history rebuild.
    func qaConversation(discussionID: String?) async throws -> QAConversationViewDTO {
        let view: QAConversationViewDTO = try await get(Self.qaBasePath(discussionID))
        apiLog.debug("qa conversation loaded scope=\(discussionID ?? "global", privacy: .public) parts=\(view.parts.count, privacy: .public) running=\((view.isRunning ?? false), privacy: .public)")
        return view
    }

    /// Permanently clears one persisted Q&A conversation without deleting its
    /// billing metadata. A nil discussion ID addresses the user's global chat.
    func clearQAConversation(discussionID: String?) async throws {
        _ = try await perform(request(method: "DELETE", path: Self.qaBasePath(discussionID)))
    }

    /// Sends a user message and streams the agent's turn.
    func qaStream(discussionID: String?, prompt: String, language: String? = nil) -> AsyncThrowingStream<QAStreamEvent, Error> {
        apiLog.debug("qa SSE start kind=user scope=\(discussionID ?? "global", privacy: .public) promptChars=\(prompt.count, privacy: .public)")
        return streamQARequest(method: "POST",
                               path: Self.qaBasePath(discussionID) + "/stream",
                               body: try? JSONEncoder().encode(QAStreamRequest(prompt: prompt, language: language)))
    }

    /// Reattaches to an already-running Q&A stream. 204 completes the sequence.
    func resumeActiveQAStream(discussionID: String?) -> AsyncThrowingStream<QAStreamEvent, Error> {
        apiLog.debug("qa SSE start kind=active-resume scope=\(discussionID ?? "global", privacy: .public)")
        return streamQARequest(method: "GET", path: Self.qaBasePath(discussionID) + "/stream", body: nil)
    }

    private func streamQARequest(method: String, path: String, body payload: Data?) -> AsyncThrowingStream<QAStreamEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    try await consumeQASSE(method: method, path: path, body: payload, continuation: continuation)
                } catch {
                    apiLog.error("qa SSE failed path=\(path, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func consumeQASSE(method: String, path: String, body payload: Data?,
                              continuation: AsyncThrowingStream<QAStreamEvent, Error>.Continuation) async throws {
        guard let token = await tokens.token() else { throw APIError.notAuthenticated }
        var (bytes, http) = try await openSSE(method: method, path: path, body: payload, token: token)
        if http.statusCode == 401 {
            guard let fresh = await tokens.refreshedToken() else { throw APIError.notAuthenticated }
            (bytes, http) = try await openSSE(method: method, path: path, body: payload, token: fresh)
        }
        apiLog.debug("qa SSE opened path=\(path, privacy: .public) status=\(http.statusCode, privacy: .public)")
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
                Self.dispatchQASSE(event: event, data: data, to: continuation)
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
        Self.dispatchQASSE(event: event, data: data, to: continuation)
        apiLog.debug("qa SSE finished path=\(path, privacy: .public)")
        continuation.finish()
    }

    static func dispatchQASSE(event: String, data: String,
                              to continuation: AsyncThrowingStream<QAStreamEvent, Error>.Continuation) {
        guard !data.isEmpty else { return }
        switch event {
        case "text-delta":
            if let p = decodeSSE(PlanningTextDeltaPayload.self, data) {
                continuation.yield(.textDelta(p.text))
            }
        case "tool-input-start":
            if let p = decodeSSE(PlanningToolInputStartPayload.self, data) {
                continuation.yield(.toolInputStart(p))
            }
        case "tool-input-delta":
            if let p = decodeSSE(PlanningToolInputDeltaPayload.self, data) {
                continuation.yield(.toolInputDelta(p))
            }
        case "tool-call":
            if let p = decodeSSE(PlanningToolCallPayload.self, data) {
                continuation.yield(.toolCall(p))
            }
        case "tool-result":
            if let p = decodeSSE(PlanningToolResultPayload.self, data) {
                continuation.yield(.toolResult(p))
            }
        case "podcast", "podcasts", "podcast_highlights", "highlight_lines", "transcript", "sources", "mindmap", "ppt", "document":
            if let p = decodeSSE(QACardPayload.self, data) {
                continuation.yield(.card(p))
            } else {
                apiLog.error("qa SSE decode failed event=\(event, privacy: .public) bytes=\(data.utf8.count, privacy: .public)")
            }
        case "progress":
            if let p = decodeSSE(PlanProgressEvent.self, data) {
                continuation.yield(.progress(p))
            }
        case "done":
            if let p = decodeSSE(QADonePayload.self, data) {
                continuation.yield(.done(p))
            } else {
                apiLog.error("qa SSE decode failed event=done bytes=\(data.utf8.count, privacy: .public)")
            }
        case "error":
            continuation.yield(.failed(sseErrorMessage(data)))
        default:
            break
        }
    }

    // MARK: - Agent documents

    func agentDocuments(discussionID: String? = nil) async throws -> [AgentDocumentDTO] {
        let path = discussionID.map { "/api/discussions/\($0)/documents" } ?? "/api/documents"
        let response: AgentDocumentListResponse = try await get(path)
        return response.documents
    }

    func allAgentDocuments(limit: Int = 20,
                           offset: Int = 0,
                           query: String = "") async throws -> AgentDocumentListResponse {
        var queryItems = [
            URLQueryItem(name: "scope", value: "all"),
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset))
        ]
        let trimmedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        return try await get("/api/documents", query: queryItems)
    }

    func deleteAgentDocument(id: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/documents/\(pathComponent(id))"))
    }

    func agentDocument(id: String) async throws -> AgentDocumentDTO {
        try await get("/api/documents/\(id)")
    }

    func agentDocumentUIActions(id: String) async throws -> DiscussionUIActionsResponse {
        try await get("/api/documents/\(id)/ui-actions")
    }

    func downloadAgentDocumentPDF(id: String, title: String) async throws -> URL {
        var req = request(method: "GET", path: "/api/documents/\(id)/pdf")
        req.timeoutInterval = Self.summaryExportTimeout
        let (data, _) = try await perform(req)
        return try writeSummaryFile(data: data, title: title, ext: "pdf")
    }

    func exportAgentDocumentToNotion(id: String, parentPageID: String?) async throws -> NotionExportResponse {
        try await send(
            "POST",
            "/api/documents/\(id)/notion",
            body: NotionExportRequest(parentPageID: parentPageID, docType: nil)
        )
    }

    // MARK: - Semantic search

    /// Global semantic search over the user's indexed podcast content, grouped
    /// by podcast. `enabled == false` means embeddings are unconfigured.
    func semanticSearch(query: String, limit: Int? = nil) async throws -> SemanticSearchResponse {
        try await send("POST", "/api/search/semantic",
                       body: SemanticSearchRequest(query: query, limit: limit))
    }

    /// Semantic search within one owned podcast's transcript + sources.
    func discussionSemanticSearch(id: String, query: String, limit: Int? = nil) async throws -> DiscussionSemanticSearchResponse {
        try await send("POST", "/api/discussions/\(id)/search",
                       body: SemanticSearchRequest(query: query, limit: limit))
    }
}

struct QAStreamRequest: Codable, Sendable {
    var prompt: String
    var language: String?
}
