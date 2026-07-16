import SwiftUI

/// Conversational planning screen: the agent gathers context, asks the user
/// questions (bottom sheet), and writes/refines the plan over an SSE stream. Each
/// tool call shows as an inline card; `show_plan` renders the current plan card.
struct PlanConversationView: View {
    @Environment(AuthManager.self) var auth
    @Environment(PurchaseManager.self) var purchases
    @State var discussion: Discussion
    var onGenerated: (Discussion) -> Void = { _ in }

    @State var parts: [PlanningPart] = []
    @State var input = ""
    @State var isStreaming = false
    @State var progressText: String?
    @State var errorMessage: String?
    @State var showingErrorAlert = false
    @State var pendingQuestion: QuestionPayload?
    @State var selectedToolPart: PlanningPart?
    @State var selectedAttachment: AttachmentPreviewItem?
    @State var selectedReference: PodcastReference?
    @State var selectedSourcesDiscussion: Discussion?
    @State var selectedChapters: PlanChaptersPresentation?
    @State var selectedTranscript: UploadedAudioTranscriptPresentation?
    @State var isGenerating = false
    @State var showingGenerateConfirm = false
    @State var showingChapterChecklist = false
    @State var showingPaywall = false
    @State var errorOffersTopUp = false
    @State var showingSpeakerModels = false
    @State var didStart = false
    @State var didLoadHistory = false
    @State var isLoadingHistory = false
    @State var historyLoadingPulse = false
    @State var editIsAtBottom = true
    @State var attachments: [PendingAttachment] = []
    @State var shouldScrollToInitialBottom = false
    @State var didRequestInitialBottomScroll = false
    @State var selectedLanguage: String
    /// The last language the plan itself carried; used to tell a plan-driven
    /// language change (agent detected the audio's language) apart from a
    /// manual picker choice, which must never be overridden.
    @State var planLanguageCode: String
    @State var languageOptions: [PlanLanguageOption] = []
    @State var streamTask: Task<Void, Never>?
    @State var initialScrollTask: Task<Void, Never>?
    @State var isTranscribing = false
    @State var transcribePollTask: Task<Void, Never>?
    @State var streamHasActivity = false
    @FocusState var inputFocused: Bool

    init(discussion: Discussion,
         onGenerated: @escaping (Discussion) -> Void = { _ in })
    {
        _discussion = State(initialValue: discussion)
        let planLanguage = PlanLanguageOption.initialCode(discussion.script?.language ?? discussion.language)
        _selectedLanguage = State(initialValue: planLanguage)
        _planLanguageCode = State(initialValue: planLanguage)
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
        .sheet(item: $selectedTranscript) { presentation in
            UploadedAudioTranscriptSheet(
                discussionID: discussion.id,
                presentation: presentation
            ) { updated in
                discussion = updated
                syncVisiblePlanCards(from: updated)
            }
        }
        .sheet(isPresented: $showingChapterChecklist) {
            if let script = latestAudioBookScript {
                ChapterChecklistSheet(mode: .plan(script),
                                      editableDiscussion: speakerModelsDiscussionBinding)
                { indices in
                    showingChapterChecklist = false
                    generate(chapters: indices)
                }
            }
        }
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
        .sheet(isPresented: $showingSpeakerModels) {
            SpeakerModelsSheet(discussion: speakerModelsDiscussionBinding)
        }
        .alert("Could not update the plan", isPresented: errorAlertBinding) {
            if errorOffersTopUp {
                Button("Top Up") {
                    clearPlanningError()
                    showingPaywall = true
                }
            }
            Button("OK", role: .cancel) { clearPlanningError() }
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
        .onChange(of: PlanLanguageOption.initialCode(discussion.script?.language ?? discussion.language)) { _, newCode in
            // Follow plan-driven language changes (e.g. the agent detecting an
            // uploaded audio's spoken language) while the picker still shows
            // the previous plan language; a manual selection stays put.
            if selectedLanguage == planLanguageCode {
                selectedLanguage = newCode
            }
            planLanguageCode = newCode
        }
        .onChange(of: selectedLanguage) { _, newCode in
            persistLanguageSelection(newCode)
        }
        .onAppear(perform: start)
        .onDisappear {
            streamTask?.cancel()
            initialScrollTask?.cancel()
            transcribePollTask?.cancel()
            showingPaywall = false
        }
    }

    /// Persists a manual language pick to the discussion right away, so the
    /// choice sticks even if the user leaves without generating. Plan-driven
    /// syncs (selectedLanguage set back to the plan's own language) are no-ops
    /// against planLanguageCode and are skipped.
    func persistLanguageSelection(_ code: String) {
        guard code != planLanguageCode, discussion.script != nil else { return }
        Task {
            do {
                let updated = try await APIClient(tokens: auth).updateDiscussionLanguage(id: discussion.id, language: code)
                discussion = updated
            } catch {
                // Revert the picker so it never shows a language the plan
                // doesn't actually have.
                selectedLanguage = planLanguageCode
            }
        }
    }

    // MARK: - Rows

    var rows: [PlanningRow] {
        var r = visibleParts.map { PlanningRow(id: $0.id, content: .part($0)) }
        if isStreaming {
            r.append(PlanningRow(id: "planning-loading", content: .loading))
        } else if isTranscribing {
            let text = discussion.progress?.text
                ?? String(localized: "Transcribing audio…", comment: "Progress shown while an uploaded audio file is being transcribed")
            r.append(PlanningRow(id: "transcribing", content: .transcribing(text)))
        }
        return r
    }

    var visibleParts: [PlanningPart] {
        guard isStreaming else { return parts }
        var visible = parts
        if let last = visible.last, last.isTransientRunningTool {
            visible.removeLast()
        }
        return visible
    }

    var animatedRowIDs: [String] {
        rows.map(\.id)
    }

    var latestPlanPartID: String? {
        visibleParts.last(where: { $0.isPlanCard })?.id
    }

    @ViewBuilder
    func rowView(_ row: PlanningRow) -> some View {
        switch row.content {
        case .loading:
            loadingBubble
        case let .transcribing(text):
            transcribingBubble(text)
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
    func textBubble(_ part: PlanningPart) -> some View {
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

    func userAttachmentChips(_ attachments: [Attachment]) -> some View {
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

    func userReferenceChips(_ references: [PodcastReference]) -> some View {
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
    func assistantContent(_ text: String) -> some View {
        MarkdownText(text)
    }

    @ViewBuilder
    func planCard(_ part: PlanningPart) -> some View {
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
                        requestGenerate()
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

    func questionCard(_ part: PlanningPart) -> some View {
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

    func questionColor(_ status: String?) -> Color {
        switch status {
        case "pending_question": return Theme.accent
        case "rejected": return .orange
        default: return .green
        }
    }

    func questionStatusText(_ status: String?, firstTitle: String) -> String {
        switch status {
        case "pending_question": return firstTitle.isEmpty
            ? String(localized: "Tap to answer", comment: "Question card hint when awaiting an answer")
            : firstTitle
        case "rejected": return String(localized: "Skipped", comment: "Question card status when the user skipped")
        default: return String(localized: "Answered", comment: "Question card status when answered")
        }
    }

    func transcribingBubble(_ text: String) -> some View {
        HStack {
            HStack(spacing: 10) {
                Image(systemName: "waveform")
                    .foregroundStyle(Theme.accent)
                    .symbolEffect(.variableColor.iterative, options: .repeating)
                Text(text)
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
                PlanningTypingDots()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 20))
            Spacer(minLength: 34)
        }
        .accessibilityLabel(text)
        .frame(maxWidth: .infinity, alignment: .leading)
        .planningCardAppear()
    }

    var loadingBubble: some View {
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

    var isShowingHistorySkeleton: Bool {
        isLoadingHistory && parts.isEmpty
    }

    var historyLoadingSkeleton: some View {
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

    func historySkeletonAssistant(widths: [CGFloat]) -> some View {
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

    func historySkeletonUser(widths: [CGFloat]) -> some View {
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

    func historySkeletonToolCard(compact: Bool = false) -> some View {
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

    var historySkeletonComposer: some View {
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

    func historySkeletonLine(widthFactor: CGFloat, opacity: Double = 0.22, tint: Color? = nil) -> some View {
        GeometryReader { proxy in
            RoundedRectangle(cornerRadius: 5)
                .fill(tint ?? Theme.secondaryText.opacity(opacity))
                .frame(width: proxy.size.width * widthFactor, height: 12)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(height: 12)
    }

    func startHistoryLoadingAnimation() {
        guard !historyLoadingPulse else { return }
        withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) {
            historyLoadingPulse = true
        }
    }

    // MARK: - Edit bar

    var editBar: some View {
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

    var errorAlertBinding: Binding<Bool> {
        Binding(
            get: { showingErrorAlert },
            set: {
                showingErrorAlert = $0
                if !$0 {
                    clearPlanningError()
                }
            }
        )
    }

    @ToolbarContentBuilder
    var toolbarContent: some ToolbarContent {
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
                requestGenerate()
            } label: {
                if isGenerating { ProgressView() } else { Label("Generate", systemImage: "waveform") }
            }
            .labelStyle(.iconOnly)
            .disabled(isGenerating || isStreaming || !canGenerate)
        }
    }

    /// Multi-chapter audiobooks pick a batch of chapters (server caps a run at
    /// 5) in a checklist; everything else confirms via the plain dialog.
    func requestGenerate() {
        if let script = latestAudioBookScript, (script.audioBookChapters?.count ?? 0) > 1 {
            if discussion.script == nil {
                discussion.script = script
                discussion.title = script.title
            }
            showingChapterChecklist = true
        } else {
            showingGenerateConfirm = true
        }
    }

    /// The most recent audiobook plan shown in the conversation (falls back to
    /// the persisted discussion script); nil for non-audiobook plans.
    var latestAudioBookScript: ScriptDTO? {
        let script = visibleParts.last(where: { $0.isPlanCard })?.script ?? discussion.script
        guard let script, script.type == "audio-book" else { return nil }
        return script
    }

}
