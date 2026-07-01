import SwiftUI

/// Conversational planning screen: the agent gathers context, asks the user
/// questions (bottom sheet), and writes/refines the plan over an SSE stream. Each
/// tool call shows as an inline card; `show_plan` renders the current plan card.
struct PlanConversationView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @State var discussion: Discussion
    var onGenerated: (Discussion) -> Void = { _ in }

    @State private var parts: [PlanningPart] = []
    @State private var input = ""
    @State private var isStreaming = false
    @State private var progressText: String?
    @State private var errorMessage: String?
    @State private var pendingQuestion: QuestionPayload?
    @State private var selectedToolPart: PlanningPart?
    @State private var selectedAttachment: AttachmentPreviewItem?
    @State private var selectedReference: PodcastReference?
    @State private var selectedSourcesDiscussion: Discussion?
    @State private var selectedChapters: PlanChaptersPresentation?
    @State private var isGenerating = false
    @State private var showingGenerateConfirm = false
    @State private var showingPaywall = false
    @State private var showingSpeakerModels = false
    @State private var didStart = false
    @State private var didLoadHistory = false
    @State private var isLoadingHistory = false
    @State private var historyLoadingPulse = false
    @State private var editIsAtBottom = true
    @State private var attachments: [PendingAttachment] = []
    @State private var shouldScrollToInitialBottom = false
    @State private var didRequestInitialBottomScroll = false
    @State private var selectedLanguage: String
    @State private var languageOptions: [PlanLanguageOption] = []
    @State private var streamTask: Task<Void, Never>?
    @State private var initialScrollTask: Task<Void, Never>?
    @State private var streamHasActivity = false
    @FocusState private var inputFocused: Bool

    init(discussion: Discussion,
         onGenerated: @escaping (Discussion) -> Void = { _ in }) {
        _discussion = State(initialValue: discussion)
        _selectedLanguage = State(initialValue: PlanLanguageOption.initialCode(discussion.script?.language ?? discussion.language))
        self.onGenerated = onGenerated
    }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()

            if isShowingHistorySkeleton {
                historyLoadingSkeleton
                    .transition(.opacity)
            } else {
                MessageList(
                    messages: rows,
                    isStreaming: isStreaming,
                    shouldScrollToBottom: shouldScrollToInitialBottom,
                    scrollToBottomAnimated: false,
                    isAtBottom: $editIsAtBottom
                ) { row in
                    rowView(row)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 7)
                }
                .scrollDismissesKeyboard(.interactively)
                .contentMargins(.bottom, 96, for: .scrollContent)

                VStack { Spacer(); editBar }
            }
        }
        .animation(.easeInOut(duration: 0.18), value: isShowingHistorySkeleton)
        .animation(.spring(response: 0.34, dampingFraction: 0.86, blendDuration: 0.08), value: animatedRowIDs)
        .navigationTitle(discussion.title.isEmpty ? "Plan" : discussion.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { toolbarContent }
        .sheet(item: $pendingQuestion) { question in
            QuestionSheetView(
                question: question,
                remainingCount: 0,
                onAnswer: { answers in answer(question: question, answers: answers) },
                onReject: { reject(question: question) }
            )
        }
        .sheet(item: $selectedToolPart) { part in
            PlanningToolDetailSheet(part: part)
        }
        .sheet(item: $selectedAttachment) { item in
            AttachmentPreviewSheet(attachment: item.attachment)
        }
        .sheet(item: $selectedReference) { reference in
            PodcastReferencePreviewSheet(reference: reference)
        }
        .sheet(item: $selectedSourcesDiscussion) { discussion in
            SourcesSheet(discussion: discussion, allowsAddingSources: false)
        }
        .sheet(item: $selectedChapters) { presentation in
            AudioBookChaptersSheet(presentation: presentation)
        }
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
        .sheet(isPresented: $showingSpeakerModels) {
            SpeakerModelsSheet(discussion: speakerModelsDiscussionBinding)
        }
        .alert("Could not update the plan", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .confirmationDialog(
            "Generate this podcast?",
            isPresented: $showingGenerateConfirm,
            titleVisibility: .visible
        ) {
            Button("Generate") { generate() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This turns the current plan into an audio podcast in \(PlanLanguageOption.label(for: selectedLanguage, options: languageOptions)). It can take a few minutes and uses generation credits.")
        }
        .task {
            await purchases.refreshBalance()
            await loadLanguageOptions()
        }
        .onAppear(perform: start)
        .onDisappear {
            streamTask?.cancel()
            initialScrollTask?.cancel()
        }
    }

    // MARK: - Rows

    private var rows: [PlanningRow] {
        var r = visibleParts.map { PlanningRow(id: $0.id, content: .part($0)) }
        if isStreaming {
            r.append(PlanningRow(id: "planning-loading", content: .loading))
        }
        return r
    }

    private var visibleParts: [PlanningPart] {
        guard isStreaming else { return parts }
        var visible = parts
        if let last = visible.last, last.isTransientRunningTool {
            visible.removeLast()
        }
        return visible
    }

    private var animatedRowIDs: [String] {
        rows.map(\.id)
    }

    private var latestPlanPartID: String? {
        visibleParts.last(where: { $0.isPlanCard })?.id
    }

    @ViewBuilder
    private func rowView(_ row: PlanningRow) -> some View {
        switch row.content {
        case .loading:
            loadingBubble
        case let .part(part):
            if part.kind == "text" {
                textBubble(part)
            } else if part.isPlanCard {
                planCard(part)
            } else if part.isQuestionCard {
                questionCard(part)
            } else {
                HStack {
                    PlanningToolCard(part: part) { selectedToolPart = part }
                    Spacer(minLength: 34)
                }
                .planningCardAppear(delay: 0.02)
            }
        }
    }

    @ViewBuilder
    private func textBubble(_ part: PlanningPart) -> some View {
        let text = part.displayText
        let displayText = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let messageAttachments = part.attachments ?? []
        let messageReferences = part.references ?? []
        if part.role == "user" {
            if !displayText.isEmpty || !messageAttachments.isEmpty || !messageReferences.isEmpty {
                HStack {
                    Spacer(minLength: 46)
                    VStack(alignment: .leading, spacing: 8) {
                        if !displayText.isEmpty {
                            Text(text)
                                .font(.body)
                                .foregroundStyle(.white)
                        }
                        if !messageAttachments.isEmpty {
                            userAttachmentChips(messageAttachments)
                        }
                        if !messageReferences.isEmpty {
                            userReferenceChips(messageReferences)
                        }
                    }
                    .padding(.horizontal, 14)
                    .padding(.vertical, 11)
                    .background(Theme.accent, in: .rect(cornerRadius: 20))
                }
                .frame(maxWidth: .infinity, alignment: .trailing)
            }
        } else {
            if !displayText.isEmpty {
                assistantContent(text)
                    .font(.body)
                    .foregroundStyle(.primary)
                    .padding(.vertical, 4)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private func userAttachmentChips(_ attachments: [Attachment]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(attachments.enumerated()), id: \.offset) { _, attachment in
                Button {
                    selectedAttachment = AttachmentPreviewItem(attachment: attachment)
                } label: {
                    HStack(spacing: 7) {
                        Image(systemName: attachment.iconName)
                            .font(.caption.weight(.semibold))
                        Text(attachment.displayName)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)
                        Image(systemName: "chevron.right")
                            .font(.caption2.weight(.bold))
                            .opacity(0.72)
                    }
                    .foregroundStyle(.white)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 6)
                    .background(.white.opacity(0.16), in: .capsule)
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func userReferenceChips(_ references: [PodcastReference]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(references) { reference in
                Button {
                    selectedReference = reference
                } label: {
                    HStack(spacing: 7) {
                        Image(systemName: "waveform.circle.fill")
                            .font(.caption.weight(.semibold))
                        Text(reference.displayTitle)
                            .font(.caption.weight(.semibold))
                            .lineLimit(1)
                        Image(systemName: "chevron.right")
                            .font(.caption2.weight(.bold))
                            .opacity(0.72)
                    }
                    .foregroundStyle(.white)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 6)
                    .background(.white.opacity(0.16), in: .capsule)
                }
                .buttonStyle(.plain)
            }
        }
    }

    @ViewBuilder
    private func assistantContent(_ text: String) -> some View {
        MarkdownText(text)
    }

    @ViewBuilder
    private func planCard(_ part: PlanningPart) -> some View {
        let turn = DiscussionEditTurnDTO(id: nil, role: "plan", text: nil,
                                         script: part.script, sources: part.sources,
                                         markdown: part.markdown, createdAt: nil)
        let snapshot = PlanSnapshot(turn: turn, topic: discussion.topic)
        let showsGenerateButton = part.id == latestPlanPartID
        HStack {
            VStack(spacing: 0) {
                PlanSnapshotCard(label: String(localized: "Plan", comment: "Label for a plan card in the conversation"),
                                 snapshot: snapshot,
                                 onSourcesTapped: { openSources(for: part) },
                                 onChaptersTapped: snapshot.chapters.isEmpty ? nil : { openChapters(snapshot) },
                                 onEditModels: part.script == nil ? nil : { openSpeakerModels(for: part) })
                    .padding(14)

                if showsGenerateButton {
                    Divider().overlay(Theme.secondaryText.opacity(0.18))

                    Button {
                        showingGenerateConfirm = true
                    } label: {
                        HStack {
                            Text("Start generation")
                                .font(.subheadline.weight(.semibold))
                            Spacer(minLength: 0)
                        }
                        .foregroundStyle(canGenerate ? Theme.accent : Theme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 13)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("plan.generate")
                    .disabled(isGenerating || isStreaming || !canGenerate)
                }
            }
            .background(Theme.agentBubble, in: .rect(cornerRadius: 22))
            Spacer(minLength: 34)
        }
        .planningCardAppear(delay: 0.04)
    }

    private func questionCard(_ part: PlanningPart) -> some View {
        let count = part.questions?.count ?? 0
        let firstTitle = part.questions?.first?.title ?? ""
        return HStack {
            Button {
                if part.status == "pending_question", let payload = part.questionPayload() {
                    pendingQuestion = payload
                }
            } label: {
                HStack(spacing: 10) {
                    Image(systemName: "questionmark.circle.fill")
                        .foregroundStyle(questionColor(part.status))
                    VStack(alignment: .leading, spacing: 2) {
                        Text(count == 1
                             ? String(localized: "1 question", comment: "Question card header for a single question")
                             : String(localized: "\(count) questions", comment: "Question card header; value is the question count"))
                            .font(.subheadline.weight(.medium))
                        Text(questionStatusText(part.status, firstTitle: firstTitle))
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(1)
                    }
                    Spacer(minLength: 8)
                    if part.status == "pending_question" {
                        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(Theme.secondaryText)
                    }
                }
                .padding(12)
                .frame(maxWidth: 280, alignment: .leading)
                .background(Theme.agentBubble, in: .rect(cornerRadius: 14))
            }
            .buttonStyle(.plain)
            Spacer(minLength: 34)
        }
    }

    private func questionColor(_ status: String?) -> Color {
        switch status {
        case "pending_question": return Theme.accent
        case "rejected": return .orange
        default: return .green
        }
    }

    private func questionStatusText(_ status: String?, firstTitle: String) -> String {
        switch status {
        case "pending_question": return firstTitle.isEmpty
            ? String(localized: "Tap to answer", comment: "Question card hint when awaiting an answer")
            : firstTitle
        case "rejected": return String(localized: "Skipped", comment: "Question card status when the user skipped")
        default: return String(localized: "Answered", comment: "Question card status when answered")
        }
    }

    private var loadingBubble: some View {
        HStack {
            HStack(spacing: 10) {
                if !streamHasActivity {
                    Text(String(localized: "Thinking…", comment: "Default progress text while the planning agent works"))
                        .font(.callout)
                        .foregroundStyle(Theme.secondaryText)
                        .transition(.opacity.combined(with: .scale(scale: 0.96, anchor: .leading)))
                }
                PlanningTypingDots()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 20))
            .animation(.easeInOut(duration: 0.18), value: streamHasActivity)
            Spacer(minLength: 34)
        }
        .accessibilityLabel(progressText ?? String(localized: "Thinking…", comment: "Default progress text while the planning agent works"))
        .frame(maxWidth: .infinity, alignment: .leading)
        .planningCardAppear()
    }

    // MARK: - History loading skeleton

    private var isShowingHistorySkeleton: Bool {
        isLoadingHistory && parts.isEmpty
    }

    private var historyLoadingSkeleton: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(spacing: 14) {
                    historySkeletonAssistant(widths: [0.72, 0.52, 0.64])
                    historySkeletonToolCard()
                    historySkeletonUser(widths: [0.46, 0.34])
                    historySkeletonAssistant(widths: [0.62, 0.78, 0.42])
                    historySkeletonToolCard(compact: true)
                    historySkeletonAssistant(widths: [0.68, 0.38])
                }
                .padding(.horizontal, 16)
                .padding(.top, 18)
                .padding(.bottom, 18)
            }
            .disabled(true)
            .scrollIndicators(.hidden)

            historySkeletonComposer
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .opacity(historyLoadingPulse ? 0.48 : 0.88)
        .onAppear(perform: startHistoryLoadingAnimation)
        .allowsHitTesting(false)
    }

    private func historySkeletonAssistant(widths: [CGFloat]) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 9) {
                ForEach(Array(widths.enumerated()), id: \.offset) { item in
                    historySkeletonLine(widthFactor: item.element)
                }
            }
            .padding(.vertical, 4)
            Spacer(minLength: 34)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func historySkeletonUser(widths: [CGFloat]) -> some View {
        HStack {
            Spacer(minLength: 46)
            VStack(alignment: .leading, spacing: 9) {
                ForEach(Array(widths.enumerated()), id: \.offset) { item in
                    historySkeletonLine(widthFactor: item.element, tint: Theme.accent.opacity(0.28))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(Theme.accent.opacity(0.14), in: .rect(cornerRadius: 20))
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }

    private func historySkeletonToolCard(compact: Bool = false) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 10) {
                    Circle()
                        .fill(Theme.secondaryText.opacity(0.22))
                        .frame(width: 28, height: 28)
                    VStack(alignment: .leading, spacing: 8) {
                        historySkeletonLine(widthFactor: 0.36)
                        historySkeletonLine(widthFactor: compact ? 0.46 : 0.58, opacity: 0.16)
                    }
                }
                if !compact {
                    RoundedRectangle(cornerRadius: 10)
                        .fill(Theme.secondaryText.opacity(0.12))
                        .frame(height: 92)
                }
            }
            .padding(14)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 18))
            Spacer(minLength: 34)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var historySkeletonComposer: some View {
        VStack(spacing: 0) {
            Divider().opacity(0.18)
            HStack(spacing: 10) {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Theme.secondaryText.opacity(0.18))
                    .frame(height: 16)
                Circle()
                    .fill(Theme.accent.opacity(0.28))
                    .frame(width: 28, height: 28)
            }
            .padding(12)
            .glassEffect(in: .capsule)
            .padding(16)
        }
    }

    private func historySkeletonLine(widthFactor: CGFloat, opacity: Double = 0.22, tint: Color? = nil) -> some View {
        GeometryReader { proxy in
            RoundedRectangle(cornerRadius: 5)
                .fill(tint ?? Theme.secondaryText.opacity(opacity))
                .frame(width: proxy.size.width * widthFactor, height: 12)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(height: 12)
    }

    private func startHistoryLoadingAnimation() {
        guard !historyLoadingPulse else { return }
        withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) {
            historyLoadingPulse = true
        }
    }

    // MARK: - Edit bar

    private var editBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !attachments.isEmpty {
                AttachmentsRow(attachments: $attachments, showsButton: false)
            }
            HStack(spacing: 10) {
                AttachmentsRow(attachments: $attachments, compact: true, showsChips: false)
                TextField("Message the planner", text: $input, axis: .vertical)
                    .lineLimit(1 ... 4)
                    .textFieldStyle(.plain)
                    .focused($inputFocused)
                    .accessibilityIdentifier("plan.input")
                Button(action: send) {
                    Image(systemName: isStreaming ? "ellipsis" : "arrow.up.circle.fill")
                        .font(.title2)
                        .foregroundStyle(Theme.accent)
                }
                .disabled(!canSend)
                .accessibilityIdentifier("plan.send")
            }
            .padding(12)
            .glassEffect(in: .capsule)
        }
        .padding(16)
        .disabled(isGenerating)
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                Picker("Podcast language", selection: $selectedLanguage) {
                    ForEach(PlanLanguageOption.pickerOptions(selected: selectedLanguage, options: languageOptions)) { language in
                        Text(language.label).tag(language.id)
                    }
                }
                .disabled(isGenerating)
            } label: {
                Label("Plan options", systemImage: "ellipsis.circle")
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                showingGenerateConfirm = true
            } label: {
                if isGenerating { ProgressView() } else { Label("Generate", systemImage: "waveform") }
            }
            .labelStyle(.iconOnly)
            .disabled(isGenerating || isStreaming || !canGenerate)
        }
    }

    // MARK: - Derived state

    private var canSend: Bool {
        (!input.trimmingCharacters(in: .whitespaces).isEmpty || !attachments.apiAttachments.isEmpty)
            && !attachments.isUploading
            && !isStreaming && !isGenerating && pendingQuestion == nil
    }

    private var canGenerate: Bool {
        discussion.script != nil || parts.contains { $0.isPlanCard }
    }

    private var speakerModelsDiscussionBinding: Binding<Discussion> {
        Binding(
            get: { discussion },
            set: { updated in
                discussion = updated
                syncVisiblePlanCards(from: updated)
            }
        )
    }

    private func openSpeakerModels(for part: PlanningPart) {
        if discussion.script == nil, let script = part.script {
            discussion.script = script
            discussion.title = script.title
            discussion.markdown = part.markdown
            discussion.sources = part.sources
        }
        showingSpeakerModels = true
    }

    private func openSources(for part: PlanningPart) {
        var sourceDiscussion = discussion
        if let script = part.script {
            sourceDiscussion.script = script
            sourceDiscussion.title = script.title
        }
        sourceDiscussion.markdown = part.markdown
        sourceDiscussion.sources = part.sources
        selectedSourcesDiscussion = sourceDiscussion
    }

    private func openChapters(_ snapshot: PlanSnapshot) {
        selectedChapters = PlanChaptersPresentation(title: snapshot.title, chapters: snapshot.chapters)
    }

    private func syncVisiblePlanCards(from updated: Discussion) {
        guard let script = updated.script else { return }
        for idx in parts.indices where parts[idx].isPlanCard {
            parts[idx].script = script
            parts[idx].sources = updated.sources
            parts[idx].markdown = updated.markdown
        }
    }

    // MARK: - Lifecycle

    private func start() {
        guard !didStart else { return }
        didStart = true
        Task {
            if !didLoadHistory {
                didLoadHistory = true
                isLoadingHistory = true
                defer { isLoadingHistory = false }
                let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id)
                if let view {
                    parts = view.parts
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

    private func requestInitialBottomScrollIfNeeded() {
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
    private func loadLanguageOptions() async {
        guard languageOptions.isEmpty else { return }
        do {
            let form = try await APIClient(tokens: auth).precheck().newDiscussion.form
            languageOptions = form.languageOptions
        } catch {
            languageOptions = []
        }
    }

    // MARK: - Sending / streaming

    private func send() {
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

    private func send(prompt: String, attachments: [Attachment]) {
        errorMessage = nil
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

    private func answer(question: QuestionPayload, answers: [[String: AnyCodable]]) {
        pendingQuestion = nil
        errorMessage = nil
        beginStream {
            APIClient(tokens: auth).answerPlanningQuestion(id: discussion.id, questionId: question.questionId,
                                                           action: "answered", language: selectedLanguage,
                                                           answers: answers)
        }
    }

    private func reject(question: QuestionPayload) {
        pendingQuestion = nil
        errorMessage = nil
        beginStream {
            APIClient(tokens: auth).answerPlanningQuestion(id: discussion.id, questionId: question.questionId,
                                                           action: "rejected", language: selectedLanguage,
                                                           answers: [])
        }
    }

    private func beginStream(_ makeStream: @escaping () -> AsyncThrowingStream<PlanningStreamEvent, Error>) {
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
                    errorMessage = String(localized: "You need \(required) points but have \(balance).",
                                          comment: "Shown when the user lacks enough points; values are point amounts")
                    await purchases.refreshBalance()
                    showingPaywall = true
                    return
                }
                isStreaming = false
                progressText = nil
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
                if let view = try? await APIClient(tokens: auth).planningConversation(id: discussion.id),
                   view.isRunning == true {
                    parts = view.parts
                    errorMessage = nil
                    streamTask = nil
                    beginStream {
                        APIClient(tokens: auth).resumeActivePlanningStream(id: discussion.id)
                    }
                }
            }
        }
    }

    private func handle(_ event: PlanningStreamEvent) {
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
    private func upsertPart(id: String, _ mutate: (PlanningPart?) -> PlanningPart) {
        if let idx = parts.firstIndex(where: { $0.id == id }) {
            parts[idx] = mutate(parts[idx])
        } else {
            parts.append(mutate(nil))
        }
    }

    private func friendlyToolStatus(_ name: String) -> String {
        switch name {
        case "search_sources": return String(localized: "Searching the web…", comment: "Progress while searching sources")
        case "crawl_sources": return String(localized: "Reading links…", comment: "Progress while reading URLs")
        case "write_plan", "update_plan": return String(localized: "Writing the plan…", comment: "Progress while writing the plan")
        case "show_plan": return String(localized: "Showing the plan…", comment: "Progress while showing the plan")
        case "ask_question": return String(localized: "Preparing a question…", comment: "Progress while preparing a question")
        default: return String(localized: "Working…", comment: "Generic progress text")
        }
    }

    private func removeVisiblePlanCards(except id: String) {
        parts.removeAll { $0.id != id && $0.isPlanCard }
    }

    // MARK: - Generate

    private func generate() {
        isGenerating = true
        errorMessage = nil
        Task {
            do {
                discussion = try await APIClient(tokens: auth).generateDiscussion(id: discussion.id, language: selectedLanguage)
                isGenerating = false
                onGenerated(discussion)
            } catch let APIError.insufficientPoints(required, balance) {
                isGenerating = false
                errorMessage = String(localized: "You need \(required) points but have \(balance).",
                                      comment: "Shown when the user lacks enough points; values are point amounts")
                await purchases.refreshBalance()
                showingPaywall = true
            } catch {
                isGenerating = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct PlanningTypingDots: View {
    @State private var isAnimating = false

    var body: some View {
        HStack(spacing: 5) {
            ForEach(0..<3, id: \.self) { index in
                Circle()
                    .fill(Theme.secondaryText)
                    .frame(width: 7, height: 7)
                    .scaleEffect(isAnimating ? 1 : 0.55)
                    .opacity(isAnimating ? 1 : 0.3)
                    .animation(
                        .easeInOut(duration: 0.6)
                            .repeatForever(autoreverses: true)
                            .delay(Double(index) * 0.18),
                        value: isAnimating
                    )
            }
        }
        // No oversized frame: the dots size to their intrinsic 7pt height so the
        // bubble's padding centers them. A taller frame leaves vertical slack the
        // dots ride to the top of, making them spill out of the bubble.
        .onAppear { isAnimating = true }
    }
}

private struct PodcastReferencePreviewSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let reference: PodcastReference
    @State private var discussion: Discussion?
    @State private var errorMessage: String?
    @State private var isLoading = false

    var body: some View {
        NavigationStack {
            Group {
                if let discussion {
                    PodcastPlayerView(discussion: discussion)
                } else if isLoading {
                    ProgressView()
                        .tint(Theme.accent)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Theme.background.ignoresSafeArea())
                } else {
                    ContentUnavailableView(reference.displayTitle,
                                           systemImage: "waveform.circle",
                                           description: Text(errorMessage ?? reference.subtitle))
                        .background(Theme.background.ignoresSafeArea())
                }
            }
            .navigationTitle(reference.displayTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") {
                        dismiss()
                    }
                }
            }
        }
        .task(id: reference.id) {
            await load()
        }
    }

    @MainActor
    private func load() async {
        isLoading = true
        defer { isLoading = false }
        let api = APIClient(tokens: auth)
        do {
            discussion = try await api.discussion(id: reference.id)
            errorMessage = nil
        } catch {
            do {
                discussion = try await api.marketStation(id: reference.id)
                errorMessage = nil
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct PlanningCardAppearModifier: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var isVisible = false
    let delay: Double

    func body(content: Content) -> some View {
        content
            .opacity(isVisible ? 1 : 0)
            .scaleEffect(reduceMotion ? 1 : (isVisible ? 1 : 0.97), anchor: .topLeading)
            .offset(y: reduceMotion ? 0 : (isVisible ? 0 : 10))
            .transition(.asymmetric(
                insertion: .opacity.combined(with: .move(edge: .bottom)),
                removal: .opacity
            ))
            .onAppear {
                guard !isVisible else { return }
                if reduceMotion {
                    isVisible = true
                } else {
                    withAnimation(.spring(response: 0.36, dampingFraction: 0.84).delay(delay)) {
                        isVisible = true
                    }
                }
            }
    }
}

private extension View {
    func planningCardAppear(delay: Double = 0) -> some View {
        modifier(PlanningCardAppearModifier(delay: delay))
    }
}

/// One row in the planning conversation: a persisted/streaming part, or the
/// transient loading accessory shown while the agent works.
private struct PlanningRow: Identifiable, MessageListItem {
    enum Content {
        case part(PlanningPart)
        case loading
    }

    let id: String
    let content: Content

    var isUserMessage: Bool {
        // Planning rows keep user text visually right-aligned in `textBubble`,
        // but should not opt into MessageList's chat turn-pinning spacer. That
        // spacer can leave the finished plan above an empty bottom region.
        return false
    }

    var isMessageListAccessory: Bool {
        switch content {
        case .loading:
            return true
        case .part:
            return false
        }
    }
}
