import Kingfisher
import SwiftUI

struct StationCoverArt: View {
    let cover: DiscussionCover?
    let title: String

    var body: some View {
        ZStack {
            if let url = cover?.renderableImageURL {
                KFImage.url(url)
                    .placeholder {
                        gradient
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFill()
            } else {
                gradient
            }
            if showsTitleOverlay {
                VStack {
                    Spacer()
                    Text(title)
                        .font(.caption.weight(.bold))
                        .foregroundStyle(.white)
                        .lineLimit(3)
                        .multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                        .background(.black.opacity(0.22))
                }
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .contentShape(.rect(cornerRadius: 8))
    }

    private var showsTitleOverlay: Bool {
        cover?.renderableImageURL == nil
    }

    private var gradient: some View {
        LinearGradient(
            colors: [
                Color(hex: cover?.gradientStart ?? "#8E5CF7"),
                Color(hex: cover?.gradientEnd ?? "#00A3FF"),
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }
}


