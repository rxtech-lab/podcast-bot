import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit

struct DiscussionRow: View {
    let discussion: Discussion
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 14) {
            leading
                .frame(width: 40)
            VStack(alignment: .leading, spacing: 4) {
                Text(discussion.displayTitle)
                    .font(.headline)
                    .lineLimit(2)
                HStack(spacing: 8) {
                    Text(statusLabel)
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                    VisibilityBadge(isPublic: discussion.isPublic)
                }
            }
            Spacer()
            Image(systemName: "chevron.right").foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(tint: isSelected ? Theme.accent.opacity(0.55) : nil)
    }

    /// Cover thumbnail when the discussion has cover art, otherwise the
    /// status icon. Keeps the row compact while surfacing covers in the library.
    @ViewBuilder
    var leading: some View {
        if let cover = discussion.cover, cover.hasImage || cover.hasGradient {
            coverThumbnail(cover)
                .frame(width: 40, height: 40)
                .clipShape(.rect(cornerRadius: 8))
        } else {
            Image(systemName: icon)
                .font(.title2)
                .foregroundStyle(Theme.accent)
        }
    }

    @ViewBuilder
    func coverThumbnail(_ cover: DiscussionCover) -> some View {
        if let urlString = cover.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !urlString.isEmpty, let url = URL(string: urlString) {
            KFImage.url(url)
                .placeholder {
                    coverGradient(cover)
                }
                .cancelOnDisappear(false)
                .retry(maxCount: 3, interval: .seconds(1))
                .resizable()
                .scaledToFill()
        } else {
            coverGradient(cover)
        }
    }

    func coverGradient(_ cover: DiscussionCover) -> some View {
        LinearGradient(
            colors: [
                Color(hex: cover.gradientStart ?? "#8E5CF7"),
                Color(hex: cover.gradientEnd ?? "#00A3FF"),
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }

    var icon: String {
        switch discussion.status {
        case .planning: return "pencil.and.list.clipboard"
        case .generating: return "waveform"
        case .ready: return "play.circle.fill"
        case .failed: return "exclamationmark.triangle"
        }
    }

    var statusLabel: String {
        switch discussion.status {
        case .planning:
            let peopleCount = discussion.sortedPeople.count
            if peopleCount > 0 {
                return String(localized: "Plan - \(peopleCount) people",
                              comment: "Discussion row status: planning, with the panelist count")
            }
            return String(localized: "Plan", comment: "Discussion row status: planning without loaded plan details")
        case .generating:
            return String(localized: "Generating...", comment: "Discussion row status: podcast is generating")
        case .ready:
            return String(localized: "Ready to play", comment: "Discussion row status: podcast is ready")
        case .failed:
            return String(localized: "Failed", comment: "Discussion row status: generation failed")
        }
    }
}
