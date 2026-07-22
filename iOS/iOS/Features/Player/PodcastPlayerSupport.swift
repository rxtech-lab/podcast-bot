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

struct TranscriptSourcesSelection: Identifiable {
    let id = UUID()
    var sources: [SourceDTO]
}

/// Deterministic per-speaker identity: each panelist gets a stable color and an
/// initials avatar so the transcript reads as a conversation between distinct
/// people instead of a wall of identical grey bubbles.

struct IdentifiableURL: Identifiable {
    let id = UUID()
    let url: URL
}

/// Renders an audiobook's "text-based content" — the book version of the
/// narration with the generated illustrations inline. The body is Markdown
/// (images embedded as `![](url)`), with remote images loaded through
/// Kingfisher so each URL gets its own cache identity.

struct TextContentMarkdownImageProvider: ImageProvider {
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

/// Full-screen player for an audiobook's rendered 1080p video (the illustration
/// slideshow with narration audio + captions). Presented from the context menu's
/// "View Video" action once the post-audio render has finished.
