import Kingfisher
import SwiftUI

struct AlbumPublishSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let detail: AlbumDetailResponse
    var onPublished: (AlbumDetailResponse) -> Void

    @State private var mode: AlbumPublishMode = .all
    @State private var selected: Set<String>
    @State private var cover: DiscussionCover
    @State private var isWorking = false
    @State private var errorMessage: String?

    init(detail: AlbumDetailResponse, onPublished: @escaping (AlbumDetailResponse) -> Void) {
        self.detail = detail
        self.onPublished = onPublished
        _selected = State(initialValue: Set(detail.episodes.map(\.id)))
        let initialCover = detail.album.cover?.isPublishable == true ? detail.album.cover! : .defaultGradient
        _cover = State(initialValue: initialCover)
    }

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: .album(id: detail.album.id),
                            title: detail.album.title,
                            cover: $cover,
                            isWorking: $isWorking)

                Section {
                    Picker("Publish", selection: $mode) {
                        ForEach(AlbumPublishMode.allCases) { item in
                            Text(item.title).tag(item)
                        }
                    }
                    .pickerStyle(.segmented)
                    .accessibilityIdentifier("albumPublish.mode")
                }

                if mode == .selected {
                    Section("Podcasts") {
                        ForEach(detail.episodes) { episode in
                            Button {
                                toggleSelection(episode.id)
                            } label: {
                                HStack(spacing: 12) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(episode.displayTitle)
                                            .foregroundStyle(.primary)
                                        Text(episode.status.rawValue.capitalized)
                                            .font(.caption)
                                            .foregroundStyle(Theme.secondaryText)
                                    }
                                    Spacer()
                                    Image(systemName: selected.contains(episode.id) ? "checkmark.circle.fill" : "circle")
                                        .foregroundStyle(selected.contains(episode.id) ? Theme.accent : Theme.secondaryText)
                                }
                            }
                            .buttonStyle(.plain)
                            .accessibilityIdentifier("albumPublish.row.\(episode.id)")
                        }
                    }
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Publish Album")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        publish()
                    } label: {
                        if isWorking {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Text("Publish")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable || publishIDs.isEmpty)
                    .accessibilityIdentifier("albumPublish.submit")
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
        .accessibilityIdentifier("albumPublish.sheet")
    }

    private var publishIDs: [String] {
        switch mode {
        case .all:
            detail.episodes.map(\.id)
        case .selected:
            detail.episodes.map(\.id).filter { selected.contains($0) }
        }
    }

    private func toggleSelection(_ id: String) {
        if selected.contains(id) {
            selected.remove(id)
        } else {
            selected.insert(id)
        }
    }

    private func publish() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                let updated = try await APIClient(tokens: auth).publishAlbum(
                    id: detail.album.id,
                    mode: mode.rawValue,
                    discussionIDs: publishIDs,
                    cover: cover
                )
                onPublished(updated)
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Multi-select picker of ungrouped podcasts for the album's "Add Podcasts"
/// action. Podcasts already in another album are excluded (the server would
/// reject them with a 400).


