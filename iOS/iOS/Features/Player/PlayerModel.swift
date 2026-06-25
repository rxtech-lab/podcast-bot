import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

private let playerLog = Logger(subsystem: "com.debatebot.ios", category: "PlayerModel")

/// One transcript bubble shown in the player. While an utterance streams it is
/// updated in place; when `done` it is finalized (and persisted).
struct LiveLine: Identifiable, Equatable {
    let id = UUID()
    var speaker: String
    var role: String
    var text: String
    var isUser: Bool
    var done: Bool
    /// Server-owned id of the human who authored this line. Used to distinguish the
    /// current user's own messages from other participants' (both are `isUser`).
    /// Nil for agent lines and legacy rows without a persisted sender.
    var senderUserID: String? = nil
    /// Playback URL when this line is a voice message; nil for text-only lines.
    var audioURL: String? = nil

    var hasAudio: Bool {
        !(audioURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
    }

    var hasDisplayText: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    @discardableResult
    static func applyTranscriptEvent(to lines: inout [LiveLine],
                                     speaker: String,
                                     role: String,
                                     text: String,
                                     done: Bool) -> LiveLine? {
        let chunk = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if let idx = lines.lastIndex(where: { $0.speaker == speaker && !$0.done && !$0.isUser }) {
            if !chunk.isEmpty {
                if lines[idx].text.isEmpty {
                    lines[idx].text = chunk
                } else {
                    lines[idx].text += " " + chunk
                }
            }
            if done {
                lines[idx].done = true
                return lines[idx]
            }
            return nil
        }

        guard !chunk.isEmpty else { return nil }
        let line = LiveLine(speaker: speaker, role: role, text: chunk, isUser: false, done: done)
        lines.append(line)
        return done ? line : nil
    }
}

struct TranscriptScrollToken: Equatable {
    var count: Int
    var lastID: UUID?
    var lastText: String
    var lastDone: Bool

    static func make(for lines: [LiveLine]) -> TranscriptScrollToken {
        guard let last = lines.last else {
            return TranscriptScrollToken(count: 0, lastID: nil, lastText: "", lastDone: false)
        }
        return TranscriptScrollToken(count: lines.count,
                                     lastID: last.id,
                                     lastText: last.text,
                                     lastDone: last.done)
    }
}

struct VTTCue: Equatable {
    var start: Double
    var end: Double
    var text: String
}

struct LyricCueGroup: Identifiable, Equatable {
    let id: Int
    var start: Double
    var end: Double
    var text: String
    var firstCueIndex: Int
    var lastCueIndex: Int
    var lineCount: Int
    var runeCount: Int
    var speaker: String
}

struct DownloadedPodcastFile: Identifiable, Equatable {
    let id = UUID()
    let url: URL
}

/// Drives the live podcast experience: streams the audio (live HLS while
/// generating, final MP3 when ready), surfaces the per-agent transcript over the
/// job WebSocket, polls live captions, and tracks job completion.
@MainActor
@Observable
final class PlayerModel {
    // The backend now emits zero-bias VTT for audio-only feeds (cues align with
    // the untrimmed recording), so no manual lead is needed. A non-zero value
    // here would make live captions appear early.
    nonisolated static let liveCaptionLeadSeconds = 0.0
    nonisolated private static let lyricGroupMinRunes = 28
    nonisolated private static let lyricGroupMaxRunes = 96
    nonisolated private static let lyricGroupMaxLines = 3
    nonisolated private static let lyricGroupMaxGapSeconds = 1.25
    nonisolated private static let lyricGroupMaxDurationSeconds = 12.0
    nonisolated static let minimumTranscriptLoadingSeconds = 1.0

    var discussion: Discussion
    /// Exposed (read-only use) so views like the share sheet can reuse the same
    /// authenticated client instead of constructing another.
    let api: APIClient
    private let username: String
    /// The current participant's authenticated id. Exposed so the transcript can
    /// reliably tell *this* user's own messages apart from other participants' —
    /// both persist with `isUser == true`, but each user line now carries a
    /// server-owned `senderUserID`. Empty only when signed out / unknown.
    let currentUserID: String
    /// The current participant's display name. Retained as a legacy fallback for
    /// lines persisted before `senderUserID` existed (no sender id to compare).
    var currentUsername: String { username }
    /// Set when this discussion was opened via a private share link; authorizes
    /// a non-owner participant's comments on the backend.
    private let shareToken: String?

    /// Two prominent colors derived from the cover (gradient hexes, or extracted
    /// from the cover image) used to tint the full-screen player background.
    var coverColors: [Color] = []
    private var coverColorsSourceKey: String?

    let player = AVPlayer()
    var isPlaying = false
    var currentTime: Double = 0
    var duration: Double = 0
    var elapsedTime: Double = 0
    var remainingTime: Double = 0
    var caption: String = ""
    var captionSpeaker: String = ""
    var phaseLabel: String = ""
    var statusText: String = ""
    var isForceStopping = false
    var isFinished = false
    var downloadURL: URL?
    var isDownloadingPodcast = false
    var downloadProgress = 0.0
    var downloadErrorText: String?
    var showsDownloadDialog = false
    var downloadedPodcastFile: DownloadedPodcastFile?
    var isTranscriptLoading = false
    var lines: [LiveLine] = []
    var transcriptScrollToken: TranscriptScrollToken {
        TranscriptScrollToken.make(for: lines)
    }
    var canForceStop: Bool {
        discussion.isOwner == true && discussion.status == .generating && !isFinished && !isForceStopping && discussion.jobID != nil
    }
    var showsForceStopAction: Bool {
        discussion.isOwner == true && discussion.status == .generating && !isFinished && discussion.jobID != nil
    }
    var isReadyForDownload: Bool {
        discussion.status == .ready || (isFinished && downloadURL != nil)
    }
    var canDownloadPodcast: Bool {
        isReadyForDownload && (downloadURL != nil || discussion.jobID != nil)
    }
    var canSendMessages: Bool {
        !isFinished && discussion.canSendMessages
    }
    var showsPodcastActions: Bool {
        showsForceStopAction || canDownloadPodcast
    }
    var canSeek: Bool {
        !usesLiveCaptionTiming && duration > 0
    }

    private var socket: JobSocket?
    private var isStartingJobSocket = false
    private var timeObserver: Any?
    private var itemStatusObservation: NSKeyValueObservation?
    private var playbackRetryTask: Task<Void, Never>?
    private var playbackJobID: String?
    private var playbackRetryCount = 0
    private var remoteCommandTargets: [Any] = []
    private nonisolated(unsafe) var audioInterruptionObserver: NSObjectProtocol?
    private var isAudioSessionInterrupted = false
    private var shouldResumeAfterAudioInterruption = false
    private var nowPlayingArtwork: MPMediaItemArtwork?
    private var nowPlayingArtworkSourceKey: String?
    private var usesLiveCaptionTiming = false
    private var autoplayRequested = false
    /// Whether playback should auto-start once the item is ready. Captured at
    /// `start()` from the entry status: a podcast that was already ready when
    /// the user opened it stays paused; a live/generating entry autoplays even
    /// if the job finishes (and final audio is installed) mid-setup or on retry.
    private var autoplayOnEntry = true
    /// A seek to apply once the current item reaches `.readyToPlay`. Seeking a
    /// freshly-replaced item before it loads is dropped (or lands past the end),
    /// which is exactly what wedged the swap-to-final-audio playback.
    private var pendingResumeTime: Double?
    /// Set once the final (seekable) audio item has been installed, so the live
    /// `setupPlayer` task can't clobber it with a now-dead HLS item if the job
    /// finishes during one of its `await` suspensions.
    private var finalAudioInstalled = false
    private(set) var cues: [VTTCue] = []
    private var tasks: [Task<Void, Never>] = []
    private var transcriptLoadingStartedAt: Date?
    private var transcriptLoadingHideTask: Task<Void, Never>?

    /// Lyrics mode is available once we have final (seekable) caption timing and
    /// at least one cue. While streaming we only surface the current caption.
    var supportsLyrics: Bool { !usesLiveCaptionTiming && !cues.isEmpty }

    // Memoized lyric groups. Grouping is O(cues × lines) — every cue scans the
    // transcript for its speaker — so recomputing it on each access made the
    // full-screen lyrics list lag with a large script (the list body and the
    // 4 Hz `activeLyricGroupID` update both read it). Lyrics mode only runs on
    // final, stable captions, so we rebuild only when the cue/line counts move.
    @ObservationIgnored private var lyricGroupsCache: [LyricCueGroup] = []
    @ObservationIgnored private var lyricGroupsCacheKey = (-1, -1)

    /// Final, seekable caption list grouped for the lyrics view. Live captions
    /// still read the raw cue list so streaming timing stays one cue at a time.
    var lyricCueGroups: [LyricCueGroup] {
        let key = (cues.count, lines.count)
        if key != lyricGroupsCacheKey {
            lyricGroupsCache = Self.groupLyricCues(cues) { [lines] cue in
                Self.captionSpeaker(for: cue.text, in: lines) ?? ""
            }
            lyricGroupsCacheKey = key
        }
        return lyricGroupsCache
    }

    /// Index of the cue at the current playback time, used to highlight and
    /// auto-scroll the lyrics list. Falls back to the last cue already passed.
    var activeCueIndex: Int? {
        let t = captionLookupTime(playbackTime: currentTime)
        return cues.firstIndex(where: { t >= $0.start && t <= $0.end })
            ?? cues.lastIndex(where: { $0.start <= t })
    }

    /// Id of the lyric group at the current playback time. Stored (not computed)
    /// and republished only when it actually changes, via `updateActiveLyricGroup`
    /// — so the lyrics list re-renders on a boundary crossing (a few times a
    /// minute) rather than on every 4 Hz time tick, which is what made scrolling
    /// a large script feel laggy.
    private(set) var activeLyricGroupID: Int?

    /// Recomputes the active lyric group at `time` and publishes it only when it
    /// changes. Cheap: `lyricCueGroups` is memoized, so this is an O(groups) scan.
    private func updateActiveLyricGroup(at time: Double) {
        guard supportsLyrics else {
            if activeLyricGroupID != nil { activeLyricGroupID = nil }
            return
        }
        let groups = lyricCueGroups
        let t = captionLookupTime(playbackTime: time)
        let index = groups.firstIndex(where: { t >= $0.start && t <= $0.end })
            ?? groups.lastIndex(where: { $0.start <= t })
        let id = index.map { groups[$0].id }
        if id != activeLyricGroupID { activeLyricGroupID = id }
    }

    /// Speaker label for a cue, reusing the transcript-matching heuristic.
    func speaker(for cue: VTTCue) -> String {
        Self.captionSpeaker(for: cue.text, in: lines) ?? ""
    }

    func speaker(for group: LyricCueGroup) -> String {
        if !group.speaker.isEmpty { return group.speaker }
        guard group.firstCueIndex <= group.lastCueIndex else { return "" }
        for index in group.firstCueIndex...group.lastCueIndex where cues.indices.contains(index) {
            if let speaker = Self.captionSpeaker(for: cues[index].text, in: lines) {
                return speaker
            }
        }
        return Self.captionSpeaker(for: group.text, in: lines) ?? ""
    }

    init(discussion: Discussion, api: APIClient, username: String, userID: String = "",
         shareToken: String? = nil) {
        self.discussion = discussion
        self.api = api
        self.username = username
        self.currentUserID = userID
        self.shareToken = shareToken
    }

    func start() {
        configureAudioSession()
        // Decide autoplay from the entry status only — not from which asset
        // `setupPlayer` ends up installing. A generating entry that flips to
        // `.ready` mid-setup still autoplays its final audio.
        autoplayOnEntry = !(discussion.status == .ready || isFinished)
        // Replay persisted transcript for a finished discussion.
        lines = discussion.sortedLines.map {
            LiveLine(speaker: $0.speaker, role: $0.role, text: $0.text, isUser: $0.isUser, done: true,
                     senderUserID: $0.senderUserID, audioURL: $0.audioURL)
        }
        if discussion.jobID != nil && !hasPodcastTranscript {
            showTranscriptLoadingIfNeeded()
        }
        if let s = discussion.downloadURLString { downloadURL = URL(string: s) }
        // Pick up cover art that may have been generated in the background after
        // the library handed us this discussion (e.g. the new-discussion toggle).
        tasks.append(Task { await self.refreshCover() })
        guard let jobID = discussion.jobID else { return }
        listenForJobUpdatesIfNeeded()

        configureRemoteCommands()
        updateNowPlayingInfo()
        // Load the persisted detail (with re-signed voice-message URLs) BEFORE the
        // text-only job-transcript snapshot, so voice lines exist with their audio
        // and the snapshot won't re-persist them as plain text.
        tasks.append(Task {
            await self.hydratePersistedLines()
            await self.loadTranscriptSnapshot(jobID: jobID)
        })
        tasks.append(Task { await setupPlayer(jobID: jobID) })
        if discussion.status == .generating {
            tasks.append(Task { await pollCaptions(jobID: jobID) })
            tasks.append(Task { await pollStatus(jobID: jobID) })
        } else {
            tasks.append(Task { await loadFinalCaptions(jobID: jobID) })
        }
    }

    func stop() {
        if let timeObserver { player.removeTimeObserver(timeObserver) }
        timeObserver = nil
        itemStatusObservation?.invalidate()
        itemStatusObservation = nil
        playbackRetryTask?.cancel()
        playbackRetryTask = nil
        forceHideTranscriptLoading()
        socket?.close()
        socket = nil
        isStartingJobSocket = false
        tasks.forEach { $0.cancel() }
        tasks.removeAll()
        player.pause()
        removeRemoteCommands()
        removeAudioSessionObservers()
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
        MPNowPlayingInfoCenter.default().playbackState = .stopped
    }

    func togglePlay() {
        if isPlaying {
            pause()
        } else {
            resumePlayback()
        }
    }

    /// User/remote-initiated play. Unlike the bare `play()`, this re-runs the
    /// readiness-gated `beginPlayback` path so tapping play can dislodge a player
    /// that got wedged in `.waitingToPlayAtSpecifiedRate` — previously the only
    /// recovery was to leave and re-open the discussion.
    private func resumePlayback() {
        guard activateAudioSessionForManualPlayback() else {
            isPlaying = false
            updateNowPlayingInfo()
            return
        }
        guard let item = player.currentItem else {
            playerLog.debug("resumePlayback: no current item, falling back to play()")
            play()
            return
        }
        playerLog.debug("resumePlayback: status=\(item.status.rawValue, privacy: .public) timeControl=\(self.player.timeControlStatus.rawValue, privacy: .public) interrupted=\(self.isAudioSessionInterrupted, privacy: .public)")
        beginPlayback(item: item)
    }

    /// Sends a user message. When `audioURL`/`audioKey` are set the message is a
    /// voice message: `text` is its on-device transcript (what the agent reads),
    /// while the audio is persisted so other participants can replay it.
    func send(_ text: String, audioURL: String? = nil, audioKey: String? = nil) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // A voice message carries audio, so it is allowed to send even when its
        // transcript is empty (no on-device model and the cloud transcription
        // failed): the audio is still replayable. Text-only messages stay gated.
        let isVoice = audioURL != nil || audioKey != nil
        guard canSendMessages, !trimmed.isEmpty || isVoice else { return }
        let line = LiveLine(speaker: username, role: "user", text: trimmed, isUser: true, done: true,
                            senderUserID: currentUserID.isEmpty ? nil : currentUserID, audioURL: audioURL)
        lines.append(line)
        let jobID = discussion.jobID
        persistIfNeeded(line: line, syncRemote: jobID == nil, audioURL: audioURL, audioKey: audioKey)
        guard let jobID else { return }
        Task {
            do {
                try await api.sendJobMessage(id: jobID,
                                            text: trimmed,
                                            username: username,
                                            discussionID: discussion.id,
                                            shareToken: shareToken,
                                            audioURL: audioURL,
                                            audioKey: audioKey)
            } catch APIError.http(429, _) {
                removeRejectedUserLine(line)
            } catch APIError.http(403, _) {
                removeRejectedUserLine(line)
            } catch {
                do {
                    try await api.appendDiscussionLine(
                        id: discussion.id,
                        line: DiscussionLineRequest(speaker: username,
                                                    role: "user",
                                                    side: nil,
                                                    text: trimmed,
                                                    startMS: 0,
                                                    isUser: true,
                                                    audioURL: audioURL,
                                                    audioKey: audioKey),
                        shareToken: shareToken
                    )
                } catch APIError.http(403, _) {
                    removeRejectedUserLine(line)
                } catch {
                }
            }
        }
    }

    private func removeRejectedUserLine(_ line: LiveLine) {
        lines.removeAll { $0.id == line.id }
        guard var cachedLines = discussion.lines,
              let index = cachedLines.lastIndex(where: {
                  $0.speaker == line.speaker &&
                      $0.role == line.role &&
                      $0.text == line.text &&
                      $0.isUser == line.isUser
              }) else { return }
        cachedLines.remove(at: index)
        discussion.lines = cachedLines
    }

    func forceStop() {
        guard canForceStop, let jobID = discussion.jobID else { return }
        isForceStopping = true
        statusText = String(localized: "Stopping and finalising upload...",
                            comment: "Status while a generating podcast is being force-stopped")
        updateNowPlayingInfo()
        Task {
            do {
                try await api.forceStopJob(id: jobID)
            } catch {
                isForceStopping = false
                statusText = String(localized: "Stop failed: \(error.localizedDescription)",
                                    comment: "Status when force-stopping a podcast failed; includes the error detail")
                updateNowPlayingInfo()
            }
        }
    }

    func downloadPodcast() {
        guard canDownloadPodcast, !isDownloadingPodcast else { return }
        isDownloadingPodcast = true
        downloadProgress = 0
        downloadErrorText = nil
        showsDownloadDialog = true

        Task {
            do {
                let file = try await api.downloadPodcastAudio(
                    sourceURL: downloadURL,
                    jobID: discussion.jobID,
                    title: discussion.displayTitle
                ) { [weak self] progress in
                    Task { @MainActor in
                        self?.downloadProgress = progress
                    }
                }
                isDownloadingPodcast = false
                downloadProgress = 1
                showsDownloadDialog = false
                try? await Task.sleep(for: .milliseconds(250))
                downloadedPodcastFile = DownloadedPodcastFile(url: file)
            } catch {
                isDownloadingPodcast = false
                downloadErrorText = error.localizedDescription
                showsDownloadDialog = true
            }
        }
    }

    // MARK: - Playback

    private func configureAudioSession() {
        #if os(iOS)
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
        try? AVAudioSession.sharedInstance().setActive(true)
        configureAudioSessionObservers()
        #endif
    }

    private func activateAudioSessionForManualPlayback() -> Bool {
        #if os(iOS)
        do {
            try AVAudioSession.sharedInstance().setActive(true)
            if isAudioSessionInterrupted {
                playerLog.debug("resumePlayback: clearing stale audio interruption suppression")
            }
            isAudioSessionInterrupted = false
            shouldResumeAfterAudioInterruption = false
            return true
        } catch {
            playerLog.error("resumePlayback: failed to activate audio session: \(error.localizedDescription, privacy: .public)")
            return false
        }
        #else
        return true
        #endif
    }

    private func configureAudioSessionObservers() {
        guard audioInterruptionObserver == nil else { return }
        audioInterruptionObserver = NotificationCenter.default.addObserver(
            forName: AVAudioSession.interruptionNotification,
            object: AVAudioSession.sharedInstance(),
            queue: .main
        ) { [weak self] notification in
            Task { @MainActor in
                self?.handleAudioSessionInterruption(notification)
            }
        }
    }

    private func removeAudioSessionObservers() {
        if let audioInterruptionObserver {
            NotificationCenter.default.removeObserver(audioInterruptionObserver)
            self.audioInterruptionObserver = nil
        }
        isAudioSessionInterrupted = false
        shouldResumeAfterAudioInterruption = false
    }

    private func handleAudioSessionInterruption(_ notification: Notification) {
        guard let rawType = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
              let type = AVAudioSession.InterruptionType(rawValue: rawType) else { return }
        switch type {
        case .began:
            suppressPlaybackForAudioInterruption()
        case .ended:
            let shouldResume = Self.audioInterruptionShouldResume(notification.userInfo)
            try? AVAudioSession.sharedInstance().setActive(true)
            isAudioSessionInterrupted = false
            guard shouldResumeAfterAudioInterruption, shouldResume else {
                shouldResumeAfterAudioInterruption = false
                updateNowPlayingInfo()
                return
            }
            shouldResumeAfterAudioInterruption = false
            if let item = player.currentItem {
                beginPlayback(item: item)
            } else {
                play()
            }
        @unknown default:
            break
        }
    }

    private func suppressPlaybackForAudioInterruption() {
        shouldResumeAfterAudioInterruption = isPlaying || autoplayRequested
        isAudioSessionInterrupted = true
        autoplayRequested = false
        isPlaying = false
        playbackRetryTask?.cancel()
        playbackRetryTask = nil
        player.pause()
        updateNowPlayingInfo()
    }

    nonisolated static func audioInterruptionShouldResume(_ userInfo: [AnyHashable: Any]?) -> Bool {
        guard let rawOptions = userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt else { return false }
        return AVAudioSession.InterruptionOptions(rawValue: rawOptions).contains(.shouldResume)
    }

    private func configureRemoteCommands() {
        guard remoteCommandTargets.isEmpty else { return }

        let commandCenter = MPRemoteCommandCenter.shared()
        commandCenter.playCommand.isEnabled = true
        commandCenter.pauseCommand.isEnabled = true
        commandCenter.togglePlayPauseCommand.isEnabled = true
        commandCenter.changePlaybackPositionCommand.isEnabled = true

        remoteCommandTargets.append(commandCenter.playCommand.addTarget { [weak self] _ in
            Task { @MainActor in self?.resumePlayback() }
            return .success
        })
        remoteCommandTargets.append(commandCenter.pauseCommand.addTarget { [weak self] _ in
            Task { @MainActor in self?.pause() }
            return .success
        })
        remoteCommandTargets.append(commandCenter.togglePlayPauseCommand.addTarget { [weak self] _ in
            Task { @MainActor in self?.togglePlay() }
            return .success
        })
        remoteCommandTargets.append(commandCenter.changePlaybackPositionCommand.addTarget { [weak self] event in
            guard let event = event as? MPChangePlaybackPositionCommandEvent else {
                return .commandFailed
            }
            Task { @MainActor in self?.seek(to: event.positionTime) }
            return .success
        })
    }

    private func removeRemoteCommands() {
        guard !remoteCommandTargets.isEmpty else { return }

        let commandCenter = MPRemoteCommandCenter.shared()
        for target in remoteCommandTargets {
            commandCenter.playCommand.removeTarget(target)
            commandCenter.pauseCommand.removeTarget(target)
            commandCenter.togglePlayPauseCommand.removeTarget(target)
            commandCenter.changePlaybackPositionCommand.removeTarget(target)
        }
        remoteCommandTargets.removeAll()
    }

    private func play() {
        guard !isAudioSessionInterrupted else {
            isPlaying = false
            updateNowPlayingInfo()
            return
        }
        autoplayRequested = true
        player.play()
        isPlaying = true
        updateNowPlayingInfo()
    }

    private func pause() {
        autoplayRequested = false
        player.pause()
        isPlaying = false
        updateNowPlayingInfo()
    }

    func seek(to seconds: Double) {
        guard seconds.isFinite, seconds >= 0 else { return }
        let time = CMTime(seconds: seconds, preferredTimescale: 600)
        player.seek(to: time)
        currentTime = seconds
        updateCaption(at: seconds)
        updateNowPlayingInfo()
    }

    func skipForward(_ s: Double = 15) {
        let target = currentTime + s
        seek(to: duration > 0 ? min(target, duration) : target)
    }

    func skipBackward(_ s: Double = 15) {
        seek(to: max(currentTime - s, 0))
    }

    private func setupPlayer(jobID: String, retryingPlayback: Bool = false) async {
        playbackJobID = jobID
        if !retryingPlayback {
            playbackRetryCount = 0
        }
        if discussion.status != .ready {
            while !Task.isCancelled {
                if await api.hlsPlaylistReady(jobID: jobID) { break }
                if isFinished || discussion.status == .ready { break }
                if statusText.isEmpty {
                    statusText = String(localized: "Preparing live audio...",
                                        comment: "Status while waiting for the live audio stream to become ready")
                }
                try? await Task.sleep(for: .seconds(1))
            }
        }
        guard !Task.isCancelled else { return }

        let useFinalAudio = isFinished || discussion.status == .ready
        usesLiveCaptionTiming = !useFinalAudio
        let url = useFinalAudio ? api.finalAudioURL(jobID: jobID) : api.hlsURL(jobID: jobID)
        var options: [String: Any] = [:]
        var headers = ["Accept-Language": AcceptLanguage.headerValue]
        if let token = await api.currentToken() {
            headers["Authorization"] = "Bearer \(token)"
        }
        options["AVURLAssetHTTPHeaderFieldsKey"] = headers
        // If the job finished during the awaits above, `switchToFinalAudioIfNeeded`
        // may have already installed the final item — don't overwrite it with a
        // now-dead HLS stream.
        if finalAudioInstalled && !useFinalAudio {
            playerLog.debug("setupPlayer: final audio already installed, skipping HLS install")
            return
        }
        if useFinalAudio { finalAudioInstalled = true }
        playerLog.debug("setupPlayer: installing \(useFinalAudio ? "final" : "live HLS", privacy: .public) item url=\(url.absoluteString, privacy: .public)")
        playbackRetryTask?.cancel()
        playbackRetryTask = nil
        let asset = AVURLAsset(url: url, options: options)
        let item = AVPlayerItem(asset: asset)
        item.preferredForwardBufferDuration = useFinalAudio ? 0 : 4
        player.automaticallyWaitsToMinimizeStalling = true
        player.replaceCurrentItem(with: item)

        if let timeObserver { player.removeTimeObserver(timeObserver) }
        let interval = CMTime(seconds: 0.25, preferredTimescale: 600)
        timeObserver = player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { [weak self] t in
            guard let self else { return }
            let secs = t.seconds
            self.currentTime = secs.isFinite ? secs : 0
            if let dur = self.player.currentItem?.duration.seconds, dur.isFinite, dur > 0 {
                self.duration = dur
            }
            self.updateCaption(at: self.currentTime)
            self.updateNowPlayingInfo()
        }
        // A podcast that was already ready on entry is prepared but left paused;
        // live entries autoplay even if final audio got installed mid-setup.
        // `useFinalAudio` only selects the URL/timing, never the autoplay intent.
        beginPlayback(item: item, autoplay: autoplayOnEntry)
    }

    /// Start playback for a freshly-installed item, but only once it actually
    /// reaches `.readyToPlay`. Calling `play()` immediately after
    /// `replaceCurrentItem` (before the live-HLS item has loaded) leaves the
    /// player wedged: the job-progress bar still advances from "tick" events so
    /// it *looks* like it's playing, but no audio renders until the player is
    /// kicked. Gating on `status` plus a `timeControlStatus` confirm loop makes
    /// the first play deterministic, so the user no longer has to leave and
    /// re-open the discussion to get sound.
    private func beginPlayback(item: AVPlayerItem, autoplay: Bool = true) {
        itemStatusObservation?.invalidate()
        itemStatusObservation = nil
        guard !isAudioSessionInterrupted else {
            autoplayRequested = false
            isPlaying = false
            player.pause()
            updateNowPlayingInfo()
            return
        }
        autoplayRequested = autoplay

        if item.status == .readyToPlay {
            playerLog.debug("beginPlayback: item already ready, autoplay=\(autoplay, privacy: .public)")
            playbackRetryCount = 0
            applyPendingResume(on: item)
            if autoplayRequested { startConfirmedPlayback() }
            return
        }
        if item.status == .failed {
            handlePlaybackItemFailed(item)
            return
        }
        playerLog.debug("beginPlayback: waiting for item to become ready (status=\(item.status.rawValue, privacy: .public))")
        itemStatusObservation = item.observe(\.status, options: [.new]) { [weak self] obsItem, _ in
            Task { @MainActor in
                guard let self else { return }
                switch obsItem.status {
                case .readyToPlay:
                    guard self.player.currentItem === obsItem else { return }
                    self.itemStatusObservation?.invalidate()
                    self.itemStatusObservation = nil
                    self.playbackRetryCount = 0
                    playerLog.debug("beginPlayback: item became ready, autoplay=\(self.autoplayRequested, privacy: .public)")
                    self.applyPendingResume(on: obsItem)
                    guard self.autoplayRequested else { return }
                    self.startConfirmedPlayback()
                case .failed:
                    self.handlePlaybackItemFailed(obsItem)
                default:
                    break
                }
            }
        }
    }

    private func handlePlaybackItemFailed(_ item: AVPlayerItem) {
        guard player.currentItem === item else { return }
        itemStatusObservation?.invalidate()
        itemStatusObservation = nil
        let message = item.error?.localizedDescription ?? "unknown"
        playerLog.error("beginPlayback: item failed: \(message, privacy: .public)")
        schedulePlaybackRetry()
    }

    private func schedulePlaybackRetry() {
        guard autoplayRequested, let jobID = playbackJobID else { return }
        guard !isAudioSessionInterrupted else { return }
        guard playbackRetryTask == nil else { return }
        guard playbackRetryCount < 8 else {
            statusText = String(localized: "Audio stream is still warming up. Try again in a moment.",
                                comment: "Status after playback retries are exhausted")
            updateNowPlayingInfo()
            return
        }

        let retry = playbackRetryCount
        playbackRetryCount += 1
        let delay = min(5.0, 0.75 * Double(retry + 1))
        playerLog.debug("beginPlayback: scheduling item reinstall in \(delay, privacy: .public)s (attempt \(retry + 1, privacy: .public))")
        let retryTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                self?.playbackRetryTask = nil
            }
            await self?.setupPlayer(jobID: jobID, retryingPlayback: true)
        }
        playbackRetryTask = retryTask
        tasks.append(retryTask)
    }

    /// Apply a deferred resume seek now that the item is ready, clamped to the
    /// final duration so a live-edge position can't land us past the end (which
    /// would render silence and look like a wedged player).
    private func applyPendingResume(on item: AVPlayerItem) {
        guard let resume = pendingResumeTime, resume > 0 else { return }
        pendingResumeTime = nil
        let dur = item.duration.seconds
        let target = (dur.isFinite && dur > 0) ? min(resume, max(dur - 1, 0)) : resume
        playerLog.debug("applyPendingResume: seeking to \(target, privacy: .public) (requested \(resume, privacy: .public), dur \(dur, privacy: .public))")
        player.seek(to: CMTime(seconds: target, preferredTimescale: 600))
        currentTime = target
    }

    private func startConfirmedPlayback() {
        play()
        tasks.append(Task { await confirmPlayback() })
    }

    /// Confirms playback truly started, kicking the player (pause→play — the
    /// same recovery the user does by hand) if it stalls instead of rendering.
    /// "Started" means `timeControlStatus == .playing`; a bare repeated
    /// `play()` does nothing to a player stuck in `.waitingToPlayAtSpecifiedRate`,
    /// so we cycle it. Bounded so a genuinely failed stream eventually gives up.
    private func confirmPlayback() async {
        for attempt in 0..<8 {
            guard !Task.isCancelled, autoplayRequested, !isAudioSessionInterrupted else { return }
            try? await Task.sleep(for: .seconds(attempt == 0 ? 0.4 : 1.0))
            guard !Task.isCancelled, autoplayRequested, !isAudioSessionInterrupted else { return }
            if player.timeControlStatus == .playing {
                playerLog.debug("confirmPlayback: playing after \(attempt, privacy: .public) attempt(s)")
                isPlaying = true
                updateNowPlayingInfo()
                return
            }
            // Not rendering yet: cycle the player. pause()/play() on the raw
            // AVPlayer (not our pause(), which would clear autoplayRequested)
            // dislodges the wedged-after-premature-play state.
            playerLog.debug("confirmPlayback: kicking player (attempt \(attempt, privacy: .public), timeControl=\(self.player.timeControlStatus.rawValue, privacy: .public))")
            player.pause()
            player.play()
            isPlaying = true
            updateNowPlayingInfo()
        }
    }


    private func updateCaption(at time: Double) {
        if let cue = Self.captionCue(in: cues, at: captionLookupTime(playbackTime: time)) {
            caption = cue.text
            captionSpeaker = Self.captionSpeaker(for: cue.text, in: lines) ?? ""
        } else {
            caption = ""
            captionSpeaker = ""
        }
        updateActiveLyricGroup(at: time)
        updateNowPlayingInfo()
    }

    private func captionLookupTime(playbackTime: Double) -> Double {
        Self.captionLookupTime(playbackTime: playbackTime, usesLiveCaptionTiming: usesLiveCaptionTiming)
    }

    nonisolated static func captionLookupTime(playbackTime: Double, usesLiveCaptionTiming: Bool) -> Double {
        usesLiveCaptionTiming ? playbackTime + liveCaptionLeadSeconds : playbackTime
    }

    nonisolated static func captionText(in cues: [VTTCue], at time: Double) -> String {
        captionCue(in: cues, at: time)?.text ?? ""
    }

    nonisolated static func captionCue(in cues: [VTTCue], at time: Double) -> VTTCue? {
        cues.first(where: { time >= $0.start && time <= $0.end })
    }

    nonisolated static func groupLyricCues(_ cues: [VTTCue],
                                           speakerForCue: ((VTTCue) -> String)? = nil) -> [LyricCueGroup] {
        var groups: [LyricCueGroup] = []
        for (index, cue) in cues.enumerated() {
            let text = cue.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { continue }
            let speaker = speakerForCue?(cue) ?? ""

            if let last = groups.last,
               shouldMergeLyricCue(last, with: cue, text: text, speaker: speaker) {
                var merged = last
                merged.end = max(merged.end, cue.end)
                merged.text += "\n" + text
                merged.lastCueIndex = index
                merged.lineCount += 1
                merged.runeCount += text.count
                if merged.speaker.isEmpty {
                    merged.speaker = speaker
                }
                groups[groups.count - 1] = merged
            } else {
                groups.append(LyricCueGroup(id: index,
                                            start: cue.start,
                                            end: cue.end,
                                            text: text,
                                            firstCueIndex: index,
                                            lastCueIndex: index,
                                            lineCount: 1,
                                            runeCount: text.count,
                                            speaker: speaker))
            }
        }
        return groups
    }

    private nonisolated static func shouldMergeLyricCue(_ group: LyricCueGroup,
                                                        with cue: VTTCue,
                                                        text: String,
                                                        speaker: String) -> Bool {
        let nextRunes = text.count
        guard group.lineCount < lyricGroupMaxLines else { return false }
        guard group.runeCount + nextRunes <= lyricGroupMaxRunes else { return false }
        guard cue.start - group.end <= lyricGroupMaxGapSeconds else { return false }
        guard max(group.end, cue.end) - group.start <= lyricGroupMaxDurationSeconds else { return false }
        if !group.speaker.isEmpty && !speaker.isEmpty && group.speaker != speaker {
            return false
        }
        return group.runeCount < lyricGroupMinRunes || nextRunes < lyricGroupMinRunes
    }

    /// Roles the human listener speaks under. The backend echoes these straight
    /// back over the socket (`PushUserMessage`) and also re-lists them in
    /// transcript snapshots, so we classify them as user-authored everywhere.
    /// Matches the snapshot path's `role == "user" || "viewer"`.
    nonisolated static func isUserRole(_ role: String) -> Bool {
        let r = role.lowercased()
        return r == "user" || r == "viewer"
    }

    nonisolated static func isLineAuthoredByCurrentUser(_ line: LiveLine,
                                                        currentUserID: String,
                                                        currentUsername: String) -> Bool {
        guard line.isUser else { return false }
        if let sender = line.senderUserID?.trimmingCharacters(in: .whitespacesAndNewlines),
           !sender.isEmpty {
            return userIDAliases(currentUserID).contains(sender)
        }
        let username = currentUsername.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !username.isEmpty else { return false }
        return line.speaker.trimmingCharacters(in: .whitespacesAndNewlines)
            .caseInsensitiveCompare(username) == .orderedSame
    }

    private nonisolated static func userIDAliases(_ id: String) -> Set<String> {
        let trimmed = id.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return [] }
        if trimmed.hasPrefix("oauth:") {
            return [trimmed, String(trimmed.dropFirst("oauth:".count))]
        }
        return [trimmed, "oauth:\(trimmed)"]
    }

    nonisolated static func applyPersistedMetadata(to line: inout LiveLine, from dto: DiscussionLineDTO) {
        guard line.speaker == dto.speaker,
              line.role == dto.role,
              line.text == dto.text,
              line.isUser == dto.isUser else { return }
        if let sender = dto.senderUserID?.trimmingCharacters(in: .whitespacesAndNewlines),
           !sender.isEmpty,
           line.senderUserID?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false {
            line.senderUserID = sender
        }
        if let audio = dto.audioURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !audio.isEmpty,
           line.audioURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false {
            line.audioURL = audio
        }
    }

    /// User-authored rows are visible once they are part of local discussion
    /// state. Role-only user echoes are still hidden so the WebSocket cannot
    /// duplicate an optimistic send as a second transcript row.
    nonisolated static func isVisibleTranscriptLine(_ line: LiveLine) -> Bool {
        line.isUser || !isUserRole(line.role)
    }

    nonisolated static func captionSpeaker(for caption: String, in lines: [LiveLine]) -> String? {
        let needle = normalizedCaptionMatchText(caption)
        guard !needle.isEmpty else { return nil }
        return lines.last(where: { line in
            !line.isUser && normalizedCaptionMatchText(line.text).contains(needle)
        })?.speaker
    }

    private nonisolated static func normalizedCaptionMatchText(_ raw: String) -> String {
        var scalars = String.UnicodeScalarView()
        for scalar in raw.unicodeScalars where CharacterSet.alphanumerics.contains(scalar) {
            scalars.append(scalar)
        }
        return String(scalars).lowercased()
    }

    // MARK: - Live transcript (WebSocket)

    private func listenEvents(jobID: String) async {
        let socket = JobSocket(api: api, jobID: jobID, discussionID: discussion.id)
        self.socket = socket
        isStartingJobSocket = false
        for await env in socket.events() {
            handle(env)
            if Task.isCancelled { break }
        }
        socket.close()
        if self.socket === socket { self.socket = nil }
    }

    func listenForJobUpdatesIfNeeded() {
        guard let jobID = discussion.jobID, socket == nil, !isStartingJobSocket else { return }
        isStartingJobSocket = true
        tasks.append(Task { await listenEvents(jobID: jobID) })
    }

    private func handle(_ env: JobEventEnvelope) {
        // The socket layer injects this after a drop+reconnect (e.g. the app was
        // backgrounded). It carries no data; use it to re-fetch state that may
        // have changed while disconnected.
        if env.event == JobSocket.reconnectEvent {
            refreshTranscriptAfterReconnect()
            refreshDiscussionForSummary()
            return
        }
        guard let data = env.data else { return }
        switch env.event {
        case "transcript":
            guard let speaker = data.speaker, let text = data.text else { return }
            let role = data.role ?? ""
            if data.isUserMessage == true {
                mergeUserMessageEvent(speaker: speaker,
                                      role: role,
                                      text: text,
                                      done: data.done == true,
                                      senderUserID: data.sender_user_id,
                                      audioURL: data.audio_url)
                hideTranscriptLoadingIfReady()
                return
            }
            // The backend echoes the listener's own messages back here. We already
            // recorded that line optimistically in `send(_:)`, so drop the echo
            // rather than re-adding it as a non-user line (`applyTranscriptEvent`
            // always sets isUser:false), which would both render and double-persist
            // into `discussion.lines`.
            if Self.isUserRole(role) { return }
            if let completed = LiveLine.applyTranscriptEvent(to: &lines,
                                                             speaker: speaker,
                                                             role: role,
                                                             text: text,
                                                             done: data.done == true) {
                persist(line: completed)
            }
            hideTranscriptLoadingIfReady()
        case "tick":
            updateJobProgress(elapsedMS: data.elapsed_ms, remainingMS: data.remaining_ms)
        case "phase":
            phaseLabel = data.label ?? data.phase ?? ""
            updateNowPlayingInfo()
        case "status":
            if let t = data.text {
                statusText = t
                updateNowPlayingInfo()
            }
        case "summary_ready":
            // The server changed the podcast summary state (generating, ready,
            // or failed). Re-fetch the discussion detail so the toolbar reflects
            // pending/manual/available states; the body itself is fetched later
            // by the summary view when it mounts.
            applySummaryEvent(data)
            refreshDiscussionForSummary()
        default:
            break
        }
    }

    /// Re-fetches the discussion detail to pick up updated `summary` metadata
    /// after a `summary_ready` event, preserving local transcript state.
    private func refreshDiscussionForSummary() {
        tasks.append(Task { [weak self] in
            guard let self else { return }
            guard let fresh = try? await self.api.discussion(id: self.discussion.id) else { return }
            self.discussion = Self.mergingLocalDiscussionState(current: self.discussion, fresh: fresh)
            self.listenForJobUpdatesIfNeeded()
        })
    }

    private func applySummaryEvent(_ data: JobEventData) {
        guard let raw = data.status, let status = SummaryStatus(rawValue: raw) else { return }
        discussion.summary = SummaryMeta(
            docType: data.doc_type,
            status: status,
            available: status == .ready,
            pending: status == .generating,
            generation: false,
            generatedAt: nil
        )
    }

    private func mergeUserMessageEvent(speaker: String,
                                       role: String,
                                       text: String,
                                       done: Bool,
                                       senderUserID: String?,
                                       audioURL: String?) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let sender = senderUserID?.trimmingCharacters(in: .whitespacesAndNewlines)
        let audio = audioURL?.trimmingCharacters(in: .whitespacesAndNewlines)
        let existingIndex = lines.firstIndex {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser
        }
        if let existingIndex {
            lines[existingIndex].done = done
            if let sender, !sender.isEmpty {
                lines[existingIndex].senderUserID = sender
            }
            if let audio, !audio.isEmpty {
                lines[existingIndex].audioURL = audio
            }
        } else {
            lines.append(LiveLine(speaker: speaker,
                                  role: role,
                                  text: trimmed,
                                  isUser: true,
                                  done: done,
                                  senderUserID: sender,
                                  audioURL: audio))
        }
        if let cachedIndex = discussion.lines?.firstIndex(where: {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser
        }) {
            if let sender, !sender.isEmpty {
                discussion.lines?[cachedIndex].senderUserID = sender
            }
            if let audio, !audio.isEmpty {
                discussion.lines?[cachedIndex].audioURL = audio
            }
        } else {
            persistIfNeeded(speaker: speaker,
                            role: role,
                            text: trimmed,
                            isUser: true,
                            senderUserID: sender,
                            syncRemote: false,
                            audioURL: audio)
        }
    }

    private func persist(line: LiveLine) {
        persistIfNeeded(line: line, startMs: Int(currentTime * 1000))
    }

    private func persistIfNeeded(line: LiveLine, startMs: Int = 0, syncRemote: Bool = true,
                                 audioURL: String? = nil, audioKey: String? = nil) {
        persistIfNeeded(speaker: line.speaker,
                        role: line.role,
                        text: line.text,
                        startMs: startMs,
                        isUser: line.isUser,
                        senderUserID: line.senderUserID,
                        syncRemote: syncRemote,
                        audioURL: audioURL,
                        audioKey: audioKey)
    }

    private func persistIfNeeded(speaker: String,
                                 role: String,
                                 text: String,
                                 startMs: Int = 0,
                                 isUser: Bool,
                                 senderUserID: String? = nil,
                                 syncRemote: Bool = true,
                                 audioURL: String? = nil,
                                 audioKey: String? = nil) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // Keep an empty voice message (audio present, transcript unavailable) — its
        // audio is still worth persisting; only empty text-only lines are dropped.
        let isVoice = audioURL != nil || audioKey != nil
        guard !trimmed.isEmpty || isVoice else { return }
        // A voice message is never a duplicate of an earlier line that happens to
        // share its transcript — distinct recordings carry distinct audio, so they
        // must not be deduped by text alone (that would drop the second note).
        let exists = audioURL == nil && discussion.sortedLines.contains {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed
                && $0.isUser == isUser && $0.audioURL == nil
        }
        guard !exists else { return }
        let dto = DiscussionLineDTO(speaker: speaker, role: role, side: nil,
                                    text: trimmed, startMS: startMs, isUser: isUser,
                                    senderUserID: senderUserID, audioURL: audioURL)
        discussion.lines = (discussion.lines ?? []) + [dto]
        guard syncRemote else { return }
        Task {
            try? await api.appendDiscussionLine(
                id: discussion.id,
                line: DiscussionLineRequest(speaker: speaker,
                                            role: role,
                                            side: nil,
                                            text: trimmed,
                                            startMS: startMs,
                                            isUser: isUser,
                                            audioURL: audioURL,
                                            audioKey: audioKey),
                shareToken: shareToken
            )
        }
    }

    /// Loads the full discussion detail on entry and adopts its persisted lines.
    ///
    /// The library opens the player from a lightweight list row that omits
    /// `native_discussion_lines`, so a re-entered discussion starts with no voice
    /// lines. The detail endpoint returns them with freshly re-signed `audioURL`s.
    /// Adopting them here (a) restores the replay control, (b) refreshes playback
    /// URLs that expire ~1h after the last fetch, and (c) gives the subsequent
    /// text-only job-transcript snapshot an existing audio line to recognize, so it
    /// won't re-persist the utterance as a duplicate plain-text row.
    private func hydratePersistedLines() async {
        guard let fresh = try? await api.discussion(id: discussion.id) else { return }
        // The library/market list responses omit the presigned audio download
        // URL (it is resolved only on the detail endpoint to keep lists fast), so
        // adopt it here to give the export/share action a direct link. Playback
        // and the download fallback already work from the jobID regardless.
        if let url = fresh.downloadURLString, !url.isEmpty {
            discussion.downloadURLString = url
            downloadURL = URL(string: url)
        }
        discussion.summary = fresh.summary
        listenForJobUpdatesIfNeeded()
        let persisted = fresh.sortedLines
        guard !persisted.isEmpty else { return }

        // Adopt persisted lines as the authoritative cache without dropping any
        // local lines not yet synced server-side.
        var cache = persisted
        for local in discussion.sortedLines where !cache.contains(local) {
            cache.append(local)
        }
        discussion.lines = cache

        // Reflect persisted lines into the visible transcript: upgrade a matching
        // text line with its audio URL, or append one we don't have yet.
        for dto in persisted {
            if let idx = lines.firstIndex(where: {
                $0.speaker == dto.speaker && $0.role == dto.role && $0.text == dto.text && $0.isUser == dto.isUser
            }) {
                Self.applyPersistedMetadata(to: &lines[idx], from: dto)
            } else {
                lines.append(LiveLine(speaker: dto.speaker, role: dto.role, text: dto.text,
                                      isUser: dto.isUser, done: true,
                                      senderUserID: dto.senderUserID, audioURL: dto.audioURL))
            }
        }
        hideTranscriptLoadingIfReady()
    }

    private func loadTranscriptSnapshot(jobID: String) async {
        while !Task.isCancelled && !hasPodcastTranscript {
            if let snapshot = try? await api.jobTranscript(id: jobID) {
                mergeTranscriptSnapshot(snapshot)
                if hasPodcastTranscript { return }
            }
            try? await Task.sleep(for: .seconds(2))
        }
    }

    /// Re-fetch the merged job transcript once and reconcile it. The live socket
    /// only delivers events that occur while it's connected, so anything that
    /// streamed during a drop (app backgrounded, network blip, pod hand-off) is
    /// missing until we pull the authoritative snapshot back in. `mergeTranscript`
    /// dedups, so re-running is safe and only fills gaps.
    private func refreshTranscriptSnapshot(jobID: String) async {
        if let snapshot = try? await api.jobTranscript(id: jobID) {
            mergeTranscriptSnapshot(snapshot)
        }
    }

    /// Kicks a one-shot transcript backfill after the socket reconnected.
    private func refreshTranscriptAfterReconnect() {
        guard let jobID = discussion.jobID else { return }
        tasks.append(Task { await self.refreshTranscriptSnapshot(jobID: jobID) })
    }

    /// Foreground hook: when the app returns to the foreground while the job is
    /// still generating, reconcile the transcript immediately instead of waiting
    /// for the socket to notice it dropped. Safe to call repeatedly.
    func foregroundRefresh() {
        guard discussion.status == .generating else { return }
        refreshTranscriptAfterReconnect()
    }

    private func mergeTranscriptSnapshot(_ snapshot: [TranscriptDTO]) {
        var didChange = false
        for item in snapshot {
            let text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { continue }
            let role = item.role
            let isUser = role == "user" || role == "viewer"
            let persisted = discussion.sortedLines.first {
                $0.speaker == item.speaker && $0.role == role && $0.text == text && $0.isUser == isUser
            }
            let existingIndex = lines.firstIndex { $0.speaker == item.speaker && $0.role == role && $0.text == text && $0.isUser == isUser }
            if let existingIndex {
                if let persisted {
                    Self.applyPersistedMetadata(to: &lines[existingIndex], from: persisted)
                }
            } else {
                lines.append(LiveLine(speaker: item.speaker, role: role, text: text, isUser: isUser, done: true,
                                      senderUserID: persisted?.senderUserID, audioURL: persisted?.audioURL))
                didChange = true
            }
            // The job transcript has no audio metadata, so a voice message comes
            // back as plain text. If we already hold it as a voice line, it is
            // already persisted (with its audio) — re-persisting would add a
            // duplicate text-only row that survives reload. A genuine user text
            // message (no audioURL) still backfills normally.
            if let existingIndex, lines[existingIndex].isUser, lines[existingIndex].audioURL?.isEmpty == false {
                continue
            }
            persistIfNeeded(speaker: item.speaker, role: role, text: text, isUser: isUser)
        }
        if didChange {
            lines.sort { lhs, rhs in
                let leftIndex = discussion.sortedLines.firstIndex {
                    $0.speaker == lhs.speaker && $0.role == lhs.role && $0.text == lhs.text && $0.isUser == lhs.isUser
                } ?? Int.max
                let rightIndex = discussion.sortedLines.firstIndex {
                    $0.speaker == rhs.speaker && $0.role == rhs.role && $0.text == rhs.text && $0.isUser == rhs.isUser
                } ?? Int.max
                return leftIndex < rightIndex
            }
        }
        hideTranscriptLoadingIfReady()
    }

    private var hasPodcastTranscript: Bool {
        Self.containsPodcastTranscript(lines)
    }

    nonisolated static func containsPodcastTranscript(_ lines: [LiveLine]) -> Bool {
        lines.contains { line in
            let role = line.role.lowercased()
            return !line.isUser
                && role != "user"
                && role != "viewer"
                && !line.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    nonisolated static func remainingTranscriptLoadingDelay(startedAt: Date?,
                                                            now: Date = Date()) -> TimeInterval {
        guard let startedAt else { return 0 }
        let elapsed = max(0, now.timeIntervalSince(startedAt))
        return max(0, minimumTranscriptLoadingSeconds - elapsed)
    }

    nonisolated static func transcriptLoadingVisibleAfterTerminalFailure(wasVisible: Bool) -> Bool {
        false
    }

    private func showTranscriptLoadingIfNeeded() {
        guard !isTranscriptLoading else { return }
        isTranscriptLoading = true
        transcriptLoadingStartedAt = Date()
        transcriptLoadingHideTask?.cancel()
        transcriptLoadingHideTask = nil
    }

    private func forceHideTranscriptLoading() {
        transcriptLoadingHideTask?.cancel()
        transcriptLoadingHideTask = nil
        transcriptLoadingStartedAt = nil
        isTranscriptLoading = Self.transcriptLoadingVisibleAfterTerminalFailure(wasVisible: isTranscriptLoading)
    }

    private func hideTranscriptLoadingIfReady() {
        guard isTranscriptLoading, hasPodcastTranscript else { return }

        transcriptLoadingHideTask?.cancel()
        let delay = Self.remainingTranscriptLoadingDelay(startedAt: transcriptLoadingStartedAt)
        if delay <= 0 {
            isTranscriptLoading = false
            transcriptLoadingStartedAt = nil
            transcriptLoadingHideTask = nil
            return
        }

        let task = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                guard let self, self.hasPodcastTranscript else { return }
                self.isTranscriptLoading = false
                self.transcriptLoadingStartedAt = nil
                self.transcriptLoadingHideTask = nil
            }
        }
        transcriptLoadingHideTask = task
        tasks.append(task)
    }

    // MARK: - Captions

    private func pollCaptions(jobID: String) async {
        while !Task.isCancelled && !isFinished {
            if let vtt = try? await api.liveSubtitles(id: jobID) {
                cues = Self.parseVTT(vtt)
            }
            try? await Task.sleep(for: .seconds(3))
        }
    }

    private func loadFinalCaptions(jobID: String) async {
        if let vtt = try? await api.liveSubtitles(id: jobID) {
            cues = Self.parseVTT(vtt)
            // Seed the active lyric group so the list scrolls to the right place
            // on first appearance, before the periodic time observer fires.
            updateActiveLyricGroup(at: currentTime)
        }
    }

    // MARK: - Job status

    private func pollStatus(jobID: String) async {
        while !Task.isCancelled && !isFinished {
            if let job = try? await api.jobStatus(id: jobID) {
                phaseLabel = job.phase_label ?? phaseLabel
                updateJobProgress(elapsedMS: job.elapsed_ms, remainingMS: job.remaining_ms)
                if job.isDone {
                    isForceStopping = false
                    isFinished = true
                    await refreshDiscussionAfterJobDone(job: job)
                    listenForJobUpdatesIfNeeded()
                    await switchToFinalAudioIfNeeded(jobID: jobID)
                    return
                } else if job.isError {
                    markGenerationFailed(job.error)
                    return
                }
            }
            try? await Task.sleep(for: .seconds(2))
        }
    }

    private func refreshDiscussionAfterJobDone(job: JobStatusDTO) async {
        if let fresh = try? await api.discussion(id: discussion.id) {
            discussion = Self.mergingLocalDiscussionState(current: discussion, fresh: fresh)
        } else {
            // Avoid showing a stale planning-only points badge when the terminal
            // discussion refresh failed. A later screen reload will fetch the
            // authoritative settled value.
            discussion.pointsCharged = nil
            discussion.status = .ready
        }
        if let url = job.download_url {
            downloadURL = URL(string: url)
            discussion.downloadURLString = url
        } else if let url = discussion.downloadURLString {
            downloadURL = URL(string: url)
        }
        discussion.status = .ready
        discussion.allowSendingMessage = false
        await refreshCoverAssets()
        updateNowPlayingInfo()
    }

    /// Fetches the latest discussion solely to pick up cover art generated in
    /// the background, without disturbing live transcript/playback state.
    private func refreshCover() async {
        if let fresh = try? await api.discussion(id: discussion.id) {
            discussion.summary = fresh.summary
            listenForJobUpdatesIfNeeded()
            if let cover = fresh.cover, cover.hasImage || cover.hasGradient {
                discussion.cover = cover
            }
        }
        await refreshCoverAssets()
    }

    private func refreshCoverAssets() async {
        await loadCoverColors()
        await loadNowPlayingArtwork()
    }

    /// Resolves the two background colors for the current cover: gradient covers
    /// use their stored hexes directly; image covers are downloaded and sampled.
    /// Keyed so it skips redundant work when the cover hasn't changed.
    func loadCoverColors() async {
        guard let cover = discussion.cover else { return }
        let key = cover.imageURL ?? "\(cover.gradientStart ?? "")-\(cover.gradientEnd ?? "")"
        guard !key.isEmpty, key != coverColorsSourceKey else { return }

        if cover.hasGradient, let start = cover.gradientStart, let end = cover.gradientEnd {
            coverColorsSourceKey = key
            coverColors = [Color(hex: start), Color(hex: end)]
            return
        }
        guard cover.hasImage,
              let urlString = cover.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
              let url = URL(string: urlString),
              let (data, _) = try? await URLSession.shared.data(from: url) else { return }
        let colors = await Task.detached(priority: .utility) {
            CoverPalette.dominantColors(from: data, count: 2)
        }.value
        guard colors.count >= 2 else { return }
        coverColorsSourceKey = key
        coverColors = colors
    }

    private func loadNowPlayingArtwork() async {
        guard let cover = discussion.cover,
              let key = Self.nowPlayingArtworkSourceKey(for: cover),
              key != nowPlayingArtworkSourceKey else { return }

        if cover.hasImage,
           let urlString = cover.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           let url = URL(string: urlString),
           let (data, _) = try? await URLSession.shared.data(from: url),
           let image = UIImage(data: data) {
            nowPlayingArtworkSourceKey = key
            nowPlayingArtwork = MPMediaItemArtwork(boundsSize: image.size) { _ in image }
            updateNowPlayingInfo()
            return
        }

        guard cover.hasGradient else { return }
        let image = Self.gradientArtworkImage(
            startHex: cover.gradientStart ?? "#8E5CF7",
            endHex: cover.gradientEnd ?? "#00A3FF"
        )
        nowPlayingArtworkSourceKey = key
        nowPlayingArtwork = MPMediaItemArtwork(boundsSize: image.size) { _ in image }
        updateNowPlayingInfo()
    }

    nonisolated static func nowPlayingArtworkSourceKey(for cover: DiscussionCover?) -> String? {
        guard let cover else { return nil }
        if cover.hasImage,
           let url = cover.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !url.isEmpty {
            return "image:\(url)"
        }
        if cover.hasGradient,
           let start = cover.gradientStart?.trimmingCharacters(in: .whitespacesAndNewlines),
           let end = cover.gradientEnd?.trimmingCharacters(in: .whitespacesAndNewlines),
           !start.isEmpty,
           !end.isEmpty {
            return "gradient:\(start):\(end)"
        }
        return nil
    }

    private nonisolated static func gradientArtworkImage(startHex: String, endHex: String) -> UIImage {
        let size = CGSize(width: 512, height: 512)
        return UIGraphicsImageRenderer(size: size).image { context in
            let cgContext = context.cgContext
            let colors = [
                UIColor(hex: startHex).cgColor,
                UIColor(hex: endHex).cgColor,
            ] as CFArray
            let colorSpace = CGColorSpaceCreateDeviceRGB()
            guard let gradient = CGGradient(colorsSpace: colorSpace, colors: colors, locations: [0, 1]) else {
                UIColor(hex: startHex).setFill()
                cgContext.fill(CGRect(origin: .zero, size: size))
                return
            }
            cgContext.drawLinearGradient(
                gradient,
                start: CGPoint(x: 0, y: 0),
                end: CGPoint(x: size.width, y: size.height),
                options: []
            )
        }
    }

    nonisolated static func mergingLocalDiscussionState(current: Discussion, fresh: Discussion) -> Discussion {
        var merged = fresh
        let localLines = current.lines ?? []
        guard !localLines.isEmpty else { return merged }
        var lines = merged.lines ?? []
        for line in localLines where !lines.contains(line) {
            lines.append(line)
        }
        merged.lines = lines
        return merged
    }

    private func switchToFinalAudioIfNeeded(jobID: String) async {
        guard usesLiveCaptionTiming else {
            playerLog.debug("switchToFinalAudio: already on final timing, nothing to do")
            return
        }
        let resumeTime = currentTime
        let shouldResume = isPlaying || autoplayRequested
        playerLog.debug("switchToFinalAudio: swapping live HLS -> final audio (resume=\(resumeTime, privacy: .public), shouldResume=\(shouldResume, privacy: .public))")
        await loadFinalCaptions(jobID: jobID)

        var options: [String: Any] = [:]
        var headers = ["Accept-Language": AcceptLanguage.headerValue]
        if let token = await api.currentToken() {
            headers["Authorization"] = "Bearer \(token)"
        }
        options["AVURLAssetHTTPHeaderFieldsKey"] = headers
        let asset = AVURLAsset(url: api.finalAudioURL(jobID: jobID), options: options)
        let item = AVPlayerItem(asset: asset)
        item.preferredForwardBufferDuration = 0
        finalAudioInstalled = true
        player.replaceCurrentItem(with: item)
        usesLiveCaptionTiming = false

        let interval = CMTime(seconds: 0.25, preferredTimescale: 600)
        if let timeObserver { player.removeTimeObserver(timeObserver) }
        timeObserver = player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { [weak self] t in
            guard let self else { return }
            let secs = t.seconds
            self.currentTime = secs.isFinite ? secs : 0
            if let dur = self.player.currentItem?.duration.seconds, dur.isFinite, dur > 0 {
                self.duration = dur
            }
            self.updateCaption(at: self.currentTime)
            self.updateNowPlayingInfo()
        }
        // Resume at the live position, but only once the final item is ready and
        // clamped to its real duration — seeking the just-replaced item here
        // (before `.readyToPlay`) is what left playback silent until re-entry.
        pendingResumeTime = resumeTime > 0 ? resumeTime : nil
        if shouldResume {
            beginPlayback(item: item)
        } else {
            pause()
        }
    }

    private func updateJobProgress(elapsedMS: Int?, remainingMS: Int?) {
        if let elapsedMS, elapsedMS >= 0 {
            elapsedTime = Double(elapsedMS) / 1000
        }
        if let remainingMS, remainingMS >= 0 {
            remainingTime = Double(remainingMS) / 1000
        }
        updateNowPlayingInfo()
    }

    func markGenerationFailed(_ error: String?) {
        isForceStopping = false
        isFinished = true
        statusText = error ?? String(localized: "Generation failed",
                                     comment: "Fallback status when podcast generation failed without a server message")
        discussion.status = .failed
        discussion.allowSendingMessage = false
        socket?.close()
        forceHideTranscriptLoading()
    }

    private func updateNowPlayingInfo() {
        var info: [String: Any] = [
            MPMediaItemPropertyTitle: nowPlayingTitle,
            MPMediaItemPropertyArtist: nowPlayingSubtitle,
            MPMediaItemPropertyAlbumTitle: AppStringLiteral.appTitleRaw,
            MPNowPlayingInfoPropertyElapsedPlaybackTime: currentTime,
            MPNowPlayingInfoPropertyPlaybackRate: isPlaying ? 1.0 : 0.0
        ]

        let hasSeekableDuration = duration > 0
        let totalDuration = nowPlayingDuration
        if totalDuration > 0 {
            info[MPMediaItemPropertyPlaybackDuration] = totalDuration
            MPRemoteCommandCenter.shared().changePlaybackPositionCommand.isEnabled = hasSeekableDuration
        } else {
            info[MPNowPlayingInfoPropertyIsLiveStream] = true
            MPRemoteCommandCenter.shared().changePlaybackPositionCommand.isEnabled = false
        }
        if let nowPlayingArtwork {
            info[MPMediaItemPropertyArtwork] = nowPlayingArtwork
        }

        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
        MPNowPlayingInfoCenter.default().playbackState = isPlaying ? .playing : .paused
    }

    private var nowPlayingTitle: String {
        if !discussion.displayTitle.isEmpty { return discussion.displayTitle }
        if !discussion.topic.isEmpty { return discussion.topic }
        return AppStringLiteral.stationNameRaw
    }

    private var nowPlayingSubtitle: String {
        if !caption.isEmpty {
            if !captionSpeaker.isEmpty { return "\(captionSpeaker): \(caption)" }
            return caption
        }
        if !phaseLabel.isEmpty { return phaseLabel }
        if !statusText.isEmpty { return statusText }
        return AppStringLiteral.appTitleRaw
    }

    private var nowPlayingDuration: Double {
        if duration > 0 { return duration }
        let estimatedTotal = elapsedTime + remainingTime
        return estimatedTotal > 0 ? estimatedTotal : 0
    }

    // MARK: - VTT parsing

    nonisolated static func parseVTT(_ text: String) -> [VTTCue] {
        var cues: [VTTCue] = []
        let blocks = text.components(separatedBy: "\n\n")
        for block in blocks {
            let lines = block.split(separator: "\n").map(String.init)
            guard let arrowLine = lines.first(where: { $0.contains("-->") }) else { continue }
            let parts = arrowLine.components(separatedBy: "-->")
            guard parts.count == 2,
                  let start = parseTimestamp(parts[0]),
                  let end = parseTimestamp(parts[1]) else { continue }
            let textLines = lines.drop(while: { !$0.contains("-->") }).dropFirst()
            let cueText = textLines.joined(separator: " ").trimmingCharacters(in: .whitespaces)
            if !cueText.isEmpty { cues.append(VTTCue(start: start, end: end, text: cueText)) }
        }
        return cues
    }

    private nonisolated static func parseTimestamp(_ raw: String) -> Double? {
        let s = raw.trimmingCharacters(in: .whitespaces)
        let comps = s.components(separatedBy: ":")
        guard !comps.isEmpty else { return nil }
        var seconds = 0.0
        for part in comps { seconds = seconds * 60 + (Double(part.replacingOccurrences(of: ",", with: ".")) ?? 0) }
        return seconds
    }
}

private extension UIColor {
    convenience init(hex: String) {
        let clean = hex.trimmingCharacters(in: CharacterSet(charactersIn: "#")).uppercased()
        var value: UInt64 = 0
        Scanner(string: clean).scanHexInt64(&value)
        let red = CGFloat((value >> 16) & 0xFF) / 255.0
        let green = CGFloat((value >> 8) & 0xFF) / 255.0
        let blue = CGFloat(value & 0xFF) / 255.0
        self.init(red: red, green: green, blue: blue, alpha: 1)
    }
}
