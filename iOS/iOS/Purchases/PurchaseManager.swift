import Foundation
import Observation
import RevenueCat

/// Wraps the RevenueCat SDK: exposes the customer's entitlements/offerings, the
/// server-side points balance, and the purchase/identity lifecycle. A single
/// instance is created in `iOSApp` and injected via the environment.
///
/// Identity: the backend keys a user as `oauth:<subject>` and the RevenueCat
/// webhook credits `oauth:<app_user_id>`, so we set RevenueCat's app user id to
/// the OAuth subject (`AuthManager.currentUser.id`) on sign-in.
@Observable
@MainActor
final class PurchaseManager {
    /// Entitlement identifier configured in the RevenueCat dashboard.
    static let proEntitlementID = "Podcaster Pro"

    private(set) var customerInfo: CustomerInfo?
    private(set) var offerings: Offerings?
    private(set) var pointsBalance: Int?
    let isConfigured: Bool

    private let tokens: TokenProviding

    /// True while the "Podcaster Pro" entitlement is active.
    var isPro: Bool {
        customerInfo?.entitlements[Self.proEntitlementID]?.isActive == true
    }

    init(tokens: TokenProviding) {
        self.tokens = tokens
        self.isConfigured = AppConfig.hasRevenueCat
        guard isConfigured else { return }
        observeCustomerInfo()
        Task { await bootstrap() }
    }

    private var api: APIClient { APIClient(tokens: tokens) }

    private func observeCustomerInfo() {
        Task { [weak self] in
            for await info in Purchases.shared.customerInfoStream {
                self?.customerInfo = info
            }
        }
    }

    private func bootstrap() async {
        customerInfo = try? await Purchases.shared.customerInfo()
        await refreshOfferings()
        await refreshBalance()
    }

    func refreshOfferings() async {
        guard isConfigured else { return }
        offerings = try? await Purchases.shared.offerings()
    }

    func refreshBalance() async {
        pointsBalance = try? await api.pointsBalance()
    }

    /// Associates RevenueCat purchases with the signed-in user. Call when auth
    /// becomes authenticated, passing the OAuth subject.
    func identify(userID: String) async {
        guard isConfigured, !userID.isEmpty else { return }
        if let result = try? await Purchases.shared.logIn(userID) {
            customerInfo = result.customerInfo
        }
        await refreshBalance()
    }

    /// Detaches the current user (anonymous RevenueCat id) on sign-out.
    func signOut() async {
        pointsBalance = nil
        guard isConfigured else { return }
        customerInfo = try? await Purchases.shared.logOut()
    }

    /// After a successful purchase the RevenueCat webhook credits points
    /// server-side with a short delay; poll a few times so the balance reflects
    /// the new credit without forcing the user to refresh manually.
    func refreshBalanceAfterPurchase() async {
        let before = pointsBalance
        for _ in 0..<6 {
            try? await Task.sleep(for: .seconds(2))
            await refreshBalance()
            if pointsBalance != before { return }
        }
    }
}
