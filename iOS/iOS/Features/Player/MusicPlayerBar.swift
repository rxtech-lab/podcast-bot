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

struct MusicPlayerBar: View {
    @Bindable var model: PlayerModel
    var onExpand: () -> Void = {}

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Button(action: model.togglePlay) {
                    Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                        .font(.headline)
                        .foregroundStyle(.primary)
                        .frame(width: 38, height: 38)
                        .glassEffect(in: .circle)
                }

                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(headerLine)
                            .font(.subheadline.weight(.semibold))
                            .lineLimit(1)
                        Spacer(minLength: 0)
                        Image(systemName: "chevron.up")
                            .font(.caption2.weight(.bold))
                            .foregroundStyle(Theme.secondaryText)
                    }
                    if !model.caption.isEmpty {
                        Text(model.caption)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(1)
                    }
                }
                .contentShape(.rect)
                .onTapGesture(perform: onExpand)
                .accessibilityIdentifier("player.expand")

                if model.canDownloadPodcast {
                    Button {
                        model.downloadPodcast()
                    } label: {
                        Image(systemName: "arrow.down.circle")
                            .font(.body)
                            .foregroundStyle(Theme.accent)
                    }
                    .disabled(model.isDownloadingPodcast)
                }
            }
            .padding(.horizontal, 12)
            .padding(.top, 10)
            .padding(.bottom, 8)

            thinProgressBar
                .padding(.horizontal, 12)
                .padding(.bottom, 10)
        }
        .glassEffect(in: .rect(cornerRadius: 18))
    }

    private var thinProgressBar: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule()
                    .fill(Color.primary.opacity(0.12))
                    .frame(height: 3)
                Capsule()
                    .fill(Theme.accent)
                    .frame(width: max(0, geo.size.width * progress), height: 3)
            }
        }
        .frame(height: 3)
    }

    private var titleLine: String {
        if !model.currentAudioBookChapterTitle.isEmpty { return model.currentAudioBookChapterTitle }
        if !model.phaseLabel.isEmpty { return model.phaseLabel }
        if !model.statusText.isEmpty { return model.statusText }
        return model.discussion.displayTitle
    }

    private var headerLine: String {
        guard !model.captionSpeaker.isEmpty else { return titleLine }
        return "\(titleLine) · \(model.captionSpeaker)"
    }

    private var progress: Double {
        guard progressDuration > 0 else { return 0 }
        return min(1, progressTime / progressDuration)
    }

    private var progressTime: Double {
        if model.duration > 0 { return model.currentTime }
        return max(model.currentTime, model.elapsedTime)
    }

    private var progressDuration: Double {
        if model.duration > 0 { return model.duration }
        let estimatedTotal = model.elapsedTime + model.remainingTime
        return estimatedTotal > 0 ? estimatedTotal : 0
    }
}
