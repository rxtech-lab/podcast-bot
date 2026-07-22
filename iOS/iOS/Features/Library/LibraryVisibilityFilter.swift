import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

enum LibraryVisibilityFilter: String, CaseIterable, Identifiable {
    case all
    case `public`
    case `private`

    var id: String { rawValue }

    var apiVisibility: DiscussionVisibility? {
        switch self {
        case .all: return nil
        case .public: return .public
        case .private: return .private
        }
    }

    var title: String {
        switch self {
        case .all:
            return String(localized: "All", comment: "Library visibility filter: all stations")
        case .public:
            return String(localized: "Public", comment: "Library visibility filter: public stations")
        case .private:
            return String(localized: "Private", comment: "Library visibility filter: stations")
        }
    }

    var icon: String {
        switch self {
        case .all: return "tray.full"
        case .public: return "globe"
        case .private: return "lock.fill"
        }
    }

    var emptyTitle: String {
        switch self {
        case .all:
            return String(localized: "No \(AppStringLiteral.stationsNameRaw) yet")
        case .public:
            return String(localized: "No Public \(AppStringLiteral.stationsNameRaw)")
        case .private:
            return String(localized: "No Private \(AppStringLiteral.stationsNameRaw)")
        }
    }

    var emptyMessage: String {
        switch self {
        case .all:
            return String(localized: "Plan an AI \(AppStringLiteral.stationNameRaw) and generate the audio.")
        case .public:
            return String(localized: "Published \(AppStringLiteral.stationsNameRaw) will appear here.")
        case .private:
            return String(localized: "Private \(AppStringLiteral.stationsNameRaw) stay visible only to you.")
        }
    }
}
