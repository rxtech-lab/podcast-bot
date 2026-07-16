import AVFoundation
import Observation
import SwiftUI

@MainActor
@Observable
final class TranscriptClipPlayer {
    private let api: APIClient
    private let discussionID: String
    private var localAudioURL: URL?
    private var player: AVPlayer?
    private var endMonitorTask: Task<Void, Never>?
    private var reachedEnd = false

    private(set) var activeIndex: Int?
    private(set) var loadingIndex: Int?
    private(set) var isPlaying = false

    init(api: APIClient, discussionID: String) {
        self.api = api
        self.discussionID = discussionID
    }

    func toggle(index: Int, segment: TranscriptSegmentDTO) async throws {
        if activeIndex == index, let player {
            if isPlaying {
                player.pause()
                isPlaying = false
                return
            }
            if reachedEnd {
                await seek(player, to: segment.offsetMs)
                reachedEnd = false
                monitorPlaybackEnd(index: index, endMs: segment.offsetMs + segment.durationMs)
            }
            player.play()
            isPlaying = true
            return
        }

        loadingIndex = index
        defer { loadingIndex = nil }
        let url: URL
        if let localAudioURL {
            url = localAudioURL
        } else {
            url = try await api.cachedUploadedAudioURL(id: discussionID)
            localAudioURL = url
        }

        stop(clearActiveIndex: false)
        let item = AVPlayerItem(url: url)
        item.forwardPlaybackEndTime = CMTime(
            value: segment.offsetMs + segment.durationMs,
            timescale: 1000
        )
        let player = AVPlayer(playerItem: item)
        self.player = player
        activeIndex = index
        reachedEnd = false
        await seek(player, to: segment.offsetMs)
        monitorPlaybackEnd(index: index, endMs: segment.offsetMs + segment.durationMs)
        player.play()
        isPlaying = true
    }

    func stop(clearActiveIndex: Bool = true) {
        player?.pause()
        player = nil
        endMonitorTask?.cancel()
        endMonitorTask = nil
        isPlaying = false
        reachedEnd = false
        if clearActiveIndex { activeIndex = nil }
    }

    private func seek(_ player: AVPlayer, to milliseconds: Int64) async {
        await player.seek(
            to: CMTime(value: milliseconds, timescale: 1000),
            toleranceBefore: .zero,
            toleranceAfter: .zero
        )
    }

    private func monitorPlaybackEnd(index: Int, endMs: Int64) {
        endMonitorTask?.cancel()
        endMonitorTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                guard !Task.isCancelled, let self,
                      self.activeIndex == index,
                      let seconds = self.player?.currentTime().seconds,
                      seconds.isFinite else { continue }
                if seconds * 1000 >= Double(max(endMs - 25, 0)) {
                    self.isPlaying = false
                    self.reachedEnd = true
                    return
                }
            }
        }
    }
}
