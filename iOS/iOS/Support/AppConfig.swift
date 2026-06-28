import Foundation

/// Environment configuration sourced from the active xcconfig (Debug/Release)
/// via the app's Info.plist, mirroring linda-assistant's `AppConfig`. Falls back
/// to sensible compiled-in defaults so the app still runs before the Info.plist
/// keys are wired (Debug → localhost:8000, Release → debatebot.rxlab.app).
enum AppConfig {
    private static func value(_ key: String) -> String? {
        guard let raw = Bundle.main.object(forInfoDictionaryKey: key) as? String else { return nil }
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        // Ignore unexpanded build-setting placeholders like "$(AppAPIBaseURL)".
        if trimmed.isEmpty || trimmed.contains("$(") { return nil }
        return trimmed
    }

    // MARK: - E2E test mode

    /// True when the app is launched by the XCUITest harness in hermetic E2E
    /// mode. Drives auth bypass, the API base-URL override, and deep-link
    /// injection below. Read once from the launch environment.
    static let isE2E: Bool = ProcessInfo.processInfo.environment["E2E_TEST_MODE"] == "1"

    /// The static bearer token the E2E harness sends; the backend ignores its
    /// value in E2E mode (every request resolves to the fixed "test" user).
    static let e2eAuthToken = "e2e-test-token"

    /// A deep link the harness wants routed through DeepLinkRouter on launch
    /// (e.g. "debatepod://d/test-ready"). Nil when not testing deep links.
    static let e2eDeepLink: URL? = {
        guard let s = ProcessInfo.processInfo.environment["E2E_DEEP_LINK"] else { return nil }
        return URL(string: s)
    }()

    /// Base URL of the debate-bot engine (no trailing slash).
    static let apiBaseURL: URL = {
        // E2E override wins so the suite can point at a local seeded server.
        if let s = ProcessInfo.processInfo.environment["E2E_API_BASE_URL"], let url = URL(string: s) { return url }
        if let s = value("AppAPIBaseURL"), let url = URL(string: s) { return url }
        #if DEBUG
        return URL(string: "http://localhost:8000")!
        #else
        return URL(string: "https://debatebot.rxlab.app")!
        #endif
    }()

    /// Public base of the deep-link website (no trailing slash). Used to build
    /// shareable links like `https://podcast.rxlab.app/d/{id}` client-side.
    static let websiteBaseURL: URL = {
        if let s = value("AppWebsiteBaseURL"), let url = URL(string: s) { return url }
        return URL(string: "https://podcast.rxlab.app")!
    }()

    /// rxlab OIDC issuer.
    static let authIssuer: String = value("AppAuthIssuer") ?? "https://auth.rxlab.app"

    /// rxlab OAuth client id registered for this app.
    static let authClientID: String = value("AppAuthClientID") ?? ""

    /// OAuth redirect URI (custom scheme registered in Info.plist).
    static let authRedirectURI: String = value("AppAuthRedirectURI") ?? "debatepod://oauth-callback"

    /// Space-separated OAuth scopes.
    static let authScopes: [String] = {
        let raw = value("AppAuthScopes") ?? "openid"
        return raw.split(separator: " ").map(String.init)
    }()

    /// True when a client id has been configured (sign-in is possible).
    static var hasOAuth: Bool { !authClientID.isEmpty }

    /// RevenueCat public SDK API key (sourced from Secrets.xcconfig). Empty when
    /// unset, which leaves the purchases layer disabled rather than crashing.
    static let revenueCatAPIKey: String = value("RevenueCatAPIKey") ?? ""

    /// True when a RevenueCat key is configured (purchases can be initialised).
    static var hasRevenueCat: Bool { !revenueCatAPIKey.isEmpty }

    /// APNs token environment attached to push-token registration.
    static let apnsEnvironment: String = {
        #if DEBUG
        return "sandbox"
        #else
        return "production"
        #endif
    }()
}
