import Kingfisher
import SwiftUI

struct CreatorProfileRow: View {
    let profile: CreatorProfile

    var body: some View {
        HStack(spacing: 12) {
            CreatorAvatar(profile: profile, size: 46)
            VStack(alignment: .leading, spacing: 3) {
                Text(profile.title)
                    .font(.subheadline.weight(.semibold))
                    .lineLimit(1)
                Text(profile.followerText)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(1)
            }
            Spacer()
            Image(systemName: "chevron.right")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
        }
        .padding(12)
        .background(.thinMaterial, in: .rect(cornerRadius: 8))
    }
}


