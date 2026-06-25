import SwiftUI
import TipKit
import UIKit

/// Non-full-height bottom sheet for sharing a PRIVATE discussion: pick how long
/// the link should stay valid (1h … 72h), create it, and manage (share / revoke)
/// the active links. Public discussions don't use this — they share a plain
/// permanent `/d/{id}` link via the system share sheet directly from the menu.
struct ShareSheet: View {
    let discussionID: String
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var duration: ShareDuration = .oneDay
    @State private var links: [DiscussionShareLink] = []
    @State private var isLoading = true
    @State private var isCreating = false
    @State private var errorText: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Link expires after", selection: $duration) {
                        ForEach(ShareDuration.allCases) { d in
                            Text(d.shortLabel).tag(d)
                        }
                    }
                    .pickerStyle(.segmented)

                    Button {
                        Task { await createLink() }
                    } label: {
                        HStack {
                            Label("Create share link", systemImage: "link.badge.plus")
                            Spacer()
                            if isCreating { ProgressView() }
                        }
                    }
                    .disabled(isCreating)
                    .popoverTip(ShareStationTip(), arrowEdge: .top)
                } header: {
                    Text("New link")
                } footer: {
                    Text("Anyone with the link can join and comment until it expires (max 3 days). You can revoke a link any time.")
                }

                Section("Active links") {
                    if isLoading {
                        HStack { Spacer(); ProgressView(); Spacer() }
                    } else if links.isEmpty {
                        Text("No active links")
                            .foregroundStyle(.secondary)
                    } else {
                        ForEach(links) { link in
                            ShareLinkRow(link: link) {
                                Task { await revoke(link) }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Share")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .alert("Couldn't share", isPresented: Binding(
                get: { errorText != nil },
                set: { if !$0 { errorText = nil } }
            )) {
                Button("OK", role: .cancel) { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
            .task { await reload() }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    private func reload() async {
        isLoading = true
        defer { isLoading = false }
        do {
            links = try await api.listShares(discussionID: discussionID)
                .sorted { $0.createdAt > $1.createdAt }
        } catch {
            errorText = error.localizedDescription
        }
    }

    private func createLink() async {
        isCreating = true
        defer { isCreating = false }
        do {
            let link = try await api.createShare(discussionID: discussionID, ttlSeconds: duration.seconds)
            links.insert(link, at: 0)
        } catch {
            errorText = error.localizedDescription
        }
    }

    private func revoke(_ link: DiscussionShareLink) async {
        do {
            try await api.revokeShare(discussionID: discussionID, token: link.token)
            links.removeAll { $0.token == link.token }
        } catch {
            errorText = error.localizedDescription
        }
    }
}

private struct ShareLinkRow: View {
    let link: DiscussionShareLink
    let onRevoke: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(link.url.absoluteString)
                    .font(.footnote)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text("Expires \(link.expiresAt.formatted(.relative(presentation: .named)))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            ShareLink(item: link.url) {
                Image(systemName: "square.and.arrow.up")
            }
            .labelStyle(.iconOnly)
        }
        .swipeActions(edge: .trailing) {
            Button(role: .destructive, action: onRevoke) {
                Label("Revoke", systemImage: "trash")
            }
        }
    }
}

/// Presents iOS's system share sheet for a local file URL, including "Save to
/// Files" and app-to-app share destinations.
struct FileShareSheet: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: [url], applicationActivities: nil)
    }

    func updateUIViewController(_ uiViewController: UIActivityViewController, context: Context) {}
}
