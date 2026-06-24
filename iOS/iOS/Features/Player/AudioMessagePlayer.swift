import AVFoundation
import SwiftUI

/// Plays a single voice-message audio file from its (signed) URL. One instance per
/// message bubble; intentionally lightweight (no scrubbing, no now-playing info)
/// so it never interferes with the main podcast `PlayerModel`.
@MainActor
@Observable
final class AudioMessagePlayer {
    let urlString: String
    private(set) var isPlaying = false

    private var player: AVPlayer?
    private nonisolated(unsafe) var endObserver: NSObjectProtocol?
    private var reachedEnd = false

    init(urlString: String) {
        self.urlString = urlString
    }

    func toggle() {
        isPlaying ? pause() : play()
    }

    func play() {
        guard let url = URL(string: urlString) else { return }
        if player == nil {
            let player = AVPlayer(url: url)
            self.player = player
            endObserver = NotificationCenter.default.addObserver(
                forName: .AVPlayerItemDidPlayToEndTime,
                object: player.currentItem,
                queue: .main
            ) { [weak self] _ in
                Task { @MainActor in self?.handleEnd() }
            }
        }
        if reachedEnd {
            player?.seek(to: .zero)
            reachedEnd = false
        }
        player?.play()
        isPlaying = true
    }

    func pause() {
        player?.pause()
        isPlaying = false
    }

    private func handleEnd() {
        isPlaying = false
        reachedEnd = true
    }

    deinit {
        if let endObserver {
            NotificationCenter.default.removeObserver(endObserver)
        }
    }
}

/// A compact play/pause + waveform control shown inside a voice-message bubble.
struct VoiceMessageControl: View {
    let urlString: String
    let isUser: Bool

    @State private var player: AudioMessagePlayer?

    private var tint: Color { isUser ? .white : Theme.accent }

    var body: some View {
        Button {
            let player = ensurePlayer()
            player.toggle()
        } label: {
            HStack(spacing: 10) {
                Image(systemName: (player?.isPlaying ?? false) ? "pause.circle.fill" : "play.circle.fill")
                    .font(.title2)
                staticWaveform
                Image(systemName: "waveform")
                    .font(.footnote)
                    .opacity(0.7)
            }
            .foregroundStyle(tint)
        }
        .buttonStyle(.plain)
    }

    private var staticWaveform: some View {
        HStack(spacing: 2) {
            ForEach(0 ..< 16, id: \.self) { index in
                Capsule()
                    .fill(tint.opacity(0.8))
                    .frame(width: 2, height: barHeight(index))
            }
        }
        .frame(height: 22)
    }

    /// Deterministic pseudo-waveform bars so each bubble looks like a voice note.
    private func barHeight(_ index: Int) -> CGFloat {
        let pattern: [CGFloat] = [6, 12, 18, 10, 22, 14, 8, 16, 20, 11, 7, 17, 13, 9, 19, 12]
        return pattern[index % pattern.count]
    }

    private func ensurePlayer() -> AudioMessagePlayer {
        if let player { return player }
        let created = AudioMessagePlayer(urlString: urlString)
        player = created
        return created
    }
}
