import SwiftUI

extension QAConversationView {
    var canSend: Bool {
        !input.trimmingCharacters(in: .whitespaces).isEmpty && !isStreaming
    }

    // MARK: - Lifecycle

    func start() {
        guard !didStart else { return }
        didStart = true
        Task {
            isLoadingHistory = true
            defer { isLoadingHistory = false }
            let view = try? await APIClient(tokens: auth).qaConversation(discussionID: scope.discussionID)
            if let view {
                parts = view.parts
                requestInitialBottomScrollIfNeeded()
                if view.isRunning == true {
                    beginStream {
                        APIClient(tokens: auth).resumeActiveQAStream(discussionID: scope.discussionID)
                    }
                }
            }
        }
    }

    func requestInitialBottomScrollIfNeeded() {
        guard !didRequestInitialBottomScroll, !rows.isEmpty else { return }
        didRequestInitialBottomScroll = true
        initialScrollTask?.cancel()
        shouldScrollToInitialBottom = false
        initialScrollTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(10))
            guard !Task.isCancelled else { return }
            shouldScrollToInitialBottom = true
        }
    }

    // MARK: - Sending / streaming

    func send() {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        input = ""
        DispatchQueue.main.async { input = "" }
        clearError()
        parts.append(QAPart(kind: "text",
                            id: "local-user-\(UUID().uuidString)",
                            role: "user",
                            text: text))
        beginStream {
            APIClient(tokens: auth).qaStream(discussionID: scope.discussionID, prompt: text, language: language)
        }
    }

    func clearAllMessages() {
        guard allowsClearingMessages, !parts.isEmpty, !isStreaming, !isClearingMessages else { return }
        inputFocused = false
        isClearingMessages = true
        clearMessagesError = nil
        Task {
            defer { isClearingMessages = false }
            do {
                try await APIClient(tokens: auth).clearQAConversation(discussionID: scope.discussionID)
                parts = []
                input = ""
                progressText = nil
                shouldScrollToInitialBottom = false
                didRequestInitialBottomScroll = false
            } catch {
                clearMessagesError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    func beginStream(_ makeStream: @escaping () -> AsyncThrowingStream<QAStreamEvent, Error>) {
        isStreaming = true
        streamHasActivity = false
        progressText = nil
        streamTask?.cancel()
        let stream = makeStream()
        streamTask = Task {
            do {
                for try await event in stream {
                    handle(event)
                }
                // Stream closed without a done frame — refresh, and reattach if
                // the server-side run is still going.
                if isStreaming {
                    if let view = try? await APIClient(tokens: auth).qaConversation(discussionID: scope.discussionID) {
                        parts = view.parts
                        if view.isRunning == true {
                            streamTask = nil
                            beginStream {
                                APIClient(tokens: auth).resumeActiveQAStream(discussionID: scope.discussionID)
                            }
                            return
                        }
                    }
                    isStreaming = false
                }
            } catch {
                isStreaming = false
                if case let APIError.insufficientPoints(required, balance) = error {
                    presentError(
                        String(localized: "You need \(required) points but have \(balance).",
                               comment: "Shown when the user lacks enough points; values are point amounts"),
                        offersTopUp: true
                    )
                    await purchases.refreshBalance()
                    return
                }
                presentError((error as? APIError)?.errorDescription ?? error.localizedDescription,
                             offersTopUp: false)
            }
        }
    }

    func handle(_ event: QAStreamEvent) {
        switch event {
        case let .textDelta(delta):
            streamHasActivity = true
            upsertPart(id: "assistant-stream") { existing in
                var p = existing ?? QAPart(kind: "text", id: "assistant-stream", role: "assistant", text: "")
                p.text = (p.text ?? "") + delta
                return p
            }
        case let .toolInputStart(payload):
            streamHasActivity = true
            progressText = friendlyToolStatus(payload.toolName)
            if let id = payload.toolCallId, !id.isEmpty {
                upsertPart(id: "tc-\(id)") { existing in
                    var p = existing ?? QAPart(kind: "tool", id: "tc-\(id)")
                    p.kind = "tool"
                    p.toolCallID = id
                    p.toolName = payload.toolName
                    p.status = "running"
                    return p
                }
            }
        case let .toolInputDelta(payload):
            streamHasActivity = true
            guard let id = payload.toolCallId, !id.isEmpty else { return }
            upsertPart(id: "tc-\(id)") { existing in
                var p = existing ?? QAPart(kind: "tool", id: "tc-\(id)")
                p.kind = "tool"
                p.toolCallID = id
                p.toolName = payload.toolName ?? p.toolName
                p.status = "running"
                p.inputText = (p.inputText ?? "") + payload.delta
                return p
            }
        case let .toolCall(payload):
            streamHasActivity = true
            upsertPart(id: "tc-\(payload.toolCallId)") { existing in
                var p = existing ?? QAPart(kind: "tool", id: "tc-\(payload.toolCallId)")
                p.kind = "tool"
                p.toolCallID = payload.toolCallId
                p.toolName = payload.toolName
                p.status = "running"
                p.input = payload.input
                return p
            }
        case let .toolResult(payload):
            streamHasActivity = true
            upsertPart(id: "tc-\(payload.toolCallId)") { existing in
                var p = existing ?? QAPart(kind: "tool", id: "tc-\(payload.toolCallId)", toolCallID: payload.toolCallId, toolName: payload.toolName)
                p.status = (payload.isError ?? false) ? "failed" : "completed"
                p.resultText = payload.output?.prettyString
                return p
            }
        case let .card(payload):
            streamHasActivity = true
            upsertPart(id: "tc-\(payload.toolCallId)") { existing in
                var p = existing ?? QAPart(kind: "tool", id: "tc-\(payload.toolCallId)", toolCallID: payload.toolCallId)
                p.kind = "tool"
                p.toolName = payload.toolName ?? p.toolName
                p.status = "completed"
                p.card = payload.card
                return p
            }
        case let .progress(ev):
            progressText = ev.text
        case let .done(payload):
            parts = payload.conversation.parts
            isStreaming = false
            progressText = nil
        case let .failed(message):
            isStreaming = false
            progressText = nil
            presentError(message, offersTopUp: false)
        }
    }

    /// Inserts or updates the part with the given id, preserving order.
    func upsertPart(id: String, _ mutate: (QAPart?) -> QAPart) {
        if let idx = parts.firstIndex(where: { $0.id == id }) {
            parts[idx] = mutate(parts[idx])
        } else {
            parts.append(mutate(nil))
        }
    }

    func friendlyToolStatus(_ name: String) -> String {
        switch name {
        case "search_summary": return String(localized: "Searching summaries…", comment: "Progress while the chat agent searches podcast summaries")
        case "search_content": return String(localized: "Searching the content…", comment: "Progress while the chat agent searches podcast content")
        case "search_podcasts": return String(localized: "Finding podcasts…", comment: "Progress while the chat agent searches podcasts")
        case "display_podcasts": return String(localized: "Preparing podcasts…", comment: "Progress while the chat agent prepares a podcast grid")
        case "show_podcasts", "show_highlight_lines": return String(localized: "Preparing highlights…", comment: "Progress while the chat agent prepares podcast highlights")
        case "get_sources", "show_sources": return String(localized: "Looking up sources…", comment: "Progress while the chat agent fetches sources")
        case "show_transcript": return String(localized: "Fetching the transcript…", comment: "Progress while the chat agent loads a transcript excerpt")
        case "show_podcast": return String(localized: "Fetching the podcast…", comment: "Progress while the chat agent loads a podcast card")
        case "display_mindmap": return String(localized: "Preparing the mindmap…", comment: "Progress while the chat agent prepares a mindmap card")
        case "display_ppt": return String(localized: "Preparing the presentation…", comment: "Progress while the chat agent prepares a PPT card")
        case "write_document": return String(localized: "Writing the document…", comment: "Progress while the chat agent writes a persistent document")
        default: return String(localized: "Working…", comment: "Generic progress text")
        }
    }

    func openPodcast(id: String) {
        onOpenPodcast?(id)
    }

    func presentError(_ message: String, offersTopUp: Bool) {
        inputFocused = false
        errorMessage = message
        errorOffersTopUp = offersTopUp
        showingErrorAlert = true
    }

    func clearError() {
        errorMessage = nil
        errorOffersTopUp = false
        showingErrorAlert = false
    }
}
