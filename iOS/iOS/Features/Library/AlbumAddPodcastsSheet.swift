import Kingfisher
import SwiftUI

struct AlbumAddPodcastsSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let albumID: String
    var onAdded: () -> Void

    @State private var candidates: [Discussion] = []
    @State private var selected: Set<String> = []
    @State private var isLoading = true
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView().tint(Theme.accent).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if candidates.isEmpty {
                    ContentUnavailableView(
                        "Nothing to add",
                        systemImage: "rectangle.stack",
                        description: Text("Every podcast already belongs to an album.")
                    )
                } else {
                    List(candidates) { candidate in
                        Button {
                            toggle(candidate.id)
                        } label: {
                            HStack(spacing: 12) {
                                DiscussionCoverThumbnail(discussion: candidate, size: 40)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(candidate.displayTitle)
                                        .font(.body.weight(.medium))
                                        .foregroundStyle(.primary)
                                        .lineLimit(2)
                                }
                                Spacer(minLength: 0)
                                Image(systemName: selected.contains(candidate.id) ? "checkmark.circle.fill" : "circle")
                                    .font(.title3)
                                    .foregroundStyle(selected.contains(candidate.id) ? Theme.accent : Color.secondary)
                            }
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("albumAdd.row.\(candidate.id)")
                    }
                    .listStyle(.plain)
                    .scrollContentBackground(.hidden)
                }
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle("Add Podcasts")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        submit()
                    } label: {
                        if isSubmitting { ProgressView() } else { Text("Add") }
                    }
                    .disabled(selected.isEmpty || isSubmitting)
                    .accessibilityIdentifier("albumAdd.submit")
                }
            }
        }
        .presentationDetents([.medium, .large])
        .alert("Couldn't add podcasts", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .task { await load() }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private func toggle(_ id: String) {
        if selected.contains(id) {
            selected.remove(id)
        } else {
            selected.insert(id)
        }
    }

    private func load() async {
        do {
            let all = try await APIClient(tokens: auth).discussions(limit: 100)
            candidates = all.filter { ($0.albumID ?? "").isEmpty }
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
    }

    private func submit() {
        guard !selected.isEmpty else { return }
        isSubmitting = true
        Task {
            do {
                _ = try await APIClient(tokens: auth).addToAlbum(id: albumID, discussionIDs: Array(selected))
                isSubmitting = false
                onAdded()
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// One episode row inside the album page: track number (or chapter range),
/// title, and status/duration.


