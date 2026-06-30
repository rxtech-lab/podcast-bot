import SwiftUI
import TipKit

/// Step 2: review/edit the plan. Shows the topic, background, panelists, and
/// researched sources; edits via a chat box ("Edit using chat") that calls
/// /api/plan/improve; and generates the audio podcast.
struct PlanDetailView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @State var discussion: Discussion
    /// When set (a freshly-created placeholder discussion), the plan is streamed
    /// in automatically on first appearance instead of being loaded from history.
    let initialPlan: PlanRequest?
    var onGenerated: (Discussion) -> Void = { _ in }

    @State private var instruction = ""
    @State private var selectedLanguage: String
    @State private var isImproving = false
    @State private var isGenerating = false
    @State private var errorMessage: String?
    @State private var editTurns: [PlanEditTurn] = []
    @State private var attachments: [PendingAttachment] = []
    @State private var languageOptions: [PlanLanguageOption] = []
    @State private var showingSources = false
    @State private var showingSpeakerModels = false
    @State private var selectedChapters: PlanChaptersPresentation?
    @State private var showingGenerateConfirm = false
    @State private var showingPaywall = false
    @State private var showingPointsHistory = false
    @State private var showingPublishSheet = false
    @State private var editIsAtBottom = true
    /// Live progress line shown in the loading bubble while the plan streams in.
    @State private var progressText: String?
    /// Guards the one-time auto-stream of `initialPlan` so re-renders don't restart it.
    @State private var didStartInitialPlan = false
    @State private var didLoadInitialHistory = false
    @State private var isLoadingInitialHistory = false
    @State private var isLoadingOlderHistory = false
    @State private var hasMoreEditHistory = false
    @State private var editHistoryBeforeID: Int64?
    @State private var progressRecoveryTask: Task<Void, Never>?

    private let historyPageSize = 20
    private var recoveredLoadingID: String { "recovered-progress-\(discussion.id)" }

    init(discussion: Discussion, initialPlan: PlanRequest? = nil,
         onGenerated: @escaping (Discussion) -> Void = { _ in })
    {
        _discussion = State(initialValue: discussion)
        _selectedLanguage = State(initialValue: PlanLanguageOption.initialCode(discussion.script?.language ?? discussion.language))
        self.initialPlan = initialPlan
        self.onGenerated = onGenerated
    }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            content
            VStack {
                Spacer()
                editBar
            }
        }
        .navigationTitle(discussion.title.isEmpty ? "Plan" : discussion.title)
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
        .sheet(isPresented: $showingPointsHistory) { PointsHistoryView() }
        .sheet(isPresented: $showingPublishSheet) { PublishStationSheet(discussion: $discussion) }
        .sheet(isPresented: $showingSpeakerModels) { SpeakerModelsSheet(discussion: $discussion) }
        .sheet(item: $selectedChapters) { presentation in
            AudioBookChaptersSheet(presentation: presentation)
        }
        .task {
            await purchases.refreshBalance()
            await loadLanguageOptions()
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    if purchases.isConfigured {
                        Button { showingPointsHistory = true } label: {
                            Label(pointsMenuLabel, systemImage: "sparkles")
                        }
                        Divider()
                    }
                    Picker("\(AppStringLiteral.stationNameRaw) language", selection: $selectedLanguage) {
                        ForEach(PlanLanguageOption.pickerOptions(selected: selectedLanguage, options: languageOptions)) { language in
                            Text(language.label).tag(language.id)
                        }
                    }
                    .disabled(isGenerating)
                    Divider()
                    if discussion.isPublic {
                        Button(role: .destructive) {
                            makePrivate()
                        } label: {
                            Label("Make Private", systemImage: "lock")
                        }
                    } else {
                        Button {
                            showingPublishSheet = true
                        } label: {
                            Label("Publish to Market", systemImage: "globe")
                        }
                    }
                } label: {
                    Label("Plan options", systemImage: "ellipsis.circle")
                }
                .accessibilityLabel("Plan options. \(planOptionsAccessibilityLabel)")
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    showingGenerateConfirm = true
                } label: {
                    if isGenerating {
                        ProgressView()
                    } else {
                        Label("Generate", systemImage: "waveform")
                    }
                }
                .labelStyle(.iconOnly)
                .disabled(isGenerating || isImproving || isEditStreaming || discussion.script == nil)
                .accessibilityLabel("Generate \(AppStringLiteral.stationNameRaw)")
                .popoverTip(PlanGenerateTip(), arrowEdge: .top)
            }
        }
        .confirmationDialog(
            "Generate this \(AppStringLiteral.stationNameRaw)?",
            isPresented: $showingGenerateConfirm,
            titleVisibility: .visible
        ) {
            Button("Generate") { generate() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This turns the current plan into an audio \(AppStringLiteral.stationNameRaw) in \(PlanLanguageOption.label(for: selectedLanguage, options: languageOptions)). It can take a few minutes and uses generation credits.")
        }
        .sheet(isPresented: $showingSources) {
            SourcesSheet(
                discussion: discussion,
                onUpdateStarted: beginSourceUpdate,
                onUpdateProgress: { progressText = $0.text },
                onUpdated: { updated in
                    discussion = updated
                    appendUpdatedPlan()
                },
                onUpdateFailed: appendError
            )
        }
    }

    private var content: some View {
        ZStack {
            MessageList(
                messages: editTurns,
                isStreaming: isEditStreaming,
                isAtBottom: $editIsAtBottom,
                hasMorePrevious: { hasMoreEditHistory },
                loadMorePrevious: loadOlderHistory,
                onLoadError: { _, error in
                    errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
                }
            ) { turn in
                PlanEditBubble(turn: turn, progressText: progressText,
                               onEditModels: { showingSpeakerModels = true },
                               onChaptersTapped: { snapshot in
                                   selectedChapters = PlanChaptersPresentation(title: snapshot.title, chapters: snapshot.chapters)
                               })
                {
                    showingSources = true
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 7)
            }
            .defaultScrollAnchor(.bottom)
            .scrollDismissesKeyboard(.interactively)
            .contentMargins(.bottom, 96, for: .scrollContent)

            if isShowingInitialHistoryLoading {
                initialHistoryLoadingView
                    .transition(.opacity.combined(with: .scale(scale: 0.96)))
                    .allowsHitTesting(false)
            }
        }
        .animation(.easeInOut(duration: 0.18), value: isShowingInitialHistoryLoading)
        .onAppear {
            if discussion.progress?.active == true {
                restoreProgressIfNeeded(from: discussion)
            } else if initialPlan != nil, discussion.script == nil {
                startInitialPlan()
            } else if discussion.editTurns != nil || discussion.script != nil {
                seedInitialTurnIfNeeded()
            }
        }
        .task(id: discussion.id) {
            await loadInitialHistory()
        }
        .onDisappear {
            stopProgressRecoveryPoll()
        }
    }

    private var isShowingInitialHistoryLoading: Bool {
        isLoadingInitialHistory && editTurns.isEmpty
    }

    private var initialHistoryLoadingView: some View {
        VStack(spacing: 12) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.12))
                    .frame(width: 52, height: 52)
                Image(systemName: "bubble.left.and.bubble.right.fill")
                    .font(.system(size: 28, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            VStack(spacing: 4) {
                Text("Loading \(AppStringLiteral.stationNameRaw)...")
                    .font(.headline)
                Text("Fetching latest messages")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            ProgressView()
                .tint(Theme.accent)
                .controlSize(.small)
        }
        .multilineTextAlignment(.center)
        .glassCard(cornerRadius: 20)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Loading \(AppStringLiteral.stationNameRaw)")
    }

    /// Streaming is in effect whenever a loading row is present — the user just
    /// sent an edit, or sources are being applied. The `MessageList` pins the
    /// latest user turn to the top while this is true and releases it (scrolling
    /// to the bottom) when the loading row is replaced by the updated plan.
    private var isEditStreaming: Bool {
        editTurns.contains { $0.role == .loading }
    }

    /// Rebuild the chat from the server-persisted edit history so it survives app
    /// restarts. Each "user" turn becomes a message bubble and each "plan" turn a
    /// plan card from the snapshot stored at that point. Falls back to the current
    /// plan for legacy turns saved before snapshots were stored.
    private func seedInitialTurnIfNeeded() {
        guard editTurns.isEmpty else { return }
        editTurns = rebuiltHistory()
    }

    /// The discussion handed in from the library list doesn't carry edit history
    /// (only GET /api/discussions/{id} loads it). Fetch the full record once and,
    /// as long as the user hasn't started editing, rebuild the chat from it.
    private func loadInitialHistory() async {
        // A freshly-created discussion streams its plan via startInitialPlan(); it
        // has no prior history to load and a late fetch could clobber the result.
        guard initialPlan == nil else { return }
        guard !didLoadInitialHistory else { return }
        didLoadInitialHistory = true
        let shouldShowLoading = editTurns.isEmpty && discussion.progress?.active != true
        if shouldShowLoading {
            isLoadingInitialHistory = true
        }
        defer {
            if shouldShowLoading {
                isLoadingInitialHistory = false
            }
        }
        if discussion.editTurns != nil {
            updateHistoryPaging(from: discussion)
            if discussion.progress?.active == true {
                restoreProgressIfNeeded(from: discussion)
            } else if !isImproving {
                editTurns = rebuiltHistory()
            } else {
                mergeHistoryRows(from: discussion)
            }
            return
        }
        let api = APIClient(tokens: auth)
        guard let full = try? await api.discussion(id: discussion.id, editLimit: historyPageSize) else {
            if discussion.progress?.active == true {
                restoreProgressIfNeeded(from: discussion)
            } else if editTurns.isEmpty {
                seedInitialTurnIfNeeded()
            }
            return
        }
        discussion = full
        updateHistoryPaging(from: full)
        if full.progress?.active == true {
            restoreProgressIfNeeded(from: full)
            return
        }
        if !isImproving {
            editTurns = rebuiltHistory()
        } else {
            mergeHistoryRows(from: full)
        }
    }

    private func loadOlderHistory() async throws {
        guard hasMoreEditHistory, !isLoadingOlderHistory, let beforeID = editHistoryBeforeID else { return }
        isLoadingOlderHistory = true
        defer { isLoadingOlderHistory = false }
        let older = try await APIClient(tokens: auth).discussion(
            id: discussion.id,
            editLimit: historyPageSize,
            editBefore: beforeID
        )
        updateHistoryPaging(from: older)
        let olderTurns = older.editTurns ?? []
        guard !olderTurns.isEmpty else { return }
        let existingDTOs = discussion.editTurns ?? []
        let existingIDs = Set(existingDTOs.compactMap(\.id))
        discussion.editTurns = olderTurns.filter { turn in
            guard let id = turn.id else { return true }
            return !existingIDs.contains(id)
        } + existingDTOs

        let existingRows = Set(editTurns.map(\.id))
        let rowsToPrepend = rows(from: olderTurns).filter { !existingRows.contains($0.id) }
        guard !rowsToPrepend.isEmpty else { return }
        editTurns = rowsToPrepend + editTurns
    }

    private func updateHistoryPaging(from loaded: Discussion) {
        hasMoreEditHistory = loaded.editTurnsHasMore == true
        editHistoryBeforeID = loaded.editTurnsBefore
    }

    private func restoreProgressIfNeeded(from loaded: Discussion) {
        guard let progress = loaded.progress, progress.active else { return }
        isImproving = true
        progressText = progress.text?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
            ? progress.text
            : String(localized: "Updating plan...", comment: "Progress text while restoring an in-flight plan update")
        mergeRecoveredHistory(from: loaded, progress: progress)
        appendRecoveredLoadingRow()
        startProgressRecoveryPoll()
    }

    private func recoveredHistory(from loaded: Discussion, progress: DiscussionProgressDTO) -> [PlanEditTurn] {
        let historyRows = rows(from: loaded.editTurns ?? [])
        if !historyRows.isEmpty {
            return historyRows
        }
        if loaded.script != nil {
            return [.plan(label: PlanEditTurn.currentPlanLabel, snapshot: PlanSnapshot(discussion: loaded))]
        }
        return [.user(recoveredContextText(for: progress, discussion: loaded), id: "recovered-context-\(loaded.id)")]
    }

    private func recoveredContextText(for progress: DiscussionProgressDTO, discussion: Discussion) -> String {
        switch progress.operation {
        case "sources":
            return String(localized: "Adding sources", comment: "Recovered context bubble while sources are being added")
        case "plan":
            let topic = discussion.topic.trimmingCharacters(in: .whitespacesAndNewlines)
            return topic.isEmpty
                ? String(localized: "Creating plan", comment: "Recovered context bubble while a plan is being created and topic is empty")
                : topic
        default:
            return String(localized: "Updating plan", comment: "Recovered context bubble for a generic plan update")
        }
    }

    private func startProgressRecoveryPoll(waitForActiveProgress: Bool = false) {
        progressRecoveryTask?.cancel()
        let discussionID = discussion.id
        let api = APIClient(tokens: auth)
        progressRecoveryTask = Task {
            var observedActiveProgress = !waitForActiveProgress
            for _ in 0 ..< 300 {
                if Task.isCancelled { return }
                try? await Task.sleep(for: .seconds(2))
                guard let full = try? await api.discussion(id: discussionID, editLimit: historyPageSize) else {
                    continue
                }
                discussion = full
                updateHistoryPaging(from: full)
                if let progress = full.progress, progress.active {
                    observedActiveProgress = true
                    progressText = progress.text?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                        ? progress.text
                        : progressText
                    mergeRecoveredHistory(from: full, progress: progress)
                    appendRecoveredLoadingRow()
                    continue
                }
                if waitForActiveProgress && !observedActiveProgress {
                    continue
                }
                progressText = nil
                isImproving = false
                editTurns = rebuiltHistory()
                progressRecoveryTask = nil
                return
            }
        }
    }

    private func stopProgressRecoveryPoll() {
        progressRecoveryTask?.cancel()
        progressRecoveryTask = nil
    }

    /// Builds the chat rows from the server-persisted edit history so the
    /// conversation survives app restarts. Each "user" turn becomes a message
    /// bubble and each "plan" turn a plan card from the snapshot stored at that
    /// point; falls back to the current plan for legacy turns saved before
    /// snapshots existed, and to a single "Current plan" card when there's no
    /// history at all.
    private func rebuiltHistory() -> [PlanEditTurn] {
        var rows = rows(from: discussion.editTurns ?? [])
        if rows.isEmpty {
            rows = [.plan(label: PlanEditTurn.currentPlanLabel, snapshot: PlanSnapshot(discussion: discussion))]
        }
        return rows
    }

    private func rows(from history: [DiscussionEditTurnDTO]) -> [PlanEditTurn] {
        history.compactMap { turn in
            switch turn.role {
            case "user":
                let text = (turn.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                return text.isEmpty ? nil : .user(text, id: turn.planEditTurnID)
            case "plan":
                let snapshot = turn.script != nil
                    ? PlanSnapshot(turn: turn, topic: discussion.topic)
                    : PlanSnapshot(discussion: discussion)
                let label = (turn.text?.isEmpty == false)
                    ? turn.text!
                    : String(localized: "Plan", comment: "Default label for a plan history card with no stored label")
                return .plan(label: label, snapshot: snapshot, id: turn.planEditTurnID)
            default:
                return nil
            }
        }
    }

    private func mergeHistoryRows(from loaded: Discussion) {
        let serverRows = rows(from: loaded.editTurns ?? [])
        guard !serverRows.isEmpty else { return }
        editTurns = mergedRows(serverRows: serverRows, existingRows: editTurns)
    }

    private func mergeRecoveredHistory(from loaded: Discussion, progress: DiscussionProgressDTO) {
        let recoveredRows = recoveredHistory(from: loaded, progress: progress)
        editTurns = mergedRows(serverRows: recoveredRows, existingRows: editTurns)
    }

    private func mergedRows(serverRows: [PlanEditTurn], existingRows: [PlanEditTurn]) -> [PlanEditTurn] {
        guard !serverRows.isEmpty else {
            return existingRows.filter { $0.role != .loading }
        }
        var merged = serverRows
        for row in existingRows {
            guard row.role != .loading, !row.isHistoryBacked, !row.isFallbackCurrentPlan else { continue }
            guard row.role == .user || row.role == .error else { continue }
            guard !merged.contains(where: { $0.representsSameVisibleTurn(as: row) }) else { continue }
            merged.append(row)
        }
        return merged
    }

    private func appendRecoveredLoadingRow() {
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.loading(id: recoveredLoadingID))
    }

    private func appendUpdatedPlan() {
        stopProgressRecoveryPoll()
        progressText = nil
        isImproving = false
        editTurns.removeAll { $0.role == .loading }
        let historyRows = rows(from: discussion.editTurns ?? [])
        if !historyRows.isEmpty {
            editTurns = mergedRows(serverRows: historyRows, existingRows: editTurns)
        } else {
            editTurns.append(.plan(label: String(localized: "Updated plan", comment: "Label for the plan card after an edit or source update completes"), snapshot: PlanSnapshot(discussion: discussion)))
        }
    }

    private func beginSourceUpdate(urls: [String]) {
        errorMessage = nil
        isImproving = true
        progressText = String(localized: "Reading added sources...", comment: "Progress text while newly added sources are being read")
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.user(sourceUpdateText(urls: urls)))
        editTurns.append(.loading())
        startProgressRecoveryPoll(waitForActiveProgress: true)
    }

    private func appendError(_ message: String) {
        stopProgressRecoveryPoll()
        progressText = nil
        isImproving = false
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.error(message))
    }

    private func sourceUpdateText(urls: [String]) -> String {
        let count = urls.count
        let header = count == 1
            ? String(localized: "Added \(count) source:", comment: "User bubble header when one source is added; followed by the URL list")
            : String(localized: "Added \(count) sources:", comment: "User bubble header when multiple sources are added; followed by the URL list")
        return ([header] + urls).joined(separator: "\n")
    }

    private var editBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .padding(.horizontal, 4)
            }
            if !attachments.isEmpty {
                AttachmentsRow(attachments: $attachments, showsButton: false)
            }
            HStack(spacing: 10) {
                AttachmentsRow(attachments: $attachments, compact: true, showsChips: false)
                TextField("Edit using chat", text: $instruction, axis: .vertical)
                    .lineLimit(1 ... 3)
                    .textFieldStyle(.plain)
                Button(action: improve) {
                    Image(systemName: isImproving ? "ellipsis" : "arrow.up.circle.fill")
                        .font(.title2)
                        .foregroundStyle(Theme.accent)
                }
                .disabled(!canSend)
            }
            .padding(12)
            .glassEffect(in: .capsule)
        }
        .padding(16)
        .disabled(isGenerating)
    }

    /// Balance label for the plan options menu, e.g.
    /// "Points (Balance 1,200 Points)".
    private var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Points", comment: "Plan menu label when the points balance is unknown")
        }
        let pointLabel = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Points (Balance \(UsageSummary.formatInt(balance)) \(pointLabel))",
                      comment: "Plan menu points label; first value is the formatted balance, second is the localized unit")
    }

    private var planOptionsAccessibilityLabel: String {
        var parts = [String(localized: "\(AppStringLiteral.stationNameRaw) language: \(PlanLanguageOption.label(for: selectedLanguage, options: languageOptions)).",
                            comment: "Accessibility label stating the selected podcast language")]
        if purchases.isConfigured {
            parts.insert(String(localized: "Remaining points: \(pointsMenuLabel).",
                                comment: "Accessibility label stating the remaining points balance"), at: 0)
        }
        return parts.joined(separator: " ")
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

    /// Allow sending when there's an instruction (and nothing is in flight).
    private var canSend: Bool {
        !instruction.trimmingCharacters(in: .whitespaces).isEmpty
            && !isImproving && !isEditStreaming && !isGenerating && !attachments.isUploading
    }

    /// If `error` is a points shortfall (HTTP 402 from a planning/generation
    /// gate), clear the in-flight state, refresh the balance, and open the
    /// paywall. Returns true when handled so the caller skips its recovery loop
    /// (a 402 means the server never started the work — there's nothing to poll).
    private func handleInsufficientPoints(_ error: Error) -> Bool {
        guard case let APIError.insufficientPoints(required, balance) = error else { return false }
        isImproving = false
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        errorMessage = String(localized: "You need \(UsageSummary.formatInt(required)) points but have \(UsageSummary.formatInt(balance)).",
                              comment: "Shown when the user lacks enough points; values are formatted point amounts")
        Task { await purchases.refreshBalance() }
        showingPaywall = true
        return true
    }

    /// Streams the initial plan into a freshly-created placeholder discussion,
    /// rendering progress in the same loading bubble the edit flow uses. The
    /// discussion row already exists server-side, so an interrupted stream leaves
    /// a recoverable record rather than losing the work.
    private func startInitialPlan() {
        guard !didStartInitialPlan, let request = initialPlan else { return }
        didStartInitialPlan = true
        editTurns = [.loading()]
        isImproving = true
        errorMessage = nil
        progressText = String(localized: "Researching & planning…", comment: "Progress text while the initial plan is being researched and drafted")
        let api = APIClient(tokens: auth)
        Task {
            do {
                for try await event in api.planDiscussionStream(id: discussion.id, request) {
                    switch event {
                    case let .progress(step):
                        progressText = step.text
                    case let .done(updated):
                        discussion = updated
                        appendUpdatedPlan()
                        isImproving = false
                    case let .failed(message):
                        appendError(message)
                        isImproving = false
                    }
                }
                // Stream closed without a terminal event — the server may still
                // have finished and persisted the plan, so try to recover it.
                if isImproving {
                    await recoverInitialPlan(api: api, fallbackError: nil)
                }
            } catch {
                if handleInsufficientPoints(error) { return }
                // A dropped/timed-out connection doesn't mean the server stopped:
                // poll for the persisted plan before surfacing the error.
                await recoverInitialPlan(
                    api: api,
                    fallbackError: (error as? APIError)?.errorDescription ?? error.localizedDescription
                )
            }
        }
    }

    /// Polls the discussion for a persisted plan after the planning stream ended
    /// without a `done` event (idle timeout, dropped connection). The server runs
    /// planning independently of the SSE connection, so the plan usually lands
    /// shortly after — recovering it here means the user never has to leave and
    /// re-enter the page.
    private func recoverInitialPlan(api: APIClient, fallbackError: String?) async {
        progressText = String(localized: "Finishing up…", comment: "Progress text while polling for the persisted plan after the stream ended")
        for _ in 0 ..< 100 {
            if Task.isCancelled { return }
            if let full = try? await api.discussion(id: discussion.id) {
                discussion = full
                if let progress = full.progress, progress.active {
                    progressText = progress.text?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                        ? progress.text
                        : progressText
                }
                if full.script != nil {
                    isImproving = false
                    appendUpdatedPlan()
                    return
                }
            }
            try? await Task.sleep(for: .seconds(3))
        }
        isImproving = false
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        appendError(fallbackError ?? String(localized: "Planning didn’t finish. Pull to refresh or try editing the plan.",
                                            comment: "Fallback error when initial planning never produced a plan"))
    }

    private func improve() {
        let text = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !isGenerating else { return }
        let ready = attachments.apiAttachments
        instruction = ""
        attachments = []
        editTurns.append(.user(text))
        editTurns.append(.loading())
        isImproving = true
        errorMessage = nil
        progressText = nil
        // Baseline to detect server-side completion if the stream drops: the
        // engine bumps updated_at when it persists the revised plan.
        let baselineUpdatedAt = discussion.updatedAt
        let api = APIClient(tokens: auth)
        Task {
            do {
                for try await event in api.improveDiscussionStream(id: discussion.id, instruction: text, attachments: ready) {
                    switch event {
                    case let .progress(step):
                        progressText = step.text
                    case let .done(updated):
                        discussion = updated
                        appendUpdatedPlan()
                        isImproving = false
                    case let .failed(message):
                        appendError(message)
                        isImproving = false
                    }
                }
                // Stream closed without a terminal event — the server may still
                // have persisted the revised plan, so try to recover it.
                if isImproving {
                    await recoverImprovedPlan(api: api, baselineUpdatedAt: baselineUpdatedAt, fallbackError: nil)
                }
            } catch {
                if handleInsufficientPoints(error) { return }
                // A dropped/timed-out connection doesn't stop the server-side
                // revision: poll for the persisted plan before erroring.
                await recoverImprovedPlan(
                    api: api,
                    baselineUpdatedAt: baselineUpdatedAt,
                    fallbackError: (error as? APIError)?.errorDescription ?? error.localizedDescription
                )
            }
        }
    }

    /// Polls the discussion for the revised plan after an edit stream ended
    /// without a `done` event. The engine persists the revision (and bumps
    /// updated_at) independently of the SSE connection, so the new plan usually
    /// lands shortly after — recovering it here avoids forcing a re-enter.
    private func recoverImprovedPlan(api: APIClient, baselineUpdatedAt: String?, fallbackError: String?) async {
        progressText = String(localized: "Finishing up…", comment: "Progress text while polling for the persisted plan after the stream ended")
        for _ in 0 ..< 100 {
            if Task.isCancelled { return }
            if let full = try? await api.discussion(id: discussion.id),
               full.updatedAt != nil, full.updatedAt != baselineUpdatedAt
            {
                discussion = full
                isImproving = false
                appendUpdatedPlan()
                return
            }
            try? await Task.sleep(for: .seconds(3))
        }
        isImproving = false
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        appendError(fallbackError ?? String(localized: "The edit didn’t finish. Pull to refresh or try again.",
                                            comment: "Fallback error when a plan edit never produced a revised plan"))
    }

    private func generate() {
        isGenerating = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                discussion = try await api.generateDiscussion(id: discussion.id, language: selectedLanguage)
                isGenerating = false
                onGenerated(discussion)
            } catch let APIError.insufficientPoints(required, balance) {
                // Not enough points to cover a full podcast — open the paywall.
                isGenerating = false
                errorMessage = String(localized: "You need \(UsageSummary.formatInt(required)) points but have \(UsageSummary.formatInt(balance)).",
                                      comment: "Shown when the user lacks enough points; values are formatted point amounts")
                await purchases.refreshBalance()
                showingPaywall = true
            } catch {
                isGenerating = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func makePrivate() {
        Task { @MainActor in
            do {
                discussion = try await APIClient(tokens: auth).updateDiscussionVisibility(
                    id: discussion.id,
                    visibility: .private
                )
            } catch {
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct PlanEditTurn: Identifiable, MessageListItem {
    enum Role: Equatable {
        case user
        case plan
        case loading
        case error
    }

    let id: String
    let role: Role
    let label: String?
    let text: String?
    let snapshot: PlanSnapshot?

    var isUserMessage: Bool { role == .user }
    /// The loading row is an accessory: it must not count as "content after" the
    /// pinned user turn, so the turn stays pinned to the top until the real
    /// updated plan (or an error) replaces it.
    var isMessageListAccessory: Bool { role == .loading }

    static func user(_ text: String, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .user, label: nil, text: text, snapshot: nil)
    }

    static func plan(label: String, snapshot: PlanSnapshot, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .plan, label: label, text: nil, snapshot: snapshot)
    }

    static func loading(id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .loading, label: nil, text: nil, snapshot: nil)
    }

    static func error(_ message: String, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .error, label: nil, text: message, snapshot: nil)
    }

    var isHistoryBacked: Bool {
        id.hasPrefix("history-")
    }

    /// Displayed label for the fallback "current plan" card. Used both as the
    /// shown text and as the sentinel `isFallbackCurrentPlan` compares against,
    /// so localizing it keeps the comparison correct in every locale.
    static let currentPlanLabel = String(localized: "Current plan",
                                         comment: "Label for the plan card shown when there is no edit history")

    var isFallbackCurrentPlan: Bool {
        role == .plan && !isHistoryBacked && label == PlanEditTurn.currentPlanLabel
    }

    func representsSameVisibleTurn(as other: PlanEditTurn) -> Bool {
        role == other.role
            && normalized(text) == normalized(other.text)
            && normalized(label) == normalized(other.label)
    }

    private func normalized(_ value: String?) -> String {
        (value ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

private extension DiscussionEditTurnDTO {
    var planEditTurnID: String {
        if let id { return "history-\(id)" }
        return "history-\(role)-\(createdAt ?? "")-\(text ?? "")"
    }
}

private struct PlanEditBubble: View {
    let turn: PlanEditTurn
    var progressText: String? = nil
    var onEditModels: () -> Void = {}
    var onChaptersTapped: (PlanSnapshot) -> Void = { _ in }
    var onSourcesTapped: () -> Void

    var body: some View {
        HStack(alignment: .bottom) {
            if turn.role == .user {
                Spacer(minLength: 46)
            }

            content

            if turn.role != .user {
                Spacer(minLength: 34)
            }
        }
        .frame(maxWidth: .infinity, alignment: turn.role == .user ? .trailing : .leading)
    }

    @ViewBuilder
    private var content: some View {
        switch turn.role {
        case .user:
            Text(turn.text ?? "")
                .font(.body)
                .foregroundStyle(.white)
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(Theme.accent, in: .rect(cornerRadius: 20))
        case .plan:
            if let snapshot = turn.snapshot {
                PlanSnapshotCard(label: turn.label ?? "Plan", snapshot: snapshot,
                                 onSourcesTapped: onSourcesTapped,
                                 onChaptersTapped: snapshot.chapters.isEmpty ? nil : { onChaptersTapped(snapshot) },
                                 onEditModels: onEditModels)
                    .padding(14)
                    .background(Theme.agentBubble, in: .rect(cornerRadius: 22))
            }
        case .loading:
            HStack(spacing: 10) {
                ProgressView().tint(Theme.accent)
                Text(progressText ?? "Updating plan...")
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 20))
        case .error:
            Text(turn.text ?? "Could not update the plan.")
                .font(.callout)
                .foregroundStyle(.red)
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .background(Color.red.opacity(0.12), in: .rect(cornerRadius: 20))
        }
    }
}

#if DEBUG
extension PlanSnapshot {
    /// Memberwise initializer for previews/tests (the production type only ships
    /// `init(discussion:)`).
    init(title: String, topic: String, isAudioBook: Bool = false, style: String = "", background: String,
         chapters: [PlanChapterSnapshot] = [],
         people: [PlanPersonSnapshot], sources: [PlanSourceSnapshot])
    {
        self.title = title
        self.topic = topic
        self.isAudioBook = isAudioBook
        self.style = style
        self.background = background
        self.chapters = chapters
        self.people = people
        self.sources = sources
    }
}

/// Offline harness that exercises the pinned-turn behavior of `MessageList`
/// using the real `PlanEditBubble` rows. Tap send and watch the user message
/// pin to the top while a simulated "Updating plan…" reply streams in, then
/// release to the bottom when the updated plan lands.
private struct PlanDetailPinPreview: View {
    private let sampleSnapshot = PlanSnapshot(
        title: "The Future of AI in Education",
        topic: "How will AI reshape classrooms over the next decade?",
        background: "A round-table on personalized tutoring, automated assessment, and how the role of teachers shifts as AI becomes ubiquitous in schools. The panel weighs equity, over-reliance, and what skills still matter when answers are a prompt away.",
        people: [
            PlanPersonSnapshot(name: "Dr. Lena Ortiz", aspect: "Moderator", isHost: true),
            PlanPersonSnapshot(name: "Prof. Adeyemi", aspect: "Pedagogy researcher", isHost: false),
            PlanPersonSnapshot(name: "Maya Chen", aspect: "EdTech founder", isHost: false),
        ],
        sources: [
            PlanSourceSnapshot(title: "OECD: AI and the Future of Skills", urlString: "https://example.com/oecd", snippet: "How AI shifts the skills employers demand."),
            PlanSourceSnapshot(title: "Stanford HAI 2025 Education Brief", urlString: "https://example.com/hai", snippet: "Trends in classroom AI adoption."),
        ]
    )

    @State private var turns: [PlanEditTurn] = []
    @State private var instruction = "Make the intro punchier and add a skeptic to the panel."
    @State private var isAtBottom = true
    @State private var replyTask: Task<Void, Never>?

    private var isStreaming: Bool { turns.contains { $0.role == .loading } }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                MessageList(messages: turns, isStreaming: isStreaming, isAtBottom: $isAtBottom) { turn in
                    PlanEditBubble(turn: turn) {}
                        .padding(.horizontal, 16)
                        .padding(.vertical, 7)
                }
                .contentMargins(.bottom, 80, for: .scrollContent)

                inputBar
            }
        }
        .onAppear {
            if turns.isEmpty {
                turns = [.plan(label: "Current plan", snapshot: sampleSnapshot)]
            }
        }
    }

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField("Edit using chat", text: $instruction, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.plain)
            Button(action: send) {
                Image(systemName: isStreaming ? "ellipsis" : "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.accent)
            }
            .disabled(instruction.trimmingCharacters(in: .whitespaces).isEmpty || isStreaming)
        }
        .padding(12)
        .glassEffect(in: .capsule)
        .padding(16)
    }

    private func send() {
        let text = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        instruction = ""
        turns.append(.user(text))
        turns.append(.loading())
        replyTask?.cancel()
        replyTask = Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            guard !Task.isCancelled else { return }
            turns.removeAll { $0.role == .loading }
            turns.append(.plan(label: "Updated plan", snapshot: sampleSnapshot))
        }
    }
}

#Preview("PlanDetailView · Pin to top") {
    PlanDetailPinPreview()
}
#endif
