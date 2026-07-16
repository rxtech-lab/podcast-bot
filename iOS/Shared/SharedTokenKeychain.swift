import Foundation
import Security

/// Keychain primitive compiled into both the containing app and share extension.
/// The access group comes from each target's Info.plist so signing prefixes stay
/// configuration-owned rather than being hard-coded in Swift.
final class SharedTokenKeychain: @unchecked Sendable {
    static let serviceName = "app.rxlab.debate-bot.rxauth"

    enum Account: String {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresAt = "expires_at"
    }

    private let accessGroup: String?
    private let lock = NSLock()

    init(bundle: Bundle = .main) {
        let configured = bundle.object(forInfoDictionaryKey: "SharedKeychainAccessGroup") as? String
        let trimmed = configured?.trimmingCharacters(in: .whitespacesAndNewlines)
        accessGroup = (trimmed?.isEmpty == false && trimmed?.contains("$(") == false) ? trimmed : nil
    }

    func save(_ value: String, account: Account) throws {
        guard let data = value.data(using: .utf8) else { throw KeychainFailure.invalidData }
        lock.lock()
        defer { lock.unlock() }
        let base = query(account: account)
        SecItemDelete(base as CFDictionary)
        var add = base
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainFailure.status(status) }
    }

    func value(account: Account) -> String? {
        lock.lock()
        defer { lock.unlock() }
        var lookup = query(account: account)
        lookup[kSecReturnData as String] = true
        lookup[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: AnyObject?
        guard SecItemCopyMatching(lookup as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func delete(account: Account) throws {
        lock.lock()
        defer { lock.unlock() }
        let status = SecItemDelete(query(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainFailure.status(status)
        }
    }

    func clear() throws {
        try delete(account: .accessToken)
        try delete(account: .refreshToken)
        try delete(account: .expiresAt)
    }

    private func query(account: Account) -> [String: Any] {
        var value: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.serviceName,
            kSecAttrAccount as String: account.rawValue,
        ]
        if let accessGroup { value[kSecAttrAccessGroup as String] = accessGroup }
        return value
    }

    private enum KeychainFailure: Error {
        case invalidData
        case status(OSStatus)
    }
}
