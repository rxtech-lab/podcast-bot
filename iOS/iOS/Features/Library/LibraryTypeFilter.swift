import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit

enum LibraryTypeFilter: String, CaseIterable, Identifiable {
    case all
    case discussion
    case audioBook = "audio-book"
    case news

    var id: String { rawValue }

    var apiType: String? {
        switch self {
        case .all: return nil
        case .discussion, .audioBook, .news: return rawValue
        }
    }

    var icon: String {
        switch self {
        case .all: return "square.grid.2x2"
        case .discussion: return "person.2.wave.2"
        case .audioBook: return "book.closed"
        case .news: return "newspaper"
        }
    }

    var emptyTitle: String {
        switch self {
        case .all:
            return String(localized: "No \(AppStringLiteral.stationsNameRaw) yet")
        case .discussion:
            return String(localized: "No Discussion \(AppStringLiteral.stationsNameRaw)")
        case .audioBook:
            return String(localized: "No Audio Book \(AppStringLiteral.stationsNameRaw)")
        case .news:
            return String(localized: "No News \(AppStringLiteral.stationsNameRaw)")
        }
    }

    var emptyMessage: String {
        switch self {
        case .all:
            return String(localized: "Plan an AI \(AppStringLiteral.stationNameRaw) and generate the audio.")
        case .discussion:
            return String(localized: "Discussion \(AppStringLiteral.stationsNameRaw) will appear here.")
        case .audioBook:
            return String(localized: "Audio book \(AppStringLiteral.stationsNameRaw) will appear here.")
        case .news:
            return String(localized: "News \(AppStringLiteral.stationsNameRaw) will appear here.")
        }
    }
}
