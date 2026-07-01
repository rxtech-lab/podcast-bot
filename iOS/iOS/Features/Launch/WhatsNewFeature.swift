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
        WhatsNewFeature(
            id: "notion-planning-conversation",
            title: "Plan with Notion",
            subtitle: "Shape your next \(AppStringLiteral.stationNameRaw) in a planning conversation, with Notion pages and uploads as sources.",
            icon: "bubble.left.and.bubble.right.fill"
        ),
        WhatsNewFeature(
            id: "notion-summary-export",
            title: "Export summaries to Notion",
            subtitle: "You can now export your summary to Notion directly with live podcast playback.",
            icon: "square.and.arrow.up.on.square"
        ),
        WhatsNewFeature(
            id: "template-research-planning",
            title: "Plan with templates",
            subtitle: "Choose templates for new \(AppStringLiteral.stationNameRaw)s, including a research template for academic-style topics.",
            icon: "square.grid.2x2.fill"
        ),
        WhatsNewFeature(
            id: "audio-book-generation",
            title: "Create Audio Books",
            subtitle: "Turn sources into narrated audio books with chapters, voices, illustrations, and a rendered video.",
            icon: "book.closed.fill"
        ),
    ]
}
