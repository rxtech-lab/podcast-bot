import SwiftUI
import RevenueCat
import RevenueCatUI

/// The RevenueCat-hosted paywall (points top-up + Podcaster Pro subscriptions).
/// On a successful purchase it polls the server balance, since points are
/// credited asynchronously by the RevenueCat webhook.
struct PaywallScreen: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(PurchaseManager.self) private var purchases

    /// Called when the paywall finishes (close or purchase completes). Defaults
    /// to dismissing this view; the launch flow passes a callback to advance the
    /// coordinator instead.
    var onFinish: (() -> Void)?

    private func finish() {
        if let onFinish { onFinish() } else { dismiss() }
    }

    var body: some View {
        PaywallView(displayCloseButton: true)
            .onPurchaseCompleted { _ in
                Task {
                    await purchases.refreshBalanceAfterPurchase()
                    finish()
                }
            }
            .onRestoreCompleted { _ in
                Task { await purchases.refreshBalance() }
            }
    }
}

/// The RevenueCat Customer Center for managing / restoring subscriptions.
struct CustomerCenterScreen: View {
    var showsCloseButton = true

    var body: some View {
        #if os(macOS)
        // RevenueCat's Customer Center isn't available on macOS; point the
        // user at the App Store subscription management page instead.
        VStack(spacing: 16) {
            Image(systemName: "person.crop.circle")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text("Manage your subscription in the App Store.")
                .multilineTextAlignment(.center)
            Link("Manage Subscriptions",
                 destination: URL(string: "https://apps.apple.com/account/subscriptions")!)
        }
        .padding(32)
        .frame(minWidth: 360, minHeight: 240)
        #else
        CustomerCenterView(
            navigationOptions: CustomerCenterNavigationOptions(
                usesExistingNavigation: !showsCloseButton,
                shouldShowCloseButton: showsCloseButton
            )
        )
        #endif
    }
}
