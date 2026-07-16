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
    private let tokenStorage: SharedRxAuthTokenStorage
    private let legacyTokenStorage: KeychainTokenStorage

    /// When true (XCUITest E2E mode) the OAuth flow is bypassed entirely: the
    /// app reports a fixed authenticated "test" user and a static bearer token,
    /// so the suite never has to drive a real sign-in. The backend, also in E2E
    /// mode, ignores the token and resolves every request to the same user.
    private let isE2E: Bool = AppConfig.isE2E

    /// The fixed user surfaced in E2E mode. Its id matches the backend seed owner
    /// ("test") so ownership-dependent UI behaves consistently.
    private static var e2eUser: User {
        User(id: AppConfig.e2eUserID, name: AppConfig.e2eUserID, email: nil, image: nil)
    }

    init() {
        let keychainService = Self.keychainService
        let legacyStorage = KeychainTokenStorage(serviceName: keychainService)
        let storage = SharedRxAuthTokenStorage()
        Self.migrateTokensIfNeeded(from: legacyStorage, to: storage)
        self.tokenStorage = storage
        self.legacyTokenStorage = legacyStorage
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

    var authState: AuthenticationState { isE2E ? .authenticated : manager.authState }
    var isAuthenticated: Bool { authState == .authenticated }
    var currentUser: User? { isE2E ? Self.e2eUser : manager.currentUser }

    /// The current access token, used as the bearer for engine requests.
    var accessToken: String? { isE2E ? AppConfig.e2eAuthToken : tokenStorage.getAccessToken() }

    func restore() async {
        guard !isE2E else { return }
        await manager.checkExistingAuth()
    }

    func signIn() async throws {
        guard !isE2E else { return }
        try await manager.authenticate()
    }

    func signOut() async {
        guard !isE2E else { return }
        await manager.logout()
        try? legacyTokenStorage.clearAll()
    }

    /// Refreshes the token if needed and returns the latest access token (or nil).
    func refreshedAccessToken() async -> String? {
        if isE2E { return AppConfig.e2eAuthToken }
        try? await manager.refreshTokenIfNeeded()
        return tokenStorage.getAccessToken()
    }
}

extension AuthManager: TokenProviding {
    nonisolated func token() async -> String? { await accessToken }
    nonisolated func refreshedToken() async -> String? { await refreshedAccessToken() }
}

private extension AuthManager {
    static func migrateTokensIfNeeded(from legacy: KeychainTokenStorage,
                                      to shared: SharedRxAuthTokenStorage) {
        guard shared.getAccessToken() == nil, shared.getRefreshToken() == nil else { return }
        if let token = legacy.getAccessToken() { try? shared.saveAccessToken(token) }
        if let token = legacy.getRefreshToken() { try? shared.saveRefreshToken(token) }
        if let expiry = legacy.getExpiresAt() { try? shared.saveExpiresAt(expiry) }
    }
}

/// RxAuthSwift storage backed by the access-group keychain shared with the
/// extension. Keeping the protocol adapter here lets the shared primitive stay
/// independent from RxAuthSwift and compile in both targets.
private final class SharedRxAuthTokenStorage: TokenStorageProtocol, @unchecked Sendable {
    private let keychain = SharedTokenKeychain()

    func saveAccessToken(_ token: String) throws { try keychain.save(token, account: .accessToken) }
    func getAccessToken() -> String? { keychain.value(account: .accessToken) }
    func deleteAccessToken() throws { try keychain.delete(account: .accessToken) }
    func saveRefreshToken(_ token: String) throws { try keychain.save(token, account: .refreshToken) }
    func getRefreshToken() -> String? { keychain.value(account: .refreshToken) }
    func deleteRefreshToken() throws { try keychain.delete(account: .refreshToken) }

    func saveExpiresAt(_ date: Date) throws {
        try keychain.save(String(date.timeIntervalSince1970), account: .expiresAt)
    }

    func getExpiresAt() -> Date? {
        keychain.value(account: .expiresAt).flatMap(Double.init).map(Date.init(timeIntervalSince1970:))
    }

    func isTokenExpired() -> Bool {
        guard let expiry = getExpiresAt() else { return true }
        return expiry.timeIntervalSinceNow < 600
    }

    func clearAll() throws { try keychain.clear() }
}
