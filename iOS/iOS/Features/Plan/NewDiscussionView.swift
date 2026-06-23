import SwiftUI

/// Step 1 of planning: enter a topic + panelist count, then ask the engine to
/// draft a plan (title, background, people, researched sources). On success the
/// plan is persisted and pushed into the editor.
struct NewDiscussionView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    /// Called once the placeholder discussion is created. The plan itself is then
    /// streamed on the plan page, so the request is handed along to drive it.
    var onPlanned: (Discussion, PlanRequest) -> Void = { _, _ in }

    @State private var topic = ""
    @AppStorage("newDiscussion.discussants") private var discussants = 3
    @AppStorage("newDiscussion.language") private var language = "en-US"
    @State private var attachments: [PendingAttachment] = []
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
                    .disabled(topic.trimmingCharacters(in: .whitespaces).isEmpty || isPlanning || attachments.isUploading)
                }
            }
        }
        .interactiveDismissDisabled(true)
        .onAppear(perform: normalizeStoredSettings)
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
                    Text("Tip: paste a link in the topic and the agent will read it.")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }

                optionsCard

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isPlanning {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Creating discussion…")
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

    /// One liquid-glass card grouping attach files, panelists, and language.
    private var optionsCard: some View {
        VStack(spacing: 0) {
            AttachmentsRow(attachments: $attachments, grouped: true)
            rowDivider
            panelistsRow
            rowDivider
            DiscussionLanguageMenu(selection: $language, grouped: true)
        }
        .glassEffect(in: .rect(cornerRadius: 16))
    }

    private var panelistsRow: some View {
        HStack(spacing: 12) {
            Image(systemName: "person.2.fill")
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text("Panelists")
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text("\(discussants) people")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            Stepper("Panelists", value: $discussants, in: 2...6)
                .labelsHidden()
        }
        .padding(12)
    }

    private var rowDivider: some View {
        Divider()
            .overlay(Theme.divider.opacity(0.5))
            .padding(.leading, 46)
    }

    private func normalizeStoredSettings() {
        discussants = min(max(discussants, 2), 6)
        language = DiscussionLanguage.normalized(language)
    }

    /// Creates the placeholder discussion (fast), then hands it plus the plan
    /// request to the caller, which navigates to the plan page where the plan is
    /// streamed in. Creating the row first means the discussion is saved even if
    /// the planning stream is later interrupted.
    private func plan() {
        let trimmed = topic.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        isPlanning = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        let ready = attachments.apiAttachments
        let request = PlanRequest(topic: trimmed, language: language, discussants: discussants,
                                  research: true, attachments: ready.isEmpty ? nil : ready)
        Task {
            do {
                let created = try await api.createDiscussion(topic: trimmed, language: language)
                isPlanning = false
                dismiss()
                onPlanned(created, request)
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
    /// Grouped renders the row without its own glass background so it can share
    /// one card with another control (e.g. the attach-files row).
    var grouped: Bool = false

    var body: some View {
        Menu {
            Picker(title, selection: $selection) {
                ForEach(DiscussionLanguage.supported) { language in
                    Text(language.label).tag(language.code)
                }
            }
        } label: {
            let row = HStack(spacing: 12) {
                Image(systemName: "globe")
                    .foregroundStyle(Theme.accent)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.headline)
                        .foregroundStyle(.primary)
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

            if grouped {
                row
            } else {
                row.glassEffect(in: .rect(cornerRadius: 16))
            }
        }
        .tint(Theme.accent)
        .onAppear {
            selection = DiscussionLanguage.normalized(selection)
        }
    }
}
