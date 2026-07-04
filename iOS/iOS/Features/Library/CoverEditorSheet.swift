import SwiftUI

/// Sets a cover on a discussion without publishing it. Presented from the
/// player's actions menu so any podcast — public or private — can carry cover
/// art. Saving persists the cover via PATCH /api/discussions/{id}/cover.
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

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: .discussion(id: discussion.id),
                            title: discussion.displayTitle,
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
                discussion = try await APIClient(tokens: auth).updateDiscussionCover(id: discussion.id, cover: cover)
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
