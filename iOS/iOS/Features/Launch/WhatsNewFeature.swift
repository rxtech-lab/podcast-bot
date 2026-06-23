import SwiftUI

/// A single "What's New" card. Each feature has a **stable** `id` (slug): the
/// device stores the set of seen ids, so "new" = `all` minus seen. Changing a
/// slug re-shows the card; appending a new entry surfaces it to existing users.
struct WhatsNewFeature: Identifiable, Equatable {
    let id: String
    let title: LocalizedStringKey
    let subtitle: LocalizedStringKey
    let icon: String // SF Symbol

    static func == (lhs: WhatsNewFeature, rhs: WhatsNewFeature) -> Bool {
        lhs.id == rhs.id
    }

    /// All shipped cards, oldest first. Append new cards with a fresh slug.
    static let all: [WhatsNewFeature] = [
        WhatsNewFeature(
            id: "discussion-search",
            title: "Search your \(AppStringLiteral.stationTitleRaw)",
            subtitle: "Find any \(AppStringLiteral.stationNameRaw) instantly with live, server-side search in your library.",
            icon: "magnifyingglass"
        ),
        WhatsNewFeature(
            id: "points-history",
            title: "Track your points",
            subtitle: "See exactly how your points are spent with a full usage history.",
            icon: "chart.bar.fill"
        ),
        WhatsNewFeature(
            id: "chinese-localization",
            title: "中文支持",
            subtitle: "The app now speaks Simplified and Traditional Chinese, end to end.",
            icon: "globe.asia.australia.fill"
        ),
    ]
}
