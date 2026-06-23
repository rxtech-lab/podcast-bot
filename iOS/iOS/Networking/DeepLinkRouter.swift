import Foundation
import SwiftUI

/// Holds a pending deep link and the resolved discussion to open. The app entry
/// captures incoming universal links / custom-scheme URLs into `pending`; once
/// the user is authenticated, `RootView` calls `resolve` to fetch+join the
/// discussion and presents the player from `opened`.
@MainActor
@Observable
final class DeepLinkRouter {
    /// A deep link captured before it could be handled (e.g. arrived while
    /// signed out). Resolved once the app is authenticated.
    var pending: DeepLink?

    /// The discussion to present full-screen, plus the share token (if any) so
    /// the player can authorize this participant's comments.
    var opened: OpenedDiscussion?

    /// User-facing error from the most recent resolve attempt.
    var error: String?

    /// True while a deep link is being resolved (joining / fetching).
    var isResolving = false

    struct OpenedDiscussion: Identifiable {
        let id = UUID()
        let discussion: Discussion
        let shareToken: String?
    }

    /// Records an incoming URL as a pending deep link if it is one we handle.
    func handle(url: URL) {
        if let link = DeepLink(url: url) {
            pending = link
        }
    }

    /// Resolves the pending deep link into an open discussion. For a public
    /// link, fetches the market station and records a participant. For a private
    /// share link, joins via the token (which also returns the discussion).
    func resolvePending(api: APIClient) async {
        guard let link = pending, !isResolving else { return }
        isResolving = true
        defer { isResolving = false }
        do {
            switch link {
            case let .publicDiscussion(id):
                let discussion = try await api.marketStation(id: id)
                try? await api.joinDiscussion(id: id, token: nil)
                opened = OpenedDiscussion(discussion: discussion, shareToken: nil)
            case let .sharedDiscussion(token):
                let discussion = try await api.joinViaShare(token: token)
                opened = OpenedDiscussion(discussion: discussion, shareToken: token)
            }
            pending = nil
        } catch {
            self.error = error.localizedDescription
            pending = nil
        }
    }
}
