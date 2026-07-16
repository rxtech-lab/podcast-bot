import SwiftUI

/// Sets a cover on a discussion without publishing it. Presented from the
/// player's actions menu so any podcast — public or private — can carry cover
/// art. Saving persists the cover via PATCH /api/discussions/{id}/cover.
///
/// Language-aware: when the bound discussion is presented through a
/// translation (the player's language switcher), edits target that language's
/// dedicated cover instead of silently overwriting the default one.
struct CoverEditorSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    @Binding var discussion: Discussion
    @State private var cover: DiscussionCover
    @State private var isWorking = false
    @State private var errorMessage: String?

    init(discussion: Binding<Discussion>) {
        _discussion = discussion
        let initialCover = discussion.wrappedValue.cover?.isPublishable == true
            ? discussion.wrappedValue.cover!
            : .defaultGradient
        _cover = State(initialValue: initialCover)
    }

    /// The translation language the player is currently presenting, or nil
    /// when the discussion is shown in its original language.
    private var presentedTranslationLanguage: String? {
        guard let main = discussion.mainLanguage, !main.isEmpty,
              !discussion.language.isEmpty,
              discussion.language.caseInsensitiveCompare(main) != .orderedSame else { return nil }
        return discussion.language
    }

    /// The translation language whose cover is being edited, or nil when the
    /// default cover is the target. A podcast with no usable cover yet always
    /// saves the default cover first — it is the fallback every language needs
    /// (and publishing requires it) — even while viewing a translation.
    private var editingLanguage: String? {
        guard discussion.cover?.isPublishable == true else { return nil }
        return presentedTranslationLanguage
    }

    private var editingLanguageName: String? {
        editingLanguage.map { languageName($0) }
    }

    private func languageName(_ code: String) -> String {
        Locale.current.localizedString(forIdentifier: code) ?? code
    }

    /// Explains which cover the Save button writes: the language-specific one,
    /// the first-ever cover (which becomes the default), or the default.
    private var targetDescription: String {
        if let name = editingLanguageName {
            return "You're viewing this podcast in \(name), so saving sets the cover shown to \(name) listeners. The default cover stays unchanged."
        }
        if let language = presentedTranslationLanguage {
            return "This podcast has no cover yet, so this first cover becomes the default cover for all languages. After saving, reopen the editor to set a dedicated \(languageName(language)) cover."
        }
        return "This sets the podcast's default cover, shown to every listener without a language-specific cover."
    }

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: editingLanguage.map { .discussionTranslation(id: discussion.id, language: $0) }
                                ?? .discussion(id: discussion.id),
                            title: discussion.displayTitle,
                            cover: $cover,
                            isWorking: $isWorking)

                Section {
                    Text(targetDescription)
                        .font(.footnote)
                        .foregroundStyle(Theme.secondaryText)
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle(editingLanguageName.map { "Cover · \($0)" } ?? "Cover")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        save()
                    } label: {
                        if isWorking {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Text("Save")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable)
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
    }

    private func save() {
        isWorking = true
        errorMessage = nil
        let language = editingLanguage
        Task { @MainActor in
            defer { isWorking = false }
            do {
                let updated = try await APIClient(tokens: auth).updateDiscussionCover(id: discussion.id,
                                                                                      cover: cover,
                                                                                      language: language)
                if language != nil {
                    // Keep the translated presentation the player is showing:
                    // the response is rendered in the device language, so
                    // adopting it wholesale would flip the view. Take just the
                    // saved cover and the refreshed translation metas.
                    discussion.cover = cover
                    if let items = updated.translations {
                        discussion.translations = items
                    }
                } else {
                    discussion = updated
                }
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Sets the language-dedicated cover on one translation of a discussion.
/// Presented from the translation settings sheet; saving persists the cover on
/// that translation row via PATCH /api/discussions/{id}/cover with a language,
/// so viewers of that language see it while everyone else keeps the default.
struct TranslationCoverEditorSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussionID: String
    let language: String
    let title: String
    var onSaved: (Discussion) -> Void
    @State private var cover: DiscussionCover
    @State private var isWorking = false
    @State private var errorMessage: String?

    init(discussionID: String,
         language: String,
         title: String,
         cover: DiscussionCover?,
         onSaved: @escaping (Discussion) -> Void) {
        self.discussionID = discussionID
        self.language = language
        self.title = title
        self.onSaved = onSaved
        let initialCover = cover?.isPublishable == true ? cover! : .defaultGradient
        _cover = State(initialValue: initialCover)
    }

    private var languageName: String {
        Locale.current.localizedString(forIdentifier: language) ?? language
    }

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: .discussionTranslation(id: discussionID, language: language),
                            title: title,
                            cover: $cover,
                            isWorking: $isWorking)

                Section {
                    Text("This cover is shown only to \(languageName) listeners. Everyone else keeps seeing the default cover.")
                        .font(.footnote)
                        .foregroundStyle(Theme.secondaryText)
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Cover")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        save()
                    } label: {
                        if isWorking {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Text("Save")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable)
                    .accessibilityIdentifier("translationCover.save")
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
    }

    private func save() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                let updated = try await APIClient(tokens: auth).updateDiscussionCover(id: discussionID,
                                                                                      cover: cover,
                                                                                      language: language)
                onSaved(updated)
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Sets a cover on an album. Presented from the album page's actions menu;
/// saving persists the cover via PATCH /api/albums/{id}/cover.
struct AlbumCoverEditorSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let album: AlbumDTO
    var onSaved: (AlbumDTO) -> Void
    @State private var cover: DiscussionCover
    @State private var isWorking = false
    @State private var errorMessage: String?

    init(album: AlbumDTO, onSaved: @escaping (AlbumDTO) -> Void) {
        self.album = album
        self.onSaved = onSaved
        let initialCover = album.cover?.isPublishable == true
            ? album.cover!
            : .defaultGradient
        _cover = State(initialValue: initialCover)
    }

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: .album(id: album.id),
                            title: album.title,
                            cover: $cover,
                            isWorking: $isWorking)

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Cover")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        save()
                    } label: {
                        if isWorking {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Text("Save")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable)
                    .accessibilityIdentifier("albumCover.save")
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
    }

    private func save() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                let updated = try await APIClient(tokens: auth).updateAlbumCover(id: album.id, cover: cover)
                onSaved(updated)
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
