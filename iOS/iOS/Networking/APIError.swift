import Foundation

enum APIError: Error, LocalizedError {
    case notAuthenticated
    case invalidRequest(String)
    case http(Int, String)
    /// HTTP 402: the user doesn't hold enough points to start this action. Carries
    /// the points required and the user's current balance so the UI can open the
    /// paywall with context.
    case insufficientPoints(required: Int, balance: Int)
    /// HTTP 409 on join: the discussion already has the maximum number of
    /// participants, so this user can't join.
    case participantCapReached
    /// HTTP 503 with a maintenance body: the app is paused for a scheduled
    /// maintenance window. Carries the operator's message so the UI can show it.
    case maintenance(MaintenanceInfo)
    case decoding(String)

    var errorDescription: String? {
        switch self {
        case .notAuthenticated:
            return String(localized: "You're signed out. Please sign in again.",
                          comment: "Error shown when the user's session has expired")
        case let .invalidRequest(msg): return msg
        case let .http(code, msg):
            return String(localized: "Request failed (\(code)): \(msg)",
                          comment: "Generic HTTP error; code is the status code, msg is the server message")
        case let .insufficientPoints(required, balance):
            return String(localized: "You need \(required) points but have \(balance). Top up to continue.",
                          comment: "Error shown when the user lacks enough points to start an action")
        case .participantCapReached:
            return String(localized: "This discussion is full. Ask the host to remove someone or try again later.",
                          comment: "Error shown when a discussion has reached its participant limit")
        case let .maintenance(info):
            return info.displayMessage
        case let .decoding(msg):
            return String(localized: "Couldn't read the server response: \(msg)",
                          comment: "Error shown when the server response could not be decoded")
        }
    }
}
