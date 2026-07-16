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

struct TranscriptBubble: View {
    let line: LiveLine
    /// True only when this line was authored by the current participant.
    let isMine: Bool
    let speakerColor: Color
    var onSourcesTapped: ([SourceDTO]) -> Void = { _ in }
    var onImageTapped: (URL) -> Void = { _ in }

    private var sources: [SourceDTO] { line.sources ?? [] }
    private var judgementComment: String {
        line.judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    init(line: LiveLine,
         isMine: Bool,
         speakerColor: Color? = nil,
         onSourcesTapped: @escaping ([SourceDTO]) -> Void = { _ in },
         onImageTapped: @escaping (URL) -> Void = { _ in }) {
        self.line = line
        self.isMine = isMine
        self.speakerColor = speakerColor ?? SpeakerPalette.color(for: line.speaker)
        self.onSourcesTapped = onSourcesTapped
        self.onImageTapped = onImageTapped
    }

    var body: some View {
        Group {
            if line.hasRenderablePayload {
                HStack(alignment: .top, spacing: 8) {
                    if isMine { Spacer(minLength: 40) }
                    if !isMine {
                        SpeakerAvatar(speaker: line.speaker, color: speakerColor)
                    }
                    VStack(alignment: isMine ? .trailing : .leading, spacing: 4) {
                        if !isMine {
                            HStack(spacing: 6) {
                                Text(line.speaker.uppercased())
                                    .font(.caption2.weight(.bold))
                                    .foregroundStyle(speakerColor)
                                // Tag a human participant's turn so a co-listener's comment
                                // is not mistaken for an AI panelist's line.
                                if line.isUser {
                                    Text("USER", comment: "Badge marking a transcript line as written by a human participant, not an AI panelist")
                                        .font(.system(size: 9, weight: .bold))
                                        .foregroundStyle(speakerColor)
                                        .padding(.horizontal, 5)
                                        .padding(.vertical, 1)
                                        .overlay {
                                            Capsule().strokeBorder(speakerColor.opacity(0.5), lineWidth: 0.5)
                                        }
                                }
                            }
                        }
                        VStack(alignment: isMine ? .trailing : .leading, spacing: 8) {
                            if let audioURL = line.audioURL, !audioURL.isEmpty {
                                VoiceMessageControl(urlString: audioURL, isUser: isMine)
                            }
                            if line.hasImage {
                                let urlStr = line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                                if let url = URL(string: urlStr) {
                                    TranscriptImageBubble(url: url, line: line, speakerColor: speakerColor) {
                                        onImageTapped(url)
                                    }
                                } else {
                                    transcriptImagePlaceholder(speakerColor: speakerColor)
                                        .onAppear {
                                            transcriptImageLog.error(
                                                "Transcript image URL invalid line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) rawLength=\(urlStr.count, privacy: .public)"
                                            )
                                        }
                                }
                            }
                            if line.hasDisplayText {
                                bubbleText
                            } else if line.hasAudio {
                                Text("Audio message", comment: "Fallback label for a voice message whose transcript is unavailable")
                                    .font(.caption.weight(.medium))
                                    .foregroundStyle((isMine ? Color.white : speakerColor).opacity(0.78))
                            }
                            if !sources.isEmpty {
                                Button {
                                    onSourcesTapped(sources)
                                } label: {
                                    Label("Sources", systemImage: "link")
                                        .font(.caption.weight(.semibold))
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .tint(isMine ? .white.opacity(0.9) : speakerColor)
                            }
                            if !judgementComment.isEmpty {
                                HStack(alignment: .top, spacing: 6) {
                                    Image(systemName: "exclamationmark.triangle.fill")
                                        .font(.caption2.weight(.bold))
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text("Judgement")
                                            .font(.caption2.weight(.bold))
                                            .textCase(.uppercase)
                                        Text(judgementComment)
                                            .font(.caption)
                                    }
                                }
                                .foregroundStyle(isMine ? Color.white.opacity(0.82) : Color.orange)
                                .padding(.top, 2)
                            }
                        }
                        .font(.body)
                        .padding(12)
                        .background(bubbleStyle, in: .rect(cornerRadius: 18))
                        .overlay {
                            RoundedRectangle(cornerRadius: 18)
                                .strokeBorder(isMine ? .clear : speakerColor.opacity(0.28),
                                              lineWidth: 0.5)
                        }
                        .foregroundStyle(isMine ? .white : .primary)
                    }
                    if !isMine { Spacer(minLength: 40) }
                }
            }
        }
    }

    /// My bubbles get an accent gradient for depth; everyone else takes a soft
    /// tint of their speaker color so each speaker's turns are recognizable.
    private var bubbleStyle: AnyShapeStyle {
        if isMine {
            AnyShapeStyle(LinearGradient(
                colors: [Theme.accent, Theme.accent.opacity(0.82)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            ))
        } else {
            AnyShapeStyle(speakerColor.opacity(0.14))
        }
    }

    /// Human messages (mine or another participant's) are plain typed text, so we
    /// render them with `Text`, which hugs its content — otherwise `MarkdownText`'s
    /// block layout greedily fills the row and leaves the bubble far wider than the
    /// message. Only agent lines actually contain markdown.
    @ViewBuilder
    private var bubbleText: some View {
        if line.isUser {
            Text(line.displayText)
        } else {
            MarkdownText(line.displayText)
        }
    }
}
