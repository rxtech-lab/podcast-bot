import Kingfisher
import SwiftUI

enum MarketDisplayItem: Identifiable, Hashable {
    case discussion(Discussion)
    case album(summary: AlbumSummaryDTO, representative: Discussion)

    var id: String {
        switch self {
        case .discussion(let discussion):
            return "discussion:\(discussion.id)"
        case .album(let summary, _):
            return "album:\(summary.id)"
        }
    }

    var destination: MarketDestination {
        switch self {
        case .discussion(let discussion):
            return .discussion(discussion)
        case .album(let summary, _):
            return .album(id: summary.id)
        }
    }
}

func marketDisplayItems(from stations: [Discussion]) -> [MarketDisplayItem] {
    var seenAlbums = Set<String>()
    var items: [MarketDisplayItem] = []
    for station in stations {
        if let summary = station.album, !summary.id.isEmpty {
            guard seenAlbums.insert(summary.id).inserted else { continue }
            items.append(.album(summary: summary, representative: station))
        } else {
            items.append(.discussion(station))
        }
    }
    return items
}


extension MarketDisplayItem {
    var accessibilityIdentifier: String {
        switch self {
        case .discussion(let discussion):
            return "market.station.\(discussion.id)"
        case .album(let summary, _):
            return "market.album.\(summary.id)"
        }
    }

    var detailsAccessibilityIdentifier: String {
        "\(accessibilityIdentifier).details"
    }
}

func albumEpisodeCount(_ summary: AlbumSummaryDTO) -> String {
    let count = summary.episodeCount ?? 0
    return "\(count) episode\(count == 1 ? "" : "s")"
}

