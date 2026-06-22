import SwiftUI

/// Step 1 of planning: enter a topic + panelist count, then ask the engine to
/// draft a plan (title, background, people, researched sources). On success the
/// plan is persisted and pushed into the editor.
struct NewDiscussionView: View {
    @Environment(AuthManager.self) private var auth
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
                        .disabled(isPlanning)
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

                DiscussionLanguageMenu(selection: $language)

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
        .scrollDismissesKeyboard(.interactively)
        .disabled(isPlanning)
    }

    private func plan() {
        let trimmed = topic.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        isPlanning = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                let discussion = try await api.planDiscussion(PlanRequest(topic: trimmed, language: language,
                                                                          discussants: discussants, research: true))
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

struct DiscussionLanguage: Identifiable, Hashable {
    let code: String
    let label: String

    var id: String { code }

    static let supported: [DiscussionLanguage] = [
        DiscussionLanguage(code: "en-US", label: "English"),
        DiscussionLanguage(code: "zh-CN", label: "Chinese (Simplified)"),
        DiscussionLanguage(code: "zh-TW", label: "Chinese (Traditional)"),
        DiscussionLanguage(code: "ja-JP", label: "Japanese"),
        DiscussionLanguage(code: "ko-KR", label: "Korean"),
        DiscussionLanguage(code: "es-ES", label: "Spanish"),
        DiscussionLanguage(code: "fr-FR", label: "French"),
        DiscussionLanguage(code: "de-DE", label: "German")
    ]

    static func normalized(_ code: String) -> String {
        supported.first(where: { $0.code == code })?.code ?? "en-US"
    }

    static func label(for code: String) -> String {
        supported.first(where: { $0.code == code })?.label ?? code
    }
}

struct DiscussionLanguageMenu: View {
    @Binding var selection: String
    var title = "Podcast language"

    var body: some View {
        Menu {
            Picker(title, selection: $selection) {
                ForEach(DiscussionLanguage.supported) { language in
                    Text(language.label).tag(language.code)
                }
            }
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "globe")
                    .foregroundStyle(Theme.accent)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.headline)
                        .foregroundStyle(.white)
                    Text(DiscussionLanguage.label(for: selection))
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
                Spacer()
                Image(systemName: "chevron.up.chevron.down")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(12)
            .glassEffect(in: .rect(cornerRadius: 16))
        }
        .tint(Theme.accent)
        .onAppear {
            selection = DiscussionLanguage.normalized(selection)
        }
    }
}
