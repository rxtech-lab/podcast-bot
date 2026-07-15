import SwiftUI

struct PodcastReferencePreviewSheet: View {
    @Environment(AuthManager.self) var auth
    @Environment(\.dismiss) var dismiss
    let reference: PodcastReference
    @State var discussion: Discussion?
    @State var errorMessage: String?
    @State var isLoading = false

    var body: some View {
        NavigationStack {
            Group {
                if let discussion {
                    PodcastPlayerView(discussion: discussion)
                } else if isLoading {
                    ProgressView()
                        .tint(Theme.accent)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Theme.background.ignoresSafeArea())
                } else {
                    ContentUnavailableView(reference.displayTitle,
                                           systemImage: "waveform.circle",
                                           description: Text(errorMessage ?? reference.subtitle))
                        .background(Theme.background.ignoresSafeArea())
                }
            }
            .navigationTitle(reference.displayTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") {
                        dismiss()
                    }
                }
            }
        }
        .task(id: reference.id) {
            await load()
        }
    }

    @MainActor
    func load() async {
        isLoading = true
        defer { isLoading = false }
        let api = APIClient(tokens: auth)
        do {
            discussion = try await api.discussion(id: reference.id)
            errorMessage = nil
        } catch {
            do {
                discussion = try await api.marketStation(id: reference.id)
                errorMessage = nil
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
