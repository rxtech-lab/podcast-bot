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

struct TranscriptImageFullScreenView: View {
    @Environment(\.dismiss) private var dismiss
    let url: URL
    @State private var didFail = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Color.black.ignoresSafeArea()

            Group {
                if didFail {
                    Image(systemName: "photo")
                        .font(.system(size: 52, weight: .semibold))
                        .foregroundStyle(.white.opacity(0.75))
                } else {
                    KFImage.url(url)
                        .placeholder {
                            ProgressView()
                                .tint(.white)
                        }
                        .cancelOnDisappear(false)
                        .retry(maxCount: 3, interval: .seconds(1))
                        .onSuccess { _ in
                            didFail = false
                        }
                        .onFailure { error in
                            didFail = true
                            transcriptImageLog.error(
                                "Transcript image fullscreen failed url=\(redactedURLDescription(url), privacy: .public) error=\(error.localizedDescription, privacy: .public)"
                            )
                        }
                        .resizable()
                        .scaledToFit()
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, maxHeight: .infinity)

            Button {
                dismiss()
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.system(size: 32, weight: .semibold))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Close image")
            .padding(20)
        }
        .presentationBackground(.black)
    }
}

func transcriptImagePlaceholder(speakerColor: Color) -> some View {
    RoundedRectangle(cornerRadius: 12)
        .fill(speakerColor.opacity(0.12))
        .frame(height: 160)
}

func redactedURLDescription(_ url: URL) -> String {
    let components = URLComponents(url: url, resolvingAgainstBaseURL: false)
    let queryNames = components?.queryItems?
        .map(\.name)
        .sorted()
        .joined(separator: ",") ?? ""
    let base = "\(url.scheme ?? "unknown")://\(url.host ?? "no-host")\(url.path)"
    if queryNames.isEmpty {
        return base
    }
    return "\(base)?[\(queryNames)]"
}

/// Trailing accessory row showing only the points this podcast consumed. The
/// detailed token/cost breakdown is intentionally hidden from users; the server
/// sends only the points total.
