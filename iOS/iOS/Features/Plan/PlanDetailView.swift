import SwiftUI
import SwiftData

/// Step 2: review/edit the plan. Shows the topic, background, panelists, and
/// researched sources; edits via a chat box ("Edit using chat") that calls
/// /api/plan/improve; and generates the audio podcast.
struct PlanDetailView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.modelContext) private var context
    @Bindable var discussion: Discussion

    @State private var instruction = ""
    @State private var isImproving = false
    @State private var isGenerating = false
    @State private var errorMessage: String?
    @State private var generated: Discussion?
    @State private var editTurns: [PlanEditTurn] = []

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
        .navigationDestination(item: $generated) { d in
            PodcastPlayerView(discussion: d)
        }
    }

    private var content: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
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

    private func appendUpdatedPlan(_ response: PlanResponse) {
        editTurns.removeAll { $0.role == .loading }
        editTurns.append(.plan(label: "Updated plan", snapshot: PlanSnapshot(discussion: discussion, response: response)))
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
        let prev = Mapping.scriptDTO(from: discussion)
        Task {
            do {
                let resp = try await api.improve(PlanImproveRequest(previousScript: prev, instruction: text))
                Mapping.apply(resp, to: discussion, in: context)
                try? context.save()
                appendUpdatedPlan(resp)
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
        let script = Mapping.scriptDTO(from: discussion)
        Task {
            do {
                let resp = try await api.submitJob(JobSubmitRequest(script: script))
                discussion.jobID = resp.id
                discussion.status = .generating
                discussion.updatedAt = Date()
                try? context.save()
                isGenerating = false
                generated = discussion
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

private struct PlanSnapshot {
    let title: String
    let topic: String
    let background: String
    let people: [PlanPersonSnapshot]
    let sources: [PlanSourceSnapshot]

    init(discussion: Discussion) {
        title = discussion.title
        topic = discussion.topic
        background = discussion.background
        people = discussion.sortedPeople.map(PlanPersonSnapshot.init)
        sources = discussion.sortedSources.map(PlanSourceSnapshot.init)
    }

    init(discussion: Discussion, response: PlanResponse) {
        let script = response.script
        title = script.title
        topic = discussion.topic
        background = script.background ?? discussion.background

        var nextPeople: [PlanPersonSnapshot] = []
        if let host = script.host, !host.name.isEmpty {
            nextPeople.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        nextPeople.append(contentsOf: (script.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        people = nextPeople

        let dtoSources = response.sources ?? script.sources ?? []
        sources = dtoSources.map {
            PlanSourceSnapshot(title: $0.title, urlString: $0.url, snippet: $0.snippet ?? "")
        }
    }
}

private struct PlanPersonSnapshot: Identifiable {
    let id = UUID()
    let name: String
    let aspect: String
    let isHost: Bool

    init(_ person: Person) {
        name = person.name
        aspect = person.aspect
        isHost = person.isHost
    }

    init(name: String, aspect: String, isHost: Bool) {
        self.name = name
        self.aspect = aspect
        self.isHost = isHost
    }
}

private struct PlanSourceSnapshot: Identifiable {
    let id = UUID()
    let title: String
    let urlString: String
    let snippet: String

    init(_ source: SourceRef) {
        title = source.title
        urlString = source.urlString
        snippet = source.snippet
    }

    init(title: String, urlString: String, snippet: String) {
        self.title = title
        self.urlString = urlString
        self.snippet = snippet
    }

    var url: URL? { URL(string: urlString) }
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

private struct PlanSnapshotCard: View {
    let label: String
    let snapshot: PlanSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 6) {
                Text(label.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                if !snapshot.title.isEmpty {
                    Text(snapshot.title)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(.white)
                }
                if !snapshot.topic.isEmpty {
                    Text("Topic: \(snapshot.topic)")
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }

            if !snapshot.background.isEmpty {
                MarkdownText(snapshot.background)
                    .font(.body)
                    .foregroundStyle(Theme.secondaryText)
            }

            if !snapshot.people.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    Text("Panelists").font(.headline)
                    ForEach(snapshot.people) { person in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 8) {
                                Image(systemName: person.isHost ? "person.wave.2.fill" : "person.fill")
                                    .foregroundStyle(Theme.accent)
                                    .frame(width: 20)
                                Text(person.name)
                                    .font(.body.weight(.semibold))
                            }
                            if !person.aspect.isEmpty {
                                Text(person.aspect)
                                    .font(.subheadline)
                                    .foregroundStyle(Theme.secondaryText)
                                    .padding(.leading, 28)
                            }
                        }
                    }
                }
            }

            if !snapshot.sources.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Sources").font(.headline)
                    ForEach(snapshot.sources) { source in
                        sourceRow(source)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func sourceRow(_ source: PlanSourceSnapshot) -> some View {
        if let url = source.url {
            Link(destination: url) {
                sourceContent(source)
            }
        } else {
            sourceContent(source)
        }
    }

    private func sourceContent(_ source: PlanSourceSnapshot) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(source.title.isEmpty ? source.urlString : source.title)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.white)
            if !source.snippet.isEmpty {
                Text(source.snippet)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(3)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(Color.white.opacity(0.05), in: .rect(cornerRadius: 14))
    }
}
