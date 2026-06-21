import SwiftData
import SwiftUI

/// Step 1 of planning: enter a topic + panelist count, then ask the engine to
/// draft a plan (title, background, people, researched sources). On success the
/// plan is persisted and pushed into the editor.
struct NewDiscussionView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.modelContext) private var context
    @Environment(\.dismiss) private var dismiss
    var onPlanned: (Discussion) -> Void = { _ in }

    @State private var topic = ""
    @State private var discussants = 3
    @State private var language = "en-US"
    @State private var isPlanning = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                form
            }
            .navigationTitle("New Discussion")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }

                ToolbarItem(placement: .confirmationAction) {
                    Button(action: plan) {
                        if isPlanning {
                            ProgressView()
                        } else {
                            Text("Plan")
                        }
                    }
                    .disabled(topic.trimmingCharacters(in: .whitespaces).isEmpty || isPlanning)
                }
            }
        }
        .interactiveDismissDisabled(true)
    }

    private var form: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Topic").font(.headline)
                    TextField("e.g. The future of AI in education", text: $topic, axis: .vertical)
                        .lineLimit(10...15)
                        .textFieldStyle(.plain)
                        .padding(12)
                        .glassEffect(in: .rect(cornerRadius: 16))
                }

                Stepper("Panelists: \(discussants)", value: $discussants, in: 2...6)
                    .padding(12)
                    .glassEffect(in: .rect(cornerRadius: 16))

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isPlanning {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Researching & planning…")
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
    }

    private func plan() {
        let trimmed = topic.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        isPlanning = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                let resp = try await api.plan(PlanRequest(topic: trimmed, language: language,
                                                          discussants: discussants, research: true))
                let discussion = Discussion(topic: trimmed, language: language)
                context.insert(discussion)
                Mapping.apply(resp, to: discussion, in: context)
                try? context.save()
                isPlanning = false
                dismiss()
                onPlanned(discussion)
            } catch {
                isPlanning = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
