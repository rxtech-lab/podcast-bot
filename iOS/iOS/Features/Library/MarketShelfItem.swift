import Kingfisher
import SwiftUI

struct MarketShelfItem: View {
    let item: MarketDisplayItem
    let onOpen: (MarketDisplayItem) -> Void
    let onToggleLike: (Discussion) -> Void

    var body: some View {
        switch item {
        case .discussion(let discussion):
            VStack(alignment: .leading, spacing: 8) {
                Button { onOpen(item) } label: {
                    StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                        .frame(width: 136, height: 136)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.accessibilityIdentifier)
                HStack {
                    Button { onOpen(item) } label: {
                        Text(discussion.displayTitle)
                            .font(.caption.weight(.semibold))
                            .lineLimit(2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier(item.detailsAccessibilityIdentifier)
                    Spacer()
                    Button { onToggleLike(discussion) } label: {
                        Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                            .font(.caption)
                    }
                    .buttonStyle(.borderless)
                }
                .frame(width: 136)
            }
        case .album(let summary, _):
            VStack(alignment: .leading, spacing: 8) {
                Button { onOpen(item) } label: {
                    StationCoverArt(cover: summary.cover, title: summary.title)
                        .frame(width: 136, height: 136)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.accessibilityIdentifier)
                Button { onOpen(item) } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(summary.title)
                            .font(.caption.weight(.semibold))
                            .lineLimit(2)
                        Label(albumEpisodeCount(summary), systemImage: "rectangle.stack")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(Theme.secondaryText)
                    }
                    .frame(width: 136, alignment: .leading)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.detailsAccessibilityIdentifier)
            }
        }
    }
}


