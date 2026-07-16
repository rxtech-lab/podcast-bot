import Kingfisher
import SwiftUI

struct MarketFeaturedItem: View {
    let item: MarketDisplayItem
    let onOpen: (MarketDisplayItem) -> Void
    let onToggleLike: (Discussion) -> Void

    var body: some View {
        Button { onOpen(item) } label: {
            HStack(spacing: 16) {
                switch item {
                case .discussion(let discussion):
                    StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                        .frame(width: 118, height: 118)
                    VStack(alignment: .leading, spacing: 8) {
                        Text(discussion.displayTitle)
                            .font(.title3.weight(.semibold))
                            .lineLimit(2)
                        MarketStatusLabel(discussion: discussion)
                        HStack {
                            Label("\(discussion.likeCount ?? 0)", systemImage: "heart")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(Theme.secondaryText)
                            Spacer()
                            Button { onToggleLike(discussion) } label: {
                                Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                            }
                            .buttonStyle(.borderless)
                        }
                    }
                case .album(let summary, _):
                    StationCoverArt(cover: summary.cover, title: summary.title)
                        .frame(width: 118, height: 118)
                    VStack(alignment: .leading, spacing: 8) {
                        Text(summary.title)
                            .font(.title3.weight(.semibold))
                            .lineLimit(2)
                        Label(albumEpisodeCount(summary), systemImage: "rectangle.stack")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(Theme.secondaryText)
                        Spacer(minLength: 0)
                        HStack {
                            Spacer()
                            Image(systemName: "chevron.right")
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .glassCard()
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(item.accessibilityIdentifier)
    }
}
