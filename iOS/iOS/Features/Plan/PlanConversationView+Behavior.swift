import SwiftUI

extension PlanConversationView {
    // MARK: - Derived state

    var canSend: Bool {
        (!input.trimmingCharacters(in: .whitespaces).isEmpty || !attachments.apiAttachments.isEmpty)
            && !attachments.isUploading
            && !isStreaming && !isGenerating && pendingQuestion == nil
    }

    var canGenerate: Bool {
        discussion.script != nil || parts.contains { $0.isPlanCard }
    }

    var speakerModelsDiscussionBinding: Binding<Discussion> {
        Binding(
            get: { discussion },
            set: { updated in
                discussion = updated
                syncVisiblePlanCards(from: updated)
            }
        )
    }

    func openSpeakerModels(for part: PlanningPart) {
        if discussion.script == nil, let script = part.script {
            discussion.script = script
            discussion.title = script.title
            discussion.markdown = part.markdown
            discussion.sources = part.sources
        }
        showingSpeakerModels = true
    }

    func openSources(for part: PlanningPart) {
        var sourceDiscussion = discussion
        if let script = part.script {
            sourceDiscussion.script = script
            sourceDiscussion.title = script.title
        }
        sourceDiscussion.markdown = part.markdown
        sourceDiscussion.sources = part.sources
        selectedSourcesDiscussion = sourceDiscussion
    }

    func openChapters(_ snapshot: PlanSnapshot) {
        if snapshot.isUploadedAudio {
            selectedTranscript = UploadedAudioTranscriptPresentation(snapshot: snapshot)
        } else {
            selectedChapters = PlanChaptersPresentation(title: snapshot.title, chapters: snapshot.chapters)
        }
    }

    func syncVisiblePlanCards(from updated: Discussion) {
        guard let script = updated.script else { return }
        for idx in parts.indices where parts[idx].isPlanCard {
            parts[idx].script = script
            parts[idx].sources = updated.sources
            parts[idx].markdown = updated.markdown
        }
    }

    // MARK: - Lifecycle

    func start() {
        guard !didStart else { return }
        didStart = true
        Task {
            if !didLoadHistory {
                didLoadHistory = true
                isLoadingHistory = true
                defer { isLoadingHistory = false }
                let api = APIClient(tokens: auth)
                async let conversationRequest: PlanningConversationView? = try? api.planningConversation(id: discussion.id)
                async let discussionRequest: Discussion? = try? api.discussion(
                    id: discussion.id,
                    includeEditTurns: false
                )
                let (view, latestDiscussion) = await (conversationRequest, discussionRequest)
                if let latestDiscussion {
                    discussion = latestDiscussion
                }
                if let view {
                    parts = view.parts
                    syncVisiblePlanCards(from: discussion)
                    requestInitialBottomScrollIfNeeded()
                }
                let conversationFailed = view?.conversation?.status == "failed"
                if !conversationFailed {
                    if view?.isRunning == true {
                        beginStream {
                            APIClient(tokens: auth).resumeActivePlanningStream(id: discussion.id)
                        }
                        return
                    }
                    // An uploaded-audio discussion whose transcription is still
                    // running has no conversation yet — poll the discussion
                    // until the transcript plan and the seeded review turn
                    // land, then auto-run the review.
                    if parts.isEmpty, discussion.status == .planning, isTranscribeInProgress(discussion) {
                        beginTranscribePolling()
                        return
                    }
                    // Connect to the plan stream when the server has a turn waiting
                    // to run (needs_run), or when this is a freshly created planning
                    // station whose server-seeded first turn hasn't surfaced/run yet
                    // (empty history, no plan — the history fetch can lag right after
                    // creation, or fail outright). The resume endpoint re-checks
                    // server-side and no-ops cleanly if there's nothing to run, so
                    // this is safe and idempotent.
                    let isUnstartedPlanning = parts.isEmpty
                        && discussion.status == .planning
                        && discussion.script == nil
                    if view?.needsRun == true || isUnstartedPlanning {
                        beginStream {
                            APIClient(tokens: auth).resumePlanningConversation(id: discussion.id)
                        }
                        return
                    }
                }
            }
            if parts.isEmpty, discussion.script != nil {
                // Legacy discussion (a plan made before conversational planning, with
                // no planning conversation yet): seed a plan card from the saved plan
                // so it's visible and can be generated/refined here.
                parts = [PlanningPart(
                    kind: "tool",
                    id: "legacy-plan",
                    toolName: "show_plan",
                    status: "completed",
                    script: discussion.script,
                    sources: discussion.sources,
                    markdown: discussion.markdown
                )]
                requestInitialBottomScrollIfNeeded()
            }
        }
    }

    func isTranscribeInProgress(_ d: Discussion) -> Bool {
        d.progress?.operation == "transcribe" && d.progress?.active == true
    }

    /// Polls the discussion while its uploaded audio transcribes server-side.
    /// When the transcript plan lands (the server also seeds the review turn),
    /// this loads the conversation and auto-runs the AI transcript review.
    func beginTranscribePolling() {
        guard !isTranscribing else { return }
        isTranscribing = true
        transcribePollTask?.cancel()
        transcribePollTask = Task { @MainActor in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3))
                guard !Task.isCancelled else { return }
                guard let updated = try? await APIClient(tokens: auth).discussion(id: discussion.id) else { continue }
                discussion = updated
                if updated.status == .failed {
                    isTranscribing = false
                    errorMessage = String(localized: "Transcription failed. Please delete this \(AppStringLiteral.stationNameRaw) and try again.",
                                          comment: "Shown when server-side audio transcription failed")
                    return
                }
                if isTranscribeInProgress(updated) {
                    continue
                }
                // Progress cleared: the transcript should be stored now.
                isTranscribing = false
                if let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id) {
                    parts = view.parts
                    requestInitialBottomScrollIfNeeded()
                    if view.needsRun == true {
                        beginStream {
                            APIClient(tokens: auth).resumePlanningConversation(id: discussion.id)
                        }
                    }
                }
                return
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

    @MainActor
    func loadLanguageOptions() async {
        guard languageOptions.isEmpty else { return }
        do {
            let form = try await APIClient(tokens: auth).precheck().newDiscussion.form
            languageOptions = form.languageOptions
        } catch {
            languageOptions = []
        }
    }

    // MARK: - Sending / streaming

    func send() {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        let ready = attachments.apiAttachments
        guard !text.isEmpty || !ready.isEmpty else { return }
        input = ""
        attachments = []
        // A vertical-axis TextField that is the first responder doesn't always
        // refresh its displayed text when the binding is reset synchronously inside
        // the send action. Re-assert the empty value on the next runloop so the
        // field visibly clears without dismissing the keyboard.
        DispatchQueue.main.async { input = "" }
        send(prompt: text.isEmpty ? String(localized: "Please use the attached files.", comment: "Fallback chat message when sending attachments without text") : text,
             attachments: ready)
    }

    func send(prompt: String, attachments: [Attachment]) {
        clearPlanningError()
        // Optimistic user bubble; the server echoes it back in the final parts.
        parts.append(PlanningPart(kind: "text",
                                  id: "local-user-\(UUID().uuidString)",
                                  role: "user",
                                  text: prompt,
                                  attachments: attachments.isEmpty ? nil : attachments))
        beginStream {
            APIClient(tokens: auth).planningConversationStream(id: discussion.id, prompt: prompt,
                                                               language: selectedLanguage,
                                                               attachments: attachments)
        }
    }

    func answer(question: QuestionPayload, answers: [[String: AnyCodable]]) {
        pendingQuestion = nil
        clearPlanningError()
        beginStream {
            APIClient(tokens: auth).answerPlanningQuestion(id: discussion.id, questionId: question.questionId,
                                                           action: "answered", language: selectedLanguage,
                                                           answers: answers)
        }
    }

    func reject(question: QuestionPayload) {
        pendingQuestion = nil
        clearPlanningError()
        beginStream {
            APIClient(tokens: auth).answerPlanningQuestion(id: discussion.id, questionId: question.questionId,
                                                           action: "rejected", language: selectedLanguage,
                                                           answers: [])
        }
    }

    func beginStream(_ makeStream: @escaping () -> AsyncThrowingStream<PlanningStreamEvent, Error>) {
        isStreaming = true
        streamHasActivity = false
        progressText = String(localized: "Thinking…", comment: "Progress text while the planning agent works")
        streamTask?.cancel()
        let stream = makeStream()
        streamTask = Task {
            do {
                for try await event in stream {
                    handle(event)
                }
                // Stream closed without a done event — fall back to a fresh fetch.
                if isStreaming {
                    if let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id) {
                        parts = view.parts
                        if view.isRunning == true {
                            streamTask = nil
                            beginStream {
                                APIClient(tokens: auth).resumeActivePlanningStream(id: discussion.id)
                            }
                            return
                        }
                    }
                    isStreaming = false
                    progressText = nil
                }
            } catch {
                if case let APIError.insufficientPoints(required, balance) = error {
                    isStreaming = false
                    progressText = nil
                    presentPlanningError(
                        String(localized: "You need \(required) points but have \(balance).",
                               comment: "Shown when the user lacks enough points; values are point amounts"),
                        offersTopUp: true
                    )
                    await purchases.refreshBalance()
                    return
                }
                isStreaming = false
                progressText = nil
                presentPlanningError((error as? APIError)?.errorDescription ?? error.localizedDescription,
                                     offersTopUp: false)
                if let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id),
                   view.isRunning == true
                {
                    parts = view.parts
                    clearPlanningError()
                    streamTask = nil
                    beginStream {
                        APIClient(tokens: auth).resumeActivePlanningStream(id: discussion.id)
                    }
                }
            }
        }
    }

    func handle(_ event: PlanningStreamEvent) {
        switch event {
        case let .textDelta(delta):
            streamHasActivity = true
            upsertPart(id: "assistant-stream") { existing in
                var p = existing ?? PlanningPart(kind: "text", id: "assistant-stream", role: "assistant", text: "")
                p.text = (p.text ?? "") + delta
                return p
            }
        case let .toolInputStart(payload):
            streamHasActivity = true
            progressText = friendlyToolStatus(payload.toolName)
            if let id = payload.toolCallId, !id.isEmpty {
                upsertPart(id: "tc-\(id)") { existing in
                    var p = existing ?? PlanningPart(kind: "tool", id: "tc-\(id)")
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
                var p = existing ?? PlanningPart(kind: "tool", id: "tc-\(id)")
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
                var p = existing ?? PlanningPart(kind: "tool", id: "tc-\(payload.toolCallId)")
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
                var p = existing ?? PlanningPart(kind: "tool", id: "tc-\(payload.toolCallId)", toolCallID: payload.toolCallId, toolName: payload.toolName)
                p.status = (payload.isError ?? false) ? "failed" : "completed"
                p.resultText = payload.output?.prettyString
                return p
            }
        case let .plan(payload):
            streamHasActivity = true
            if let script = payload.script {
                discussion.script = script
                discussion.title = script.title
                discussion.markdown = payload.markdown
                discussion.sources = payload.sources
            }
            parts.removeAll { $0.id == "tc-\(payload.toolCallId)" }
            removeVisiblePlanCards(except: "current-plan")
            upsertPart(id: "current-plan") { existing in
                var p = existing ?? PlanningPart(kind: "tool", id: "current-plan", toolCallID: payload.toolCallId)
                p.toolCallID = payload.toolCallId
                p.toolName = payload.toolName ?? "show_plan"
                p.status = "completed"
                p.script = payload.script
                p.sources = payload.sources
                p.markdown = payload.markdown
                return p
            }
        case let .question(payload):
            streamHasActivity = true
            progressText = nil
            upsertPart(id: "tc-\(payload.toolCallId)") { existing in
                var p = existing ?? PlanningPart(kind: "tool", id: "tc-\(payload.toolCallId)", toolCallID: payload.toolCallId)
                p.toolName = payload.toolName
                p.status = "pending_question"
                p.questionID = payload.questionId
                p.questions = payload.questions
                return p
            }
            pendingQuestion = payload
        case let .progress(ev):
            progressText = ev.text
        case let .done(payload):
            if let updated = payload.discussion { discussion = updated }
            parts = payload.conversation.parts
            isStreaming = false
            progressText = nil
        case let .failed(message):
            isStreaming = false
            progressText = nil
            errorMessage = message
        }
    }

    /// Inserts or updates the part with the given id, preserving order.
    func upsertPart(id: String, _ mutate: (PlanningPart?) -> PlanningPart) {
        if let idx = parts.firstIndex(where: { $0.id == id }) {
            parts[idx] = mutate(parts[idx])
        } else {
            parts.append(mutate(nil))
        }
    }

    func friendlyToolStatus(_ name: String) -> String {
        switch name {
        case "search_sources": return String(localized: "Searching the web…", comment: "Progress while searching sources")
        case "crawl_sources": return String(localized: "Reading links…", comment: "Progress while reading URLs")
        case "write_plan", "update_plan": return String(localized: "Writing the plan…", comment: "Progress while writing the plan")
        case "show_plan": return String(localized: "Showing the plan…", comment: "Progress while showing the plan")
        case "ask_question": return String(localized: "Preparing a question…", comment: "Progress while preparing a question")
        default: return String(localized: "Working…", comment: "Generic progress text")
        }
    }

    func removeVisiblePlanCards(except id: String) {
        parts.removeAll { $0.id != id && $0.isPlanCard }
    }

    // MARK: - Generate

    func generate(chapters: [Int]? = nil) {
        isGenerating = true
        clearPlanningError()
        Task {
            do {
                discussion = try await APIClient(tokens: auth).generateDiscussion(id: discussion.id, language: selectedLanguage, chapters: chapters)
                isGenerating = false
                onGenerated(discussion)
            } catch let APIError.insufficientPoints(required, balance) {
                isGenerating = false
                presentPlanningError(
                    String(localized: "You need \(required) points but have \(balance).",
                           comment: "Shown when the user lacks enough points; values are point amounts"),
                    offersTopUp: true
                )
                await purchases.refreshBalance()
            } catch {
                isGenerating = false
                presentPlanningError((error as? APIError)?.errorDescription ?? error.localizedDescription,
                                     offersTopUp: false)
            }
        }
    }

    func presentPlanningError(_ message: String, offersTopUp: Bool) {
        inputFocused = false
        errorMessage = message
        errorOffersTopUp = offersTopUp
        showingErrorAlert = true
    }

    func clearPlanningError() {
        errorMessage = nil
        errorOffersTopUp = false
        showingErrorAlert = false
    }
}
