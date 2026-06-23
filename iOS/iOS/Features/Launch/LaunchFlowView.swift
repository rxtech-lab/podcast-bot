import SwiftUI

/// Coordinates the launch flow inside a single sheet: shows each step in order
/// (Welcome → What's New → Paywall) and advances on completion. The set of
/// `steps` is captured once by the caller so it can't change underneath the
/// sheet as features get marked seen.
struct LaunchFlowView: View {
    let steps: [LaunchStep]
    var onWelcomeSeen: () -> Void
    var onFeaturesSeen: ([String]) -> Void
    var onFinished: () -> Void

    @State private var index = 0

    var body: some View {
        Group {
            if index < steps.count {
                switch steps[index] {
                case .welcome:
                    WelcomeSheet {
                        onWelcomeSeen()
                        advance()
                    }
                case .whatsNew(let features):
                    WhatsNewSheet(features: features) {
                        onFeaturesSeen(features.map(\.id))
                        advance()
                    }
                case .paywall:
                    PaywallScreen(onFinish: advance)
                }
            } else {
                Color.clear.onAppear(perform: onFinished)
            }
        }
    }

    private func advance() {
        if index + 1 < steps.count {
            index += 1
        } else {
            onFinished()
        }
    }
}
