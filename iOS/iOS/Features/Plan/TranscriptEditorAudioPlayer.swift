import AVFoundation
import Observation
import SwiftUI

@MainActor
@Observable
final class TranscriptEditorAudioPlayer {
    private let api: APIClient
    private let discussionID: String
    private let audioDurationMs: Int64
    private var player: AVPlayer?
    private var localAudioURL: URL?
    private var timeObserver: Any?
    private var endObserver: NSObjectProtocol?
    private var reachedEnd = false

    private(set) var currentTimestampMs: Int64
    private(set) var isLoading = false
    private(set) var isPlaying = false

    var playbackTimestampMs: Int64 {
        // Register the observable timestamp even when AVPlayer is available.
        // Its periodic observer drives SwiftUI updates without a TimelineView
        // continuously rebuilding the surrounding accessibility hierarchy.
        let observedTimestampMs = currentTimestampMs
        guard let player,
              let timestampMs = transcriptPlaybackTimestamp(
                  player.currentTime(),
                  maximumMs: audioDurationMs
              ) else {
            return observedTimestampMs
        }
        return timestampMs
    }

    init(api: APIClient,
         discussionID: String,
         initialTimestampMs: Int64,
         audioDurationMs: Int64) {
        self.api = api
        self.discussionID = discussionID
        self.currentTimestampMs = max(initialTimestampMs, 0)
        self.audioDurationMs = audioDurationMs
    }

    func togglePlayback() async throws {
        // Hermetic E2E runs never touch AVFoundation: on headless CI hosts the
        // simulator's media services can block the main thread once a player
        // exists, wedging the whole UI. Timing is simulated instead — the
        // E2E tests drive current time through deterministic seeks anyway.
        if AppConfig.isE2E {
            if !isPlaying, reachedEnd || (audioDurationMs > 0 && currentTimestampMs >= audioDurationMs - 25) {
                currentTimestampMs = 0
                reachedEnd = false
            }
            isPlaying.toggle()
            return
        }
        let player = try await loadPlayerIfNeeded()
        if isPlaying {
            player.pause()
            isPlaying = false
            return
        }
        if reachedEnd || (audioDurationMs > 0 && currentTimestampMs >= audioDurationMs - 25) {
            await seekPlayer(player, to: 0)
            currentTimestampMs = 0
            reachedEnd = false
        }
        player.play()
        isPlaying = true
    }

    func seek(to timestampMs: Int64) async throws {
        if AppConfig.isE2E {
            currentTimestampMs = clampedTimestamp(timestampMs)
            return
        }
        let player = try await loadPlayerIfNeeded()
        let clamped = clampedTimestamp(timestampMs)
        await seekPlayer(player, to: clamped)
        currentTimestampMs = clamped
    }

    func stop() {
        player?.pause()
        stopMonitoring()
        player = nil
        isPlaying = false
        reachedEnd = false
    }

    private func loadPlayerIfNeeded() async throws -> AVPlayer {
        if let player { return player }
        isLoading = true
        defer { isLoading = false }
        let url: URL
        if let localAudioURL {
            url = localAudioURL
        } else {
            url = try await api.cachedUploadedAudioURL(id: discussionID)
            localAudioURL = url
        }
        let player = AVPlayer(url: url)
        self.player = player
        startMonitoring(player)
        await seekPlayer(player, to: currentTimestampMs)
        return player
    }

    private func seekPlayer(_ player: AVPlayer, to timestampMs: Int64) async {
        await player.seek(
            to: CMTime(value: timestampMs, timescale: 1_000),
            toleranceBefore: .zero,
            toleranceAfter: .zero
        )
    }

    private func clampedTimestamp(_ timestampMs: Int64) -> Int64 {
        guard audioDurationMs > 0 else { return max(timestampMs, 0) }
        return min(max(timestampMs, 0), audioDurationMs)
    }

    private func startMonitoring(_ player: AVPlayer) {
        stopMonitoring()
        let interval = CMTime(value: 100, timescale: 1_000)
        timeObserver = player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { [weak self] time in
            guard let self, let timestamp = transcriptPlaybackTimestamp(
                time,
                maximumMs: self.audioDurationMs
            ) else { return }
            self.currentTimestampMs = timestamp
        }
        endObserver = NotificationCenter.default.addObserver(
            forName: .AVPlayerItemDidPlayToEndTime,
            object: player.currentItem,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                guard let self else { return }
                self.reachedEnd = true
                self.isPlaying = false
            }
        }
    }

    private func stopMonitoring() {
        if let timeObserver, let player {
            player.removeTimeObserver(timeObserver)
        }
        timeObserver = nil
        if let endObserver {
            NotificationCenter.default.removeObserver(endObserver)
        }
        endObserver = nil
    }
}

func transcriptPlaybackTimestamp(_ time: CMTime, maximumMs: Int64) -> Int64? {
    let seconds = time.seconds
    guard seconds.isFinite else { return nil }
    let timestampMs = max(Int64((seconds * 1_000).rounded()), 0)
    guard maximumMs > 0 else { return timestampMs }
    return min(timestampMs, maximumMs)
}

func transcriptTimestamp(_ milliseconds: Int64,
                                 includesMilliseconds: Bool = false) -> String {
    let clamped = max(milliseconds, 0)
    let totalSeconds = clamped / 1000
    let hours = totalSeconds / 3600
    let minutes = (totalSeconds % 3600) / 60
    let seconds = totalSeconds % 60
    let fraction = clamped % 1000
    if hours > 0 {
        return includesMilliseconds
            ? String(format: "%d:%02d:%02d.%03d", hours, minutes, seconds, fraction)
            : String(format: "%d:%02d:%02d", hours, minutes, seconds)
    }
    return includesMilliseconds
        ? String(format: "%d:%02d.%03d", minutes, seconds, fraction)
        : String(format: "%d:%02d", minutes, seconds)
}

private func parseTranscriptTimestamp(_ raw: String) -> Int64? {
    let parts = raw.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: ":")
    guard (1 ... 3).contains(parts.count) else { return nil }
    let values = parts.compactMap { Double($0) }
    guard values.count == parts.count, values.allSatisfy({ $0 >= 0 }) else { return nil }

    let seconds: Double
    switch values.count {
    case 1:
        seconds = values[0]
    case 2:
        guard values[1] < 60 else { return nil }
        seconds = values[0] * 60 + values[1]
    case 3:
        guard values[1] < 60, values[2] < 60 else { return nil }
        seconds = values[0] * 3600 + values[1] * 60 + values[2]
    default:
        return nil
    }
    guard seconds.isFinite, seconds <= Double(Int64.max) / 1000 else { return nil }
    return Int64((seconds * 1000).rounded())
}
