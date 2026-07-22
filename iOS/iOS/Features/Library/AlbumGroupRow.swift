import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct AlbumGroupRow: View {
    let summary: AlbumSummaryDTO
    let newest: Discussion
    let episodeCount: Int
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 14) {
            AlbumCoverThumbnail(cover: summary.cover, size: 40)
            VStack(alignment: .leading, spacing: 4) {
                Text(summary.title.isEmpty ? newest.displayTitle : summary.title)
                    .font(.headline)
                    .lineLimit(2)
                HStack(spacing: 8) {
                    Label("\(episodeCount) episode\(episodeCount == 1 ? "" : "s")",
                          systemImage: "rectangle.stack")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer()
            Image(systemName: "chevron.right").foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(tint: isSelected ? Theme.accent.opacity(0.55) : nil)
    }
}
