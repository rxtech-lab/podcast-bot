import Foundation
import JSONSchemaForm
import OSLog

let apiLog = Logger(subsystem: "com.debatebot.ios", category: "APIClient")

/// Supplies bearer tokens to the API client. AuthManager conforms to this.
protocol TokenProviding: Sendable {
    func token() async -> String?
    func refreshedToken() async -> String?
}

/// Maps a non-2xx response into a typed APIError, decoding the points-shortfall
/// body into `.insufficientPoints` so callers can present the paywall.
func mapHTTPError(_ status: Int, _ data: Data) -> APIError {
    if status == 402, let shortfall = try? JSONDecoder().decode(InsufficientPointsResponse.self, from: data) {
        return .insufficientPoints(required: shortfall.requiredPoints, balance: shortfall.balance)
    }
    if status == 503, let body = try? JSONDecoder().decode(MaintenanceResponse.self, from: data) {
        // Broadcast so the root view presents a blocking alert no matter which
        // call site hit the paused API, then return the typed error too.
        MaintenanceMonitor.report(body.maintenance)
        return .maintenance(body.maintenance)
    }
    return .http(status, String(decoding: data, as: UTF8.self))
}

/// Talks to the debate-bot engine. Attaches the rxlab bearer token and, on a
/// 401, refreshes once and retries — the pattern RxCode's SecretsService uses.
final class APIClient: Sendable {
    let baseURL: URL
    let tokens: TokenProviding
    let session: URLSession
    static let summaryExportTimeout: TimeInterval = 600


    init(baseURL: URL = AppConfig.apiBaseURL, tokens: TokenProviding, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.tokens = tokens
        self.session = session
    }

}
