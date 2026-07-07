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

struct Entitlements: Codable, Sendable, Equatable {
    var studios: Studios
    var features: Features
    var models: Rule
    var voices: Rule

    struct Studios: Codable, Sendable, Equatable {
        var discussion = false
        var audioBook = false
        var album = false
    }

    struct Features: Codable, Sendable, Equatable {
        var canPublishPodcast = false
        var canSharePodcastPrivately = false
        var canGenerateVideo = false
        var canGenerateSummary = false
        var canExportToNotion = false
        var canGeneratePPT = false
        var canGenerateMindmap = false
        var canGenerateCoverWithAI = false
    }

    /// A catalog constraint. Mode "all" allows every entry; mode "only" allows
    /// just the whitelisted ids (model ids or Azure voice ShortNames).
    struct Rule: Codable, Sendable, Equatable {
        var mode: String = "all"
        var allow: [String] = []

        func allows(_ id: String) -> Bool {
            mode != "only" || allow.contains(id)
        }
    }

    func isModelAllowed(_ id: String) -> Bool { models.allows(id) }
    func isVoiceAllowed(_ shortName: String) -> Bool { voices.allows(shortName) }

    /// Which content types the user may create.
    func canCreate(studio: StudioKind) -> Bool {
        switch studio {
        case .discussion: return studios.discussion
        case .audioBook: return studios.audioBook
        case .album: return studios.album
        }
    }

    enum StudioKind { case discussion, audioBook, album }

    /// Everything granted — the optimistic default used while entitlements load
    /// (and the fail-open state on a load error) so the UI doesn't flash disabled.
    static let all = Entitlements(
        studios: Studios(discussion: true, audioBook: true, album: true),
        features: Features(
            canPublishPodcast: true, canSharePodcastPrivately: true, canGenerateVideo: true,
            canGenerateSummary: true, canExportToNotion: true, canGeneratePPT: true,
            canGenerateMindmap: true, canGenerateCoverWithAI: true),
        models: Rule(mode: "all", allow: []),
        voices: Rule(mode: "all", allow: []))

    /// Nothing granted — used by UI tests (E2E_NO_PERMISSION) to assert the
    /// disabled state of gated surfaces.
    static let none = Entitlements(
        studios: Studios(),
        features: Features(),
        models: Rule(mode: "only", allow: []),
        voices: Rule(mode: "only", allow: []))
}

/// App-wide holder for the resolved entitlements. Created in `iOSApp` and
/// injected via the environment, mirroring `PurchaseManager`. Loaded once the
/// user is authenticated (see `RootView`).
@Observable
@MainActor
final class EntitlementsManager {
    /// Optimistic default until the first load completes.
    private(set) var current: Entitlements = .all

    private let tokens: TokenProviding

    init(tokens: TokenProviding) {
        self.tokens = tokens
    }

    /// Fetches the caller's entitlements. E2E defaults to the optimistic `.all`
    /// state so an unseeded entitlement table cannot gray out creation flows;
    /// `E2E_NO_PERMISSION` still short-circuits to `.none` for gated-surface
    /// tests. On a network error it leaves the current value (fail-open) — the
    /// generation endpoints remain the real enforcement boundary.
    func load() async {
        if AppConfig.e2eNoPermission {
            current = .none
            return
        }
        if AppConfig.isE2E {
            return
        }
        if let ent = try? await APIClient(tokens: tokens).entitlements() {
            current = ent
        }
    }

    /// Convenience passthroughs for call sites.
    var features: Entitlements.Features { current.features }
    var studios: Entitlements.Studios { current.studios }
    func isModelAllowed(_ id: String) -> Bool { current.isModelAllowed(id) }
    func isVoiceAllowed(_ shortName: String) -> Bool { current.isVoiceAllowed(shortName) }
    func canCreate(studio: Entitlements.StudioKind) -> Bool { current.canCreate(studio: studio) }
}
