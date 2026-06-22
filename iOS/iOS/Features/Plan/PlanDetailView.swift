import SwiftUI

/// Step 2: review/edit the plan. Shows the topic, background, panelists, and
/// researched sources; edits via a chat box ("Edit using chat") that calls
/// /api/plan/improve; and generates the audio podcast.
struct PlanDetailView: View {
    @Environment(AuthManager.self) private var auth
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
    @State private var showingSources = false
    @State private var showingGenerateConfirm = false
    @State private var editIsAtBottom = true
    /// Live progress line shown in the loading bubble while the plan streams in.
    @State private var progressText: String?
    /// Guards the one-time auto-stream of `initialPlan` so re-renders don't restart it.
    @State private var didStartInitialPlan = false

    init(discussion: Discussion, initialPlan: PlanRequest? = nil,
         onGenerated: @escaping (Discussion) -> Void = { _ in }) {
        _discussion = State(initialValue: discussion)
        _selectedLanguage = State(initialValue: DiscussionLanguage.normalized(discussion.script?.language ?? discussion.language))
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
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Picker("Podcast language", selection: $selectedLanguage) {
                        ForEach(DiscussionLanguage.supported) { language in
                            Text(language.label).tag(language.code)
                        }
                    }
                } label: {
                    Label("Podcast language", systemImage: "globe")
                }
                .disabled(isGenerating)
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
                .disabled(isGenerating || isImproving || discussion.script == nil)
                .accessibilityLabel("Generate podcast")
            }
        }
        .confirmationDialog(
            "Generate this podcast?",
            isPresented: $showingGenerateConfirm,
            titleVisibility: .visible
        ) {
            Button("Generate") { generate() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This turns the current plan into an audio podcast in \(DiscussionLanguage.label(for: selectedLanguage)). It can take a few minutes and uses generation credits.")
        }
        .sheet(isPresented: $showingSources) {
            SourcesSheet(
                discussion: discussion,
                onUpdateStarted: beginSourceUpdate,
                onUpdated: { updated in
                    discussion = updated
                    appendUpdatedPlan()
                },
                onUpdateFailed: appendError
            )
        }
    }

    private var content: some View {
        MessageList(
            messages: editTurns,
            isStreaming: isEditStreaming,
            isAtBottom: $editIsAtBottom
        ) { turn in
            PlanEditBubble(turn: turn, progressText: progressText) {
                showingSources = true
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 7)
        }
        .scrollDismissesKeyboard(.interactively)
        .contentMargins(.bottom, 96, for: .scrollContent)
        .onAppear {
            if initialPlan != nil, discussion.script == nil {
                startInitialPlan()
            } else {
                seedInitialTurnIfNeeded()
            }
        }
        .task {
            await loadFullHistory()
        }
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
    private func loadFullHistory() async {
        // A freshly-created discussion streams its plan via startInitialPlan(); it
        // has no prior history to load and a late fetch could clobber the result.
        guard initialPlan == nil else { return }
        guard discussion.editTurns == nil else { return }
        let api = APIClient(tokens: auth)
        guard let full = try? await api.discussion(id: discussion.id) else { return }
        let canRebuild = !isImproving && editTurns.allSatisfy { $0.role == .plan }
        discussion = full
        if canRebuild {
            editTurns = rebuiltHistory()
        }
    }

    /// Builds the chat rows from the server-persisted edit history so the
    /// conversation survives app restarts. Each "user" turn becomes a message
    /// bubble and each "plan" turn a plan card from the snapshot stored at that
    /// point; falls back to the current plan for legacy turns saved before
    /// snapshots existed, and to a single "Current plan" card when there's no
    /// history at all.
    private func rebuiltHistory() -> [PlanEditTurn] {
        var rows: [PlanEditTurn] = []
        if let history = discussion.editTurns, !history.isEmpty {
            rows = history.compactMap { turn in
                switch turn.role {
                case "user":
                    let text = (turn.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                    return text.isEmpty ? nil : .user(text)
                case "plan":
                    let snapshot = turn.script != nil
                        ? PlanSnapshot(turn: turn, topic: discussion.topic)
                        : PlanSnapshot(discussion: discussion)
                    let label = (turn.text?.isEmpty == false) ? turn.text! : "Plan"
                    return .plan(label: label, snapshot: snapshot)
                default:
                    return nil
                }
            }
        }
        if rows.isEmpty {
            rows = [.plan(label: "Current plan", snapshot: PlanSnapshot(discussion: discussion))]
        }
        return rows
    }

    private func appendUpdatedPlan() {
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.plan(label: "Updated plan", snapshot: PlanSnapshot(discussion: discussion)))
    }

    private func beginSourceUpdate() {
        errorMessage = nil
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.loading)
    }

    private func appendError(_ message: String) {
        progressText = nil
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.error(message))
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

    /// Allow sending when there's an instruction (and nothing is in flight).
    private var canSend: Bool {
        !instruction.trimmingCharacters(in: .whitespaces).isEmpty
            && !isImproving && !isGenerating && !attachments.isUploading
    }

    /// Streams the initial plan into a freshly-created placeholder discussion,
    /// rendering progress in the same loading bubble the edit flow uses. The
    /// discussion row already exists server-side, so an interrupted stream leaves
    /// a recoverable record rather than losing the work.
    private func startInitialPlan() {
        guard !didStartInitialPlan, let request = initialPlan else { return }
        didStartInitialPlan = true
        editTurns = [.loading]
        isImproving = true
        errorMessage = nil
        progressText = "Researching & planning…"
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
        progressText = "Finishing up…"
        for _ in 0 ..< 100 {
            if Task.isCancelled { return }
            if let full = try? await api.discussion(id: discussion.id), full.script != nil {
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
        appendError(fallbackError ?? "Planning didn’t finish. Pull to refresh or try editing the plan.")
    }

    private func improve() {
        let text = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !isGenerating else { return }
        let ready = attachments.apiAttachments
        instruction = ""
        attachments = []
        editTurns.append(.user(text))
        editTurns.append(.loading)
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
        progressText = "Finishing up…"
        for _ in 0 ..< 100 {
            if Task.isCancelled { return }
            if let full = try? await api.discussion(id: discussion.id),
               full.updatedAt != nil, full.updatedAt != baselineUpdatedAt {
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
        appendError(fallbackError ?? "The edit didn’t finish. Pull to refresh or try again.")
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
            } catch {
                isGenerating = false
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

    let id = UUID()
    let role: Role
    let label: String?
    let text: String?
    let snapshot: PlanSnapshot?

    var isUserMessage: Bool { role == .user }
    /// The loading row is an accessory: it must not count as "content after" the
    /// pinned user turn, so the turn stays pinned to the top until the real
    /// updated plan (or an error) replaces it.
    var isMessageListAccessory: Bool { role == .loading }

    static func user(_ text: String) -> PlanEditTurn {
        PlanEditTurn(role: .user, label: nil, text: text, snapshot: nil)
    }

    static func plan(label: String, snapshot: PlanSnapshot) -> PlanEditTurn {
        PlanEditTurn(role: .plan, label: label, text: nil, snapshot: snapshot)
    }

    static var loading: PlanEditTurn {
        PlanEditTurn(role: .loading, label: nil, text: nil, snapshot: nil)
    }

    static func error(_ message: String) -> PlanEditTurn {
        PlanEditTurn(role: .error, label: nil, text: message, snapshot: nil)
    }
}

private struct PlanEditBubble: View {
    let turn: PlanEditTurn
    var progressText: String? = nil
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
                PlanSnapshotCard(label: turn.label ?? "Plan", snapshot: snapshot, onSourcesTapped: onSourcesTapped)
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
    init(title: String, topic: String, background: String,
         people: [PlanPersonSnapshot], sources: [PlanSourceSnapshot]) {
        self.title = title
        self.topic = topic
        self.background = background
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
        turns.append(.loading)
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
