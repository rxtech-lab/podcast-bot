import Foundation
import Observation

/// Persists the launch-flow state on device: whether the Welcome sheet has been
/// shown, and the set of What's New feature ids the user has already seen.
/// `UserDefaults` is injectable so tests can use an isolated suite.
@Observable
@MainActor
final class LaunchFlowStore {
    private let defaults: UserDefaults

    static let welcomeKey = "launch.hasSeenWelcome"
    static let seenFeaturesKey = "launch.seenFeatureIDs"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    /// True once the user has completed (seen) the Welcome sheet.
    var hasSeenWelcome: Bool {
        defaults.bool(forKey: Self.welcomeKey)
    }

    /// Feature ids the user has already seen.
    var seenFeatureIDs: Set<String> {
        Set(defaults.stringArray(forKey: Self.seenFeaturesKey) ?? [])
    }

    /// Features the user hasn't seen yet — the diff that decides "what's new".
    var unseenFeatures: [WhatsNewFeature] {
        let seen = seenFeatureIDs
        return WhatsNewFeature.all.filter { !seen.contains($0.id) }
    }

    func markWelcomeSeen() {
        defaults.set(true, forKey: Self.welcomeKey)
    }

    /// Unions the given ids into the seen set (idempotent).
    func markFeaturesSeen(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        let merged = seenFeatureIDs.union(ids)
        defaults.set(Array(merged), forKey: Self.seenFeaturesKey)
    }
}
