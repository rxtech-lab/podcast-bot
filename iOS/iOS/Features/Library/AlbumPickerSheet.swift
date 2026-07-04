import SwiftUI

/// Picks (or creates) an album to add a podcast to, from the player toolbar's
/// "Add to Album" action. The server rejects podcasts that already belong to a
/// different album with a 400, surfaced here as an alert.
struct AlbumPickerSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussion: Discussion
    var onAdded: (AlbumDTO) -> Void

    @State private var albums: [AlbumDTO] = []
    @State private var isLoading = true
    @State private var isSubmitting = false
    @State private var newAlbumTitle = ""
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView().tint(Theme.accent).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    pickerList
                }
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle("Add to Album")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .alert("Couldn't add to album", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .task { await load() }
        .accessibilityIdentifier("albumPicker.sheet")
    }

    private var pickerList: some View {
        List {
            Section("New Album") {
                HStack(spacing: 10) {
                    TextField("Album name", text: $newAlbumTitle)
                        .accessibilityIdentifier("albumPicker.newTitle")
                    Button {
                        createAlbum()
                    } label: {
                        if isSubmitting {
                            ProgressView().controlSize(.small)
                        } else {
                            Text("Create").font(.body.weight(.semibold))
                        }
                    }
                    .disabled(isSubmitting || newAlbumTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .accessibilityIdentifier("albumPicker.create")
                }
            }

            if !albums.isEmpty {
                Section("Your Albums") {
                    ForEach(albums) { album in
                        Button {
                            add(to: album)
                        } label: {
                            HStack(spacing: 12) {
                                AlbumCoverThumbnail(cover: album.cover, size: 40)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(album.title)
                                        .font(.body.weight(.medium))
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                    Text("\(album.episodeCount ?? 0) episode\((album.episodeCount ?? 0) == 1 ? "" : "s")")
                                        .font(.caption)
                                        .foregroundStyle(Theme.secondaryText)
                                }
                                Spacer(minLength: 0)
                                Image(systemName: "plus.circle")
                                    .foregroundStyle(Theme.accent)
                            }
                        }
                        .disabled(isSubmitting)
                        .accessibilityIdentifier("albumPicker.album.\(album.id)")
                    }
                }
            }
        }
        .scrollContentBackground(.hidden)
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private func load() async {
        do {
            albums = try await APIClient(tokens: auth).albums()
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
    }

    private func createAlbum() {
        let title = newAlbumTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        isSubmitting = true
        Task {
            do {
                let album = try await APIClient(tokens: auth).createAlbum(title: title, discussionIDs: [discussion.id])
                isSubmitting = false
                onAdded(album)
                dismiss()
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func add(to album: AlbumDTO) {
        isSubmitting = true
        Task {
            do {
                let updated = try await APIClient(tokens: auth).addToAlbum(id: album.id, discussionIDs: [discussion.id])
                isSubmitting = false
                onAdded(updated)
                dismiss()
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
