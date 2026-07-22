import SwiftUI

extension PodcastPlayerView {
    var currentCreator: CreatorProfile? {
        model?.discussion.creator ?? discussion.creator
    }

    var model: PlayerModel? {
        playerSession?.model
    }

    var currentDiscussion: Discussion {
        model?.discussion ?? discussion
    }

    var sourceLanguage: String {
        currentDiscussion.mainLanguage ?? discussion.mainLanguage ?? discussion.language
    }

    var readyTranslationLanguages: [String] {
        (currentDiscussion.translations ?? discussion.translations ?? [])
            .filter { $0.available || $0.status == .ready }
            .map(\.language)
            .filter { $0 != sourceLanguage }
            .sorted()
    }

    @ViewBuilder
    func languageMenuLabel(_ language: String) -> some View {
        if (model?.presentationLanguage ?? sourceLanguage).caseInsensitiveCompare(language) == .orderedSame {
            Label(TranslationLanguageOption.name(for: language), systemImage: "checkmark")
        } else {
            Text(TranslationLanguageOption.name(for: language))
        }
    }

    func switchPresentationLanguage(to language: String) async {
        guard let model, !isSwitchingLanguage else { return }
        isSwitchingLanguage = true
        defer { isSwitchingLanguage = false }
        do {
            try await model.switchPresentationLanguage(to: language)
        } catch {
            languageSwitchError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// Whether the Summary menu item is enabled — true only once the server has
    /// generated the podcast's summary document (status `ready`).
    var summaryAvailable: Bool {
        currentDiscussion.hasSummary
    }
}

private struct TranslationLanguageOption: Identifiable, Hashable {
    let id: String
    let name: String

    static let supported = [
        TranslationLanguageOption(id: "en-US", name: "English (United States)"),
        TranslationLanguageOption(id: "zh-CN", name: "简体中文"),
        TranslationLanguageOption(id: "zh-TW", name: "繁體中文"),
        TranslationLanguageOption(id: "ja-JP", name: "日本語"),
        TranslationLanguageOption(id: "ko-KR", name: "한국어"),
        TranslationLanguageOption(id: "es-ES", name: "Español"),
        TranslationLanguageOption(id: "fr-FR", name: "Français"),
        TranslationLanguageOption(id: "de-DE", name: "Deutsch"),
    ]

    static func name(for code: String) -> String {
        supported.first(where: { $0.id == code })?.name ?? code
    }
}

/// Owner-only sheet launched from the server-driven podcast menu. Generation
/// is asynchronous; the sheet polls only while a selected translation is being
/// produced, then lets the player adopt it without replacing the audio item.
struct TranslationSettingsSheet: View {
    let discussionID: String
    let sourceLanguage: String
    let initialTranslations: [DiscussionTranslationMeta]
    let api: APIClient
    let onTranslationsChanged: ([DiscussionTranslationMeta]) -> Void
    let onReady: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var targetLanguage = ""
    @State private var translations: [DiscussionTranslationMeta]
    @State private var isSubmitting = false
    @State private var errorMessage: String?
    @State private var coverEditing: TranslationCoverEditingContext?

    init(discussionID: String, sourceLanguage: String,
         initialTranslations: [DiscussionTranslationMeta], api: APIClient,
         onTranslationsChanged: @escaping ([DiscussionTranslationMeta]) -> Void,
         onReady: @escaping (String) -> Void) {
        self.discussionID = discussionID
        self.sourceLanguage = sourceLanguage
        self.initialTranslations = initialTranslations
        self.api = api
        self.onTranslationsChanged = onTranslationsChanged
        self.onReady = onReady
        _translations = State(initialValue: initialTranslations)
    }

    private var availableTargets: [TranslationLanguageOption] {
        TranslationLanguageOption.supported.filter { $0.id != sourceLanguage }
    }

    var selectedMeta: DiscussionTranslationMeta? {
        translations.first(where: { $0.language == targetLanguage })
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Translate to") {
                    Picker("Language", selection: $targetLanguage) {
                        ForEach(availableTargets) { option in
                            Text(option.name).tag(option.id)
                        }
                    }
                    .pickerStyle(.navigationLink)
                }

                Section("Included content") {
                    Label("Title and plan", systemImage: "doc.text")
                    Label("Transcript and captions", systemImage: "captions.bubble")
                    Label("Summary and mindmap", systemImage: "brain.head.profile")
                    Text("The original audio stays unchanged. Missing translated fields automatically use the podcast’s main language.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }

                if !translations.isEmpty {
                    Section("Translations") {
                        ForEach(translations) { translation in
                            HStack(spacing: 12) {
                                if translation.cover != nil {
                                    StationCoverArt(cover: translation.cover,
                                                    title: TranslationLanguageOption.name(for: translation.language))
                                        .frame(width: 36, height: 36)
                                        .clipShape(.rect(cornerRadius: 6))
                                }
                                Text(TranslationLanguageOption.name(for: translation.language))
                                Spacer()
                                if translation.status == .ready {
                                    Button {
                                        Task { await openCoverEditor(for: translation) }
                                    } label: {
                                        Image(systemName: "photo.badge.plus")
                                    }
                                    .buttonStyle(.borderless)
                                    .accessibilityIdentifier("translation.cover.\(translation.language)")
                                }
                                translationStatus(translation)
                            }
                        }
                    }
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage).foregroundStyle(.red)
                    }
                }
            }
            #if os(macOS)
            .formStyle(.grouped)
            #endif
            .navigationTitle("Translate Podcast")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(buttonTitle) {
                        Task { await startTranslation() }
                    }
                    .disabled(targetLanguage.isEmpty || isSubmitting || selectedMeta?.status == .generating)
                    .accessibilityIdentifier("translation.start")
                }
            }
            .task {
                if targetLanguage.isEmpty {
                    targetLanguage = availableTargets.first?.id ?? ""
                }
                await refresh()
            }
            .sheet(item: $coverEditing) { context in
                TranslationCoverEditorSheet(discussionID: discussionID,
                                            language: context.language,
                                            title: context.title,
                                            cover: context.cover) { updated in
                    if let items = updated.translations {
                        translations = items
                        onTranslationsChanged(items)
                    }
                }
            }
        }
    }

    var buttonTitle: String {
        if isSubmitting || selectedMeta?.status == .generating { return "Translating…" }
        if selectedMeta?.status == .ready { return "Translate Again" }
        if selectedMeta?.status == .failed { return "Retry" }
        return "Translate"
    }

    @ViewBuilder
    func translationStatus(_ translation: DiscussionTranslationMeta) -> some View {
        switch translation.status {
        case .generating:
            ProgressView().controlSize(.small)
        case .ready:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed:
            Image(systemName: "exclamationmark.circle.fill").foregroundStyle(.red)
        }
    }

    func refresh() async {
        if let response = try? await api.discussionTranslations(id: discussionID) {
            translations = response.translations
            onTranslationsChanged(response.translations)
        }
    }

    /// Seeds the cover editor's AI prompt with the translated title so the
    /// generated art matches the language, falling back to the language name
    /// when the translated detail cannot be fetched.
    func openCoverEditor(for translation: DiscussionTranslationMeta) async {
        let translated = try? await api.discussion(id: discussionID, language: translation.language)
        let title = translated?.displayTitle ?? TranslationLanguageOption.name(for: translation.language)
        coverEditing = TranslationCoverEditingContext(language: translation.language,
                                                      title: title,
                                                      cover: translation.cover)
    }

    func startTranslation() async {
        guard !targetLanguage.isEmpty else { return }
        isSubmitting = true
        errorMessage = nil
        do {
            let started = try await api.translateDiscussion(id: discussionID,
                                                            targetLanguage: targetLanguage)
            translations.removeAll(where: { $0.language == started.language })
            translations.append(started)
            for _ in 0..<300 {
                try await Task.sleep(for: .seconds(2))
                try Task.checkCancellation()
                let response = try await api.discussionTranslations(id: discussionID)
                translations = response.translations
                onTranslationsChanged(response.translations)
                guard let current = translations.first(where: { $0.language == targetLanguage }) else { continue }
                if current.status == .ready {
                    isSubmitting = false
                    onReady(targetLanguage)
                    return
                }
                if current.status == .failed {
                    throw APIError.invalidRequest(current.error ?? "Translation failed.")
                }
            }
            throw APIError.invalidRequest("Translation is taking longer than expected. You can close this sheet and return later.")
        } catch is CancellationError {
            isSubmitting = false
        } catch {
            isSubmitting = false
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// The translation row whose language cover is being edited, plus the
/// translated title used to seed the AI prompt.
private struct TranslationCoverEditingContext: Identifiable {
    let language: String
    let title: String
    let cover: DiscussionCover?

    var id: String { language }
}

struct CaptionDownloadSheet: View {
    let jobID: String
    let title: String
    let language: String?
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var formats: [CaptionDownloadFormat] = []
    @State private var selectedFormatID = ""
    @State private var isLoading = true
    @State private var downloadingFormatID: String?
    @State private var downloadedFile: CaptionDownloadedFile?
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView("Loading formats")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if formats.isEmpty {
                    ContentUnavailableView(
                        "No Caption Formats",
                        systemImage: "captions.bubble",
                        description: Text("No caption download formats are currently available.")
                    )
                } else {
                    Form {
                        Section("Format") {
                            Picker("Caption Format", selection: $selectedFormatID) {
                                ForEach(formats) { format in
                                    Text("\(format.displayName) (.\(format.fileExtension.uppercased()))")
                                        .tag(format.id)
                                }
                            }
                            .pickerStyle(.navigationLink)
                            .accessibilityIdentifier("captions.formatPicker")
                        }
                    }
                }
            }
            .navigationTitle("Download Captions")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if !isLoading && !formats.isEmpty {
                    Button {
                        Task { await downloadSelectedFormat() }
                    } label: {
                        HStack {
                            if downloadingFormatID != nil {
                                ProgressView().controlSize(.small)
                                Text("Downloading")
                            } else {
                                Label("Download", systemImage: "arrow.down.circle")
                            }
                        }
                        .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .disabled(selectedFormat == nil || downloadingFormatID != nil)
                    .accessibilityIdentifier("captions.download")
                    .padding()
                }
            }
        }
        .presentationDetents([.medium, .large])
        .task { await loadFormats() }
        .sheet(item: $downloadedFile) { file in
            FileShareSheet(url: file.url)
        }
        .alert("Caption Download Failed", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    func loadFormats() async {
        isLoading = true
        defer { isLoading = false }
        do {
            formats = try await api.captionDownloadFormats()
            if !formats.contains(where: { $0.id == selectedFormatID }) {
                selectedFormatID = formats.first?.id ?? ""
            }
        } catch {
            formats = []
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    var selectedFormat: CaptionDownloadFormat? {
        formats.first(where: { $0.id == selectedFormatID })
    }

    func downloadSelectedFormat() async {
        guard let selectedFormat else { return }
        await download(selectedFormat)
    }

    func download(_ format: CaptionDownloadFormat) async {
        guard downloadingFormatID == nil else { return }
        downloadingFormatID = format.id
        defer { downloadingFormatID = nil }
        do {
            let url = try await api.downloadCaptions(
                jobID: jobID,
                format: format,
                title: title,
                language: language
            )
            #if os(macOS)
            _ = try await MacFileSavePanel.save(url)
            #else
            downloadedFile = CaptionDownloadedFile(url: url)
            #endif
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

private struct CaptionDownloadedFile: Identifiable {
    let id = UUID()
    let url: URL
}
