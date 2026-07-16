import Kingfisher
import SwiftUI

struct MarketProfileHeader: View {
    let profile: CreatorProfile

    var body: some View {
        HStack(spacing: 16) {
            CreatorAvatar(profile: profile, size: 74)
            VStack(alignment: .leading, spacing: 6) {
                Text(profile.title)
                    .font(.title2.weight(.semibold))
                    .lineLimit(2)
                if !profile.subtitle.isEmpty {
                    Text(profile.subtitle)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard()
    }
}


