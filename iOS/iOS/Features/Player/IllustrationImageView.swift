import Kingfisher
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct IllustrationImageView<Placeholder: View>: View {
    let url: URL
    let prefetchURL: URL?
    @ViewBuilder var placeholder: () -> Placeholder

    @State private var current: LoadedIllustration?

    private struct LoadedIllustration: Equatable {
        let url: URL
        let image: Image

        static func == (lhs: LoadedIllustration, rhs: LoadedIllustration) -> Bool {
            lhs.url == rhs.url
        }
    }

    var body: some View {
        ZStack {
            if let current {
                current.image.resizable().scaledToFill()
            } else {
                placeholder()
            }
        }
        .task(id: url) {
            if current?.url != url,
               let image = await IllustrationImageLoader.shared.load(url),
               !Task.isCancelled {
                withTransaction(Transaction(animation: nil)) {
                    current = LoadedIllustration(url: url, image: image)
                }
            }
            if let prefetchURL {
                await IllustrationImageLoader.shared.prefetch(prefetchURL)
            }
        }
    }
}

/// Small in-memory cache for player artwork illustrations, so hard cuts land
/// instantly and scrubbing back re-shows earlier images without a re-fetch.


