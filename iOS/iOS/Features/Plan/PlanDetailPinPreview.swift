import SwiftUI
import TipKit

#if DEBUG
struct PlanDetailPinPreview: View {
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

