import Foundation

/// A live private share link, parsed from `ShareLinkDTO` for display in the
/// share sheet (expiry countdown, revoke).
struct DiscussionShareLink: Identifiable, Sendable, Hashable {
    let token: String
    let url: URL
    let createdAt: Date
    let expiresAt: Date

    var id: String { token }

    init(token: String, url: URL, createdAt: Date, expiresAt: Date) {
        self.token = token
        self.url = url
        self.createdAt = createdAt
        self.expiresAt = expiresAt
    }

    init?(dto: ShareLinkDTO) {
        guard let url = URL(string: dto.url) else { return nil }
        self.token = dto.token
        self.url = url
        self.createdAt = DiscussionShareLink.parseDate(dto.createdAt) ?? Date()
        self.expiresAt = DiscussionShareLink.parseDate(dto.expiresAt) ?? Date()
    }

    /// RFC3339 parser tolerant of fractional seconds (Go's time.Time default).
    static func parseDate(_ s: String) -> Date? {
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = withFraction.date(from: s) { return d }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: s)
    }
}

/// Preset share durations the bottom sheet offers, capped at 72 hours.
enum ShareDuration: Int, CaseIterable, Identifiable, Sendable {
    case oneHour = 1
    case twoHours = 2
    case sixHours = 6
    case twelveHours = 12
    case oneDay = 24
    case twoDays = 48
    case threeDays = 72

    var id: Int { rawValue }
    var seconds: Int { rawValue * 3600 }

    var label: String {
        switch self {
        case .oneHour: return String(localized: "1 hour", comment: "Share-link duration option")
        case .twoHours: return String(localized: "2 hours", comment: "Share-link duration option")
        case .sixHours: return String(localized: "6 hours", comment: "Share-link duration option")
        case .twelveHours: return String(localized: "12 hours", comment: "Share-link duration option")
        case .oneDay: return String(localized: "1 day", comment: "Share-link duration option")
        case .twoDays: return String(localized: "2 days", comment: "Share-link duration option")
        case .threeDays: return String(localized: "3 days", comment: "Share-link duration option")
        }
    }

    /// Compact label for the segmented picker.
    var shortLabel: String {
        switch self {
        case .oneHour: return "1h"
        case .twoHours: return "2h"
        case .sixHours: return "6h"
        case .twelveHours: return "12h"
        case .oneDay: return "1d"
        case .twoDays: return "2d"
        case .threeDays: return "3d"
        }
    }
}

/// A deep link parsed from an incoming universal link / custom URL.
enum DeepLink: Equatable, Sendable {
    case publicDiscussion(id: String)
    case sharedDiscussion(token: String)

    /// Parses `https://podcast.rxlab.app/d/{id}` and `…/s/{token}` (and the
    /// matching `debatepod://` custom-scheme forms) into a DeepLink.
    init?(url: URL) {
        let comps = url.pathComponents.filter { $0 != "/" }
        // Custom scheme like debatepod://d/{id} puts the kind in host.
        if url.scheme == "debatepod" {
            if url.host == "d", let id = comps.first { self = .publicDiscussion(id: id); return }
            if url.host == "s", let token = comps.first { self = .sharedDiscussion(token: token); return }
        }
        guard comps.count >= 2 else { return nil }
        switch comps[0] {
        case "d": self = .publicDiscussion(id: comps[1])
        case "s": self = .sharedDiscussion(token: comps[1])
        default: return nil
        }
    }
}
