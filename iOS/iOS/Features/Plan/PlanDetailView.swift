import SwiftUI

/// Step 2: review/edit the plan. Shows the topic, background, panelists, and
/// researched sources; edits via a chat box ("Edit using chat") that calls
/// /api/plan/improve; and generates the audio podcast.
struct PlanDetailView: View {
    @Environment(AuthManager.self) private var auth
    @State var discussion: Discussion
    var onGenerated: (Discussion) -> Void = { _ in }

    @State private var instruction = ""
    @State private var selectedLanguage: String
    @State private var isImproving = false
    @State private var isGenerating = false
    @State private var errorMessage: String?
    @State private var editTurns: [PlanEditTurn] = []

    init(discussion: Discussion, onGenerated: @escaping (Discussion) -> Void = { _ in }) {
        _discussion = State(initialValue: discussion)
        _selectedLanguage = State(initialValue: DiscussionLanguage.normalized(discussion.script?.language ?? discussion.language))
        self.onGenerated = onGenerated
    }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                content
                editBar
            }
        }
        .navigationTitle(discussion.title.isEmpty ? "Plan" : discussion.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: generate) {
                    if isGenerating { ProgressView() } else { Text("Generate") }
                }
                .disabled(isGenerating)
            }
        }
    }

    private var content: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    DiscussionLanguageMenu(selection: $selectedLanguage)

                    ForEach(editTurns) { turn in
                        PlanEditBubble(turn: turn)
                            .id(turn.id)
                    }

                    if let errorMessage {
                        Text(errorMessage).font(.footnote).foregroundStyle(.red)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(16)
            }
            .scrollDismissesKeyboard(.interactively)
            .onAppear {
                seedInitialTurnIfNeeded()
                scrollToLatest(proxy)
            }
            .onChange(of: editTurns.count) { _, _ in
                scrollToLatest(proxy)
            }
        }
    }

    private func scrollToLatest(_ proxy: ScrollViewProxy) {
        guard let last = editTurns.last else { return }
        Task { @MainActor in
            withAnimation(.snappy) {
                proxy.scrollTo(last.id, anchor: .bottom)
            }
        }
    }

    private func seedInitialTurnIfNeeded() {
        guard editTurns.isEmpty else { return }
        editTurns = [.plan(label: "Current plan", snapshot: PlanSnapshot(discussion: discussion))]
    }

    private func appendUpdatedPlan() {
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.plan(label: "Updated plan", snapshot: PlanSnapshot(discussion: discussion)))
    }

    private func appendError(_ message: String) {
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.error(message))
    }

    private var editBar: some View {
        HStack(spacing: 10) {
            TextField("Edit using chat", text: $instruction, axis: .vertical)
                .lineLimit(1...3)
                .textFieldStyle(.plain)
            Button(action: improve) {
                Image(systemName: isImproving ? "ellipsis" : "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.accent)
            }
            .disabled(instruction.trimmingCharacters(in: .whitespaces).isEmpty || isImproving)
        }
        .padding(12)
        .glassEffect(in: .capsule)
        .padding(16)
    }

    private func improve() {
        let text = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        instruction = ""
        editTurns.append(.user(text))
        editTurns.append(.loading)
        isImproving = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                discussion = try await api.improveDiscussion(id: discussion.id, instruction: text)
                appendUpdatedPlan()
                isImproving = false
            } catch {
                isImproving = false
                appendError((error as? APIError)?.errorDescription ?? error.localizedDescription)
            }
        }
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

private struct PlanEditTurn: Identifiable {
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
                PlanSnapshotCard(label: turn.label ?? "Plan", snapshot: snapshot)
                    .padding(14)
                    .background(Theme.agentBubble, in: .rect(cornerRadius: 22))
            }
        case .loading:
            HStack(spacing: 10) {
                ProgressView().tint(Theme.accent)
                Text("Updating plan...")
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
