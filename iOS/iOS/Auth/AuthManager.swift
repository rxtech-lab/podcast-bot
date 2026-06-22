import Foundation
import Observation
import RxAuthSwift

/// Wraps RxAuthSwift's `OAuthManager` for the app, mirroring the patterns used
/// by RxCode and linda-assistant. Owns the OAuth configuration and exposes the
/// access token to the networking layer.
@Observable
@MainActor
final class AuthManager {
    static let keychainService = "app.rxlab.debate-bot.rxauth"
    let manager: OAuthManager
    private let tokenStorage: KeychainTokenStorage

    init() {
        let keychainService = Self.keychainService
        let storage = KeychainTokenStorage(serviceName: keychainService)
        self.tokenStorage = storage
        let config = RxAuthConfiguration(
            issuer: AppConfig.authIssuer,
            clientID: AppConfig.authClientID,
            redirectURI: AppConfig.authRedirectURI,
            scopes: AppConfig.authScopes,
            passkeyChallengePath: "/api/oauth/passkey/authenticate/options",
            passkeyVerificationPath: "/api/oauth/passkey/authenticate/verify",
            passkeyRegistrationChallengePath: "/api/oauth/passkey/register/options",
            passkeyRegistrationVerificationPath: "/api/oauth/passkey/register/verify",
            passkeyUpgradeChallengePath: "/api/oauth/passkey/upgrade/options",
            passkeyUpgradeVerificationPath: "/api/oauth/passkey/upgrade/verify",
            passkeyAccountCreationOptionsPath: "/api/oauth/passkey/account-creation/options",
            passkeyAccountCreationVerifyPath: "/api/oauth/passkey/account-creation/verify",
            passkeyRelyingPartyIdentifier: "rxlab.app",
            keychainServiceName: keychainService
        )
        self.manager = OAuthManager(configuration: config, tokenStorage: storage)
    }

    var authState: AuthenticationState { manager.authState }
    var isAuthenticated: Bool { manager.authState == .authenticated }
    var currentUser: User? { manager.currentUser }

    /// The current access token, used as the bearer for engine requests.
    var accessToken: String? { tokenStorage.getAccessToken() }

    func restore() async { await manager.checkExistingAuth() }

    func signIn() async throws { try await manager.authenticate() }

    func signOut() async { await manager.logout() }

    /// Refreshes the token if needed and returns the latest access token (or nil).
    func refreshedAccessToken() async -> String? {
        try? await manager.refreshTokenIfNeeded()
        return tokenStorage.getAccessToken()
    }
}

extension AuthManager: TokenProviding {
    nonisolated func token() async -> String? { await accessToken }
    nonisolated func refreshedToken() async -> String? { await refreshedAccessToken() }
}
