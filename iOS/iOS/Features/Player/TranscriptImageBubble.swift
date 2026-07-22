import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif
import UniformTypeIdentifiers
import os

struct TranscriptImageBubble: View {
    let url: URL
    let line: LiveLine
    let speakerColor: Color
    var onTap: () -> Void = {}
    @State private var didFail = false

    var body: some View {
        Group {
            if didFail {
                transcriptImagePlaceholder(speakerColor: speakerColor)
                    .overlay { Image(systemName: "photo").foregroundStyle(speakerColor) }
            } else {
                KFImage.url(url)
                    .placeholder {
                        transcriptImagePlaceholder(speakerColor: speakerColor)
                            .overlay { ProgressView() }
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .onSuccess { _ in
                        didFail = false
                        transcriptImageLog.info(
                            "Transcript image loaded line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public)"
                        )
                    }
                    .onFailure { error in
                        didFail = true
                        transcriptImageLog.error(
                            "Transcript image failed line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public) error=\(error.localizedDescription, privacy: .public)"
                        )
                    }
                    .resizable()
                    .scaledToFit()
            }
        }
        .onAppear {
            transcriptImageLog.info(
                "Transcript image requested line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public)"
            )
        }
        .onChange(of: url.absoluteString) { _, _ in
            didFail = false
        }
        .frame(maxWidth: 280)
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onTapGesture(perform: onTap)
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("Open image")
    }
}
