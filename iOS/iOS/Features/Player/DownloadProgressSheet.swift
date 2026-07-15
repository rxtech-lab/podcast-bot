import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

struct DownloadProgressSheet: View {
    @Bindable var model: PlayerModel

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: model.downloadErrorText == nil ? "arrow.down.circle.fill" : "exclamationmark.triangle.fill")
                .font(.system(size: 44, weight: .semibold))
                .foregroundStyle(model.downloadErrorText == nil ? Theme.accent : .orange)
            Text(model.downloadErrorText == nil ? "Downloading \(AppStringLiteral.stationNameRaw)" : "Download Failed")
                .font(.headline)
            if model.isDownloadingPodcast && model.downloadProgress <= 0 {
                ProgressView()
                    .tint(Theme.accent)
            } else {
                ProgressView(value: model.downloadProgress)
                    .tint(Theme.accent)
            }
            Text("\(Int(model.downloadProgress * 100))%")
                .font(.caption)
                .foregroundStyle(Theme.secondaryText)
            if let error = model.downloadErrorText {
                Text(error)
                    .font(.footnote)
                    .multilineTextAlignment(.center)
                    .foregroundStyle(Theme.secondaryText)
                Button("Close") {
                    model.showsDownloadDialog = false
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding(24)
        .presentationDetents([.height(model.downloadErrorText == nil ? 220 : 320)])
        .interactiveDismissDisabled(model.isDownloadingPodcast)
    }
}
