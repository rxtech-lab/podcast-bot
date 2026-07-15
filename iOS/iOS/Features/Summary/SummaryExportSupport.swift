import AuthenticationServices
import BeautifulMermaid
import Kingfisher
import MarkdownUI
import QuickLook
import SwiftUI
import TipKit
import os

struct KingfisherMarkdownImageProvider: ImageProvider {
    func makeImage(url: URL?) -> some View {
        Group {
            if let url {
                KFImage.url(url)
                    .placeholder {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                            .frame(height: 160)
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .fade(duration: 0.15)
                    .resizable()
                    .scaledToFit()
                    .id(url.absoluteString)
            } else {
                Color.clear
                    .frame(width: 0, height: 0)
            }
        }
    }
}

/// A summary export (PDF or Markdown) sitting in a temp file, ready to hand to
/// the system share sheet.


struct ExportedSummaryFile: Identifiable {
    let id = UUID()
    let url: URL
}


