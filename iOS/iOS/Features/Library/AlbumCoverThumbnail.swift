import Kingfisher
import SwiftUI

struct AlbumCoverThumbnail: View {
    let cover: DiscussionCover?
    let size: CGFloat

    var body: some View {
        Group {
            if let url = cover?.renderableImageURL {
                KFImage.url(url)
                    .placeholder {
                        fallback
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFill()
            } else if let cover, cover.hasGradient {
                LinearGradient(colors: [color(cover.gradientStart), color(cover.gradientEnd)],
                               startPoint: .topLeading,
                               endPoint: .bottomTrailing)
            } else {
                fallback
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: size > 100 ? 16 : 8))
    }

    private var fallback: some View {
        ZStack {
            LinearGradient(colors: [Theme.accent.opacity(0.75), Color.orange.opacity(0.72)],
                           startPoint: .topLeading,
                           endPoint: .bottomTrailing)
            Image(systemName: "rectangle.stack")
                .font(.system(size: size * 0.34, weight: .semibold))
                .foregroundStyle(.white)
        }
    }

    private func color(_ hex: String?) -> Color {
        guard let hex else { return Theme.accent }
        let trimmed = hex.trimmingCharacters(in: CharacterSet(charactersIn: "# "))
        guard trimmed.count == 6, let value = Int(trimmed, radix: 16) else {
            return Theme.accent
        }
        let red = Double((value >> 16) & 0xff) / 255.0
        let green = Double((value >> 8) & 0xff) / 255.0
        let blue = Double(value & 0xff) / 255.0
        return Color(red: red, green: green, blue: blue)
    }
}


