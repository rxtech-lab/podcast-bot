import SwiftUI
import TipKit

/// Step 1 of planning: enter a topic + panelist count, then ask the engine to
/// draft a plan (title, background, people, researched sources). On success the
/// plan is persisted and pushed into the editor.
struct NewDiscussionView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    /// Called once the placeholder discussion and its first planning turn are
    /// created. The plan page resumes that server-seeded turn.
    var onPlanned: (Discussion) -> Void = { _ in }
    @State private var topic = ""
    @State private var selectedReference: PodcastReference?
    @AppStorage("newDiscussion.type") private var discussionType = "discussion"
    @AppStorage("newDiscussion.discussants") private var discussants = 3
    @AppStorage("newDiscussion.language") private var language = "en-US"
    @State private var attachments: [PendingAttachment] = []
    @State private var discussionTypes: [DiscussionTypeDTO] = Self.defaultDiscussionTypes
    @AppStorage("newDiscussion.generateCover") private var generateCover = false
    @State private var isPlanning = false
    @State private var errorMessage: String?
    @State private var showingReferencePicker = false

    private static let defaultDiscussionTypes = [
        DiscussionTypeDTO(id: "discussion",
                          label: String(localized: "Discussion", comment: "Round-table discussion type option"))
    ]

    init(reference: PodcastReference? = nil, onPlanned: @escaping (Discussion) -> Void = { _ in }) {
        _selectedReference = State(initialValue: reference)
        self.onPlanned = onPlanned
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                form
            }
            .navigationTitle("New \(AppStringLiteral.stationNameRaw)")
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
                    .popoverTip(NewDiscussionPlanTip(), arrowEdge: .top)
                }
            }
        }
        .interactiveDismissDisabled(true)
        .onAppear(perform: normalizeStoredSettings)
        .sheet(isPresented: $showingReferencePicker) {
            ReferencePodcastPickerSheet(selection: $selectedReference)
        }
        .task {
            await loadDiscussionTypes()
        }
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

                referenceRow

                optionsCard

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isPlanning {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Creating \(AppStringLiteral.stationNameRaw)...")
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

    private var referenceRow: some View {
        HStack(spacing: 12) {
            Image(systemName: "rectangle.stack.badge.play")
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text("Parent Discussion")
                    .font(.headline)
                    .foregroundStyle(.primary)
                if let selectedReference {
                    Text(selectedReference.displayTitle)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(1)
                } else {
                    Text("None")
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer()
            if selectedReference != nil {
                Button {
                    self.selectedReference = nil
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(Theme.secondaryText)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear parent discussion")
            }
            Image(systemName: "chevron.up.chevron.down")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
        }
        .padding(12)
        .glassEffect(in: .rect(cornerRadius: 16))
        .contentShape(.rect)
        .onTapGesture { showingReferencePicker = true }
    }

    /// One liquid-glass card grouping attach files, panelists, and language.
    private var optionsCard: some View {
        VStack(spacing: 0) {
            AttachmentsRow(attachments: $attachments, grouped: true)
            rowDivider
            discussionTypeRow
            rowDivider
            panelistsRow
            rowDivider
            DiscussionLanguageMenu(selection: $language, grouped: true)
            rowDivider
            generateCoverRow
        }
        .glassEffect(in: .rect(cornerRadius: 16))
    }

    private var discussionTypeRow: some View {
        Menu {
            Picker("Type", selection: $discussionType) {
                ForEach(discussionTypes) { type in
                    Text(type.displayLabel).tag(type.id)
                }
            }
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "bubble.left.and.bubble.right.fill")
                    .foregroundStyle(Theme.accent)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Type")
                        .font(.headline)
                        .foregroundStyle(.primary)
                    Text(labelForDiscussionType(discussionType))
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
                Spacer()
                Image(systemName: "chevron.up.chevron.down")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(12)
        }
        .tint(Theme.accent)
    }

    /// Opt-in toggle: when on, the server generates AI cover art in the
    /// background after the discussion is created, and it appears the next time
    /// the discussion is opened.
    private var generateCoverRow: some View {
        HStack(spacing: 12) {
            Image(systemName: "photo.badge.plus")
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text("Generate cover")
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text("Create AI cover art in the background")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            Toggle("Generate cover", isOn: $generateCover)
                .labelsHidden()
                .tint(Theme.accent)
        }
        .padding(12)
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
        normalizeDiscussionType()
    }

    @MainActor
    private func loadDiscussionTypes() async {
        do {
            let api = APIClient(tokens: auth)
            let fetched = try await api.discussionTypes()
            discussionTypes = fetched.isEmpty ? Self.defaultDiscussionTypes : fetched
            normalizeDiscussionType()
        } catch {
            discussionTypes = Self.defaultDiscussionTypes
            normalizeDiscussionType()
        }
    }

    private func normalizeDiscussionType() {
        if !discussionTypes.contains(where: { $0.id == discussionType }) {
            discussionType = discussionTypes.first?.id ?? "discussion"
        }
    }

    private func labelForDiscussionType(_ id: String) -> String {
        discussionTypes.first(where: { $0.id == id })?.displayLabel ?? id
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
        let reference = selectedReference
        let request = PlanRequest(type: discussionType, topic: trimmed, language: language, discussants: discussants,
                                  research: true, attachments: ready.isEmpty ? nil : ready, reference: reference)
        Task {
            do {
                let created = try await api.createDiscussion(topic: trimmed,
                                                             language: language,
                                                             type: discussionType,
                                                             generateCover: generateCover,
                                                             referenceDiscussionID: reference?.id,
                                                             plan: request)
                isPlanning = false
                dismiss()
                onPlanned(created)
            } catch {
                isPlanning = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct ReferencePodcastPickerSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    @Binding var selection: PodcastReference?
    @State private var discussions: [Discussion] = []
    @State private var query = ""
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var searchTask: Task<Void, Never>?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                List {
                    if isLoading && discussions.isEmpty {
                        HStack {
                            Spacer()
                            ProgressView().tint(Theme.accent)
                            Spacer()
                        }
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                    ForEach(discussions) { discussion in
                        Button {
                            selection = discussion.podcastReference
                            dismiss()
                        } label: {
                            HStack(spacing: 12) {
                                DiscussionCoverThumbnail(discussion: discussion, size: 44)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(discussion.displayTitle)
                                        .font(.subheadline.weight(.semibold))
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                    Text(discussion.topic)
                                        .font(.caption)
                                        .foregroundStyle(Theme.secondaryText)
                                        .lineLimit(2)
                                }
                                Spacer()
                                if selection?.id == discussion.id {
                                    Image(systemName: "checkmark.circle.fill")
                                        .foregroundStyle(Theme.accent)
                                }
                            }
                            .padding(.vertical, 4)
                        }
                        .buttonStyle(.plain)
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                if let errorMessage, discussions.isEmpty {
                    ContentUnavailableView("Could not load stations",
                                           systemImage: "exclamationmark.triangle",
                                           description: Text(errorMessage))
                } else if !isLoading && discussions.isEmpty {
                    ContentUnavailableView("No Station",
                                           systemImage: "waveform",
                                           description: Text("Create or search for a podcast to use as follow-up context."))
                }
            }
            .navigationTitle("Parent Station")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search stations")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .task {
                await load()
            }
            .onChange(of: query) { _, value in
                searchTask?.cancel()
                searchTask = Task {
                    try? await Task.sleep(for: .milliseconds(250))
                    guard !Task.isCancelled else { return }
                    await load(search: value)
                }
            }
            .onDisappear {
                searchTask?.cancel()
            }
        }
    }

    @MainActor
    private func load(search: String? = nil) async {
        isLoading = true
        defer { isLoading = false }
        do {
            discussions = try await APIClient(tokens: auth).parentPodcasts(limit: 50, query: search ?? query)
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

private struct DiscussionCoverThumbnail: View {
    let discussion: Discussion
    let size: CGFloat

    var body: some View {
        Group {
            if let url = discussion.cover?.renderableImageURL {
                AsyncImage(url: url) { phase in
                    switch phase {
                    case let .success(image):
                        image
                            .resizable()
                            .scaledToFill()
                    default:
                        fallback
                    }
                }
            } else if let cover = discussion.cover, cover.hasGradient {
                LinearGradient(colors: [color(cover.gradientStart), color(cover.gradientEnd)],
                               startPoint: .topLeading,
                               endPoint: .bottomTrailing)
            } else {
                fallback
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: 8))
    }

    private var fallback: some View {
        ZStack {
            LinearGradient(colors: [Theme.accent.opacity(0.75), Color.orange.opacity(0.72)],
                           startPoint: .topLeading,
                           endPoint: .bottomTrailing)
            Image(systemName: "waveform")
                .font(.system(size: size * 0.38, weight: .semibold))
                .foregroundStyle(.white)
        }
    }

    private func color(_ hex: String?) -> Color {
        guard let hex else { return Theme.accent }
        let trimmed = hex.trimmingCharacters(in: CharacterSet(charactersIn: "# "))
        guard trimmed.count == 6, let value = Int(trimmed, radix: 16) else {
            return Theme.accent
        }
        let red = Double((value >> 16) & 0xff) / 255.0
        let green = Double((value >> 8) & 0xff) / 255.0
        let blue = Double(value & 0xff) / 255.0
        return Color(red: red, green: green, blue: blue)
    }
}

extension Discussion {
    var podcastReference: PodcastReference {
        PodcastReference(id: id, title: displayTitle, topic: topic)
    }
}

struct DiscussionLanguage: Identifiable, Hashable {
    let code: String
    let label: String

    var id: String { code }

    static let supported: [DiscussionLanguage] = [
        DiscussionLanguage(code: "en-US", label: String(localized: "English", comment: "Podcast language option")),
        DiscussionLanguage(code: "zh-CN", label: String(localized: "Chinese (Simplified)", comment: "Podcast language option")),
        DiscussionLanguage(code: "zh-TW", label: String(localized: "Chinese (Traditional)", comment: "Podcast language option")),
        DiscussionLanguage(code: "ja-JP", label: String(localized: "Japanese", comment: "Podcast language option")),
        DiscussionLanguage(code: "ko-KR", label: String(localized: "Korean", comment: "Podcast language option")),
        DiscussionLanguage(code: "es-ES", label: String(localized: "Spanish", comment: "Podcast language option")),
        DiscussionLanguage(code: "fr-FR", label: String(localized: "French", comment: "Podcast language option")),
        DiscussionLanguage(code: "de-DE", label: String(localized: "German", comment: "Podcast language option"))
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
    var title = String(localized: "\(AppStringLiteral.stationNameRaw) language", comment: "Label for the podcast language picker")
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
