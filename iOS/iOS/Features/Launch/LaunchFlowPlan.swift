import Foundation

/// One step in the launch flow, presented in order.
enum LaunchStep: Equatable {
    case welcome
    case whatsNew([WhatsNewFeature])
    case paywall
}

/// Pure decision logic for the launch flow. Kept free of UI/persistence so the
/// combos are trivially unit-testable.
///
/// Rules (per product decision):
/// - Welcome shows only on the first launch ever (`!hasSeenWelcome`).
/// - What's New shows whenever there are unseen features.
/// - The Paywall auto-shows **only on the first launch** and only when the user
///   is not Pro. Returning non-subscribers are not auto-shown the paywall.
enum LaunchFlowPlan {
    static func steps(hasSeenWelcome: Bool,
                      unseenFeatures: [WhatsNewFeature],
                      isPro: Bool) -> [LaunchStep] {
        var steps: [LaunchStep] = []
        let isFirstLaunch = !hasSeenWelcome
        if isFirstLaunch {
            steps.append(.welcome)
        }
        if !unseenFeatures.isEmpty {
            steps.append(.whatsNew(unseenFeatures))
        }
        if isFirstLaunch && !isPro {
            steps.append(.paywall)
        }
        return steps
    }
}
