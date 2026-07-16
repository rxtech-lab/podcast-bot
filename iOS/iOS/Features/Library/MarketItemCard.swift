import Kingfisher
import SwiftUI

struct MarketItemCard: View {
    let item: MarketDisplayItem
    let onOpen: (MarketDisplayItem) -> Void
    let onToggleLike: (Discussion) -> Void

    var body: some View {
        switch item {
        case .discussion(let discussion):
            VStack(alignment: .leading, spacing: 8) {
                Button { onOpen(item) } label: {
                    StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                        .aspectRatio(1, contentMode: .fit)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.accessibilityIdentifier)
                HStack(alignment: .top) {
                    Button { onOpen(item) } label: {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(discussion.displayTitle)
                                .font(.subheadline.weight(.semibold))
                                .lineLimit(2)
                            MarketStatusLabel(discussion: discussion)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier(item.detailsAccessibilityIdentifier)
                    Spacer(minLength: 8)
                    Button { onToggleLike(discussion) } label: {
                        Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                    }
                    .buttonStyle(.borderless)
                }
            }
        case .album(let summary, _):
            VStack(alignment: .leading, spacing: 8) {
                Button { onOpen(item) } label: {
                    StationCoverArt(cover: summary.cover, title: summary.title)
                        .aspectRatio(1, contentMode: .fit)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.accessibilityIdentifier)
                Button { onOpen(item) } label: {
                    HStack(alignment: .top) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(summary.title)
                                .font(.subheadline.weight(.semibold))
                                .lineLimit(2)
                            Label(albumEpisodeCount(summary), systemImage: "rectangle.stack")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(Theme.secondaryText)
                        }
                        Spacer(minLength: 8)
                        Image(systemName: "rectangle.stack")
                            .foregroundStyle(Theme.accent)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier(item.detailsAccessibilityIdentifier)
            }
        }
    }
}


