import SwiftUI

struct PublishStationSheet: View {
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
                CoverEditor(discussionID: discussion.id,
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
            .navigationTitle(discussion.isPublic ? "Station Visibility" : "Publish Station")
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
                            Text(discussion.isPublic ? "Update" : "Publish")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable)
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
    }

    private func publish() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                discussion = try await APIClient(tokens: auth).updateDiscussionVisibility(
                    id: discussion.id,
                    visibility: .public,
                    cover: cover
                )
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
