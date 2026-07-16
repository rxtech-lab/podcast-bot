import Kingfisher
import SwiftUI

struct CreatorAvatar: View {
    let profile: CreatorProfile
    let size: CGFloat

    var body: some View {
        ZStack {
            Circle()
                .fill(Theme.accent.opacity(0.18))
            if let avatar = profile.avatarURL,
               let url = URL(string: avatar) {
                KFImage.url(url)
                    .placeholder {
                        Image(systemName: "person.fill")
                            .font(.system(size: size * 0.42, weight: .semibold))
                            .foregroundStyle(Theme.accent)
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFill()
            } else {
                Image(systemName: "person.fill")
                    .font(.system(size: size * 0.42, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
        }
        .frame(width: size, height: size)
        .clipShape(Circle())
    }
}


