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

    /// Base URL of the debate-bot engine (no trailing slash).
    static let apiBaseURL: URL = {
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
        let raw = value("AppAuthScopes") ?? "openid profile email"
        return raw.split(separator: " ").map(String.init)
    }()

    /// True when a client id has been configured (sign-in is possible).
    static var hasOAuth: Bool { !authClientID.isEmpty }

    /// RevenueCat public SDK API key (sourced from Secrets.xcconfig). Empty when
    /// unset, which leaves the purchases layer disabled rather than crashing.
    static let revenueCatAPIKey: String = value("RevenueCatAPIKey") ?? ""

    /// True when a RevenueCat key is configured (purchases can be initialised).
    static var hasRevenueCat: Bool { !revenueCatAPIKey.isEmpty }
}
