import Foundation
import Observation
import AVFoundation
import MediaPlayer
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
    private let api: APIClient
    private let username: String

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
    var usageSummaryText: String = ""
    var usageSummary: UsageSummary?
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
        discussion.status == .generating && !isFinished && !isForceStopping && discussion.jobID != nil
    }
    var showsForceStopAction: Bool {
        discussion.status == .generating && !isFinished && discussion.jobID != nil
    }
    var isReadyForDownload: Bool {
        discussion.status == .ready || (isFinished && downloadURL != nil)
    }
    var canDownloadPodcast: Bool {
        isReadyForDownload && (downloadURL != nil || discussion.jobID != nil)
    }
    var showsPodcastActions: Bool {
        showsForceStopAction || canDownloadPodcast
    }
    var canSeek: Bool {
        !usesLiveCaptionTiming && duration > 0
    }

    private var socket: JobSocket?
    private var timeObserver: Any?
    private var itemStatusObservation: NSKeyValueObservation?
    private var playbackRetryTask: Task<Void, Never>?
    private var playbackJobID: String?
    private var playbackRetryCount = 0
    private var remoteCommandTargets: [Any] = []
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

    /// Final, seekable caption list grouped for the lyrics view. Live captions
    /// still read the raw cue list so streaming timing stays one cue at a time.
    var lyricCueGroups: [LyricCueGroup] {
        Self.groupLyricCues(cues) { [lines] cue in
            Self.captionSpeaker(for: cue.text, in: lines) ?? ""
        }
    }

    /// Index of the cue at the current playback time, used to highlight and
    /// auto-scroll the lyrics list. Falls back to the last cue already passed.
    var activeCueIndex: Int? {
        let t = captionLookupTime(playbackTime: currentTime)
        return cues.firstIndex(where: { t >= $0.start && t <= $0.end })
            ?? cues.lastIndex(where: { $0.start <= t })
    }

    var activeLyricGroupID: Int? {
        let groups = lyricCueGroups
        let t = captionLookupTime(playbackTime: currentTime)
        let index = groups.firstIndex(where: { t >= $0.start && t <= $0.end })
            ?? groups.lastIndex(where: { $0.start <= t })
        guard let index else { return nil }
        return groups[index].id
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

    init(discussion: Discussion, api: APIClient, username: String) {
        self.discussion = discussion
        self.api = api
        self.username = username
    }

    func start() {
        configureAudioSession()
        // Decide autoplay from the entry status only — not from which asset
        // `setupPlayer` ends up installing. A generating entry that flips to
        // `.ready` mid-setup still autoplays its final audio.
        autoplayOnEntry = !(discussion.status == .ready || isFinished)
        // Replay persisted transcript for a finished discussion.
        lines = discussion.sortedLines.map {
            LiveLine(speaker: $0.speaker, role: $0.role, text: $0.text, isUser: $0.isUser, done: true)
        }
        if discussion.jobID != nil && !hasPodcastTranscript {
            showTranscriptLoadingIfNeeded()
        }
        if let s = discussion.downloadURLString { downloadURL = URL(string: s) }
        usageSummaryText = discussion.usageSummaryText ?? ""
        usageSummary = discussion.usageSummary
        guard let jobID = discussion.jobID else { return }

        configureRemoteCommands()
        updateNowPlayingInfo()
        tasks.append(Task { await loadTranscriptSnapshot(jobID: jobID) })
        tasks.append(Task { await setupPlayer(jobID: jobID) })
        if discussion.status == .generating {
            tasks.append(Task { await listenEvents(jobID: jobID) })
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
        tasks.forEach { $0.cancel() }
        tasks.removeAll()
        player.pause()
        removeRemoteCommands()
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
        guard let item = player.currentItem else {
            playerLog.debug("resumePlayback: no current item, falling back to play()")
            play()
            return
        }
        playerLog.debug("resumePlayback: status=\(item.status.rawValue, privacy: .public) timeControl=\(self.player.timeControlStatus.rawValue, privacy: .public)")
        beginPlayback(item: item)
    }

    func send(_ text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let line = LiveLine(speaker: username, role: "user", text: trimmed, isUser: true, done: true)
        lines.append(line)
        let jobID = discussion.jobID
        persistIfNeeded(line: line, syncRemote: jobID == nil)
        guard let jobID else { return }
        Task {
            do {
                try await api.sendJobMessage(id: jobID,
                                            text: trimmed,
                                            username: username,
                                            discussionID: discussion.id)
            } catch {
                try? await api.appendDiscussionLine(
                    id: discussion.id,
                    line: DiscussionLineRequest(speaker: username,
                                                role: "user",
                                                side: nil,
                                                text: trimmed,
                                                startMS: 0,
                                                isUser: true)
                )
            }
        }
    }

    func forceStop() {
        guard canForceStop, let jobID = discussion.jobID else { return }
        isForceStopping = true
        statusText = "Stopping and finalising upload..."
        updateNowPlayingInfo()
        Task {
            do {
                try await api.forceStopJob(id: jobID)
            } catch {
                isForceStopping = false
                statusText = "Stop failed: \(error.localizedDescription)"
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
        #endif
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
                    statusText = "Preparing live audio..."
                }
                try? await Task.sleep(for: .seconds(1))
            }
        }
        guard !Task.isCancelled else { return }

        let useFinalAudio = isFinished || discussion.status == .ready
        usesLiveCaptionTiming = !useFinalAudio
        let url = useFinalAudio ? api.finalAudioURL(jobID: jobID) : api.hlsURL(jobID: jobID)
        var options: [String: Any] = [:]
        if let token = await api.currentToken() {
            options["AVURLAssetHTTPHeaderFieldsKey"] = ["Authorization": "Bearer \(token)"]
        }
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
        guard playbackRetryTask == nil else { return }
        guard playbackRetryCount < 8 else {
            statusText = "Audio stream is still warming up. Try again in a moment."
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
            guard !Task.isCancelled, autoplayRequested else { return }
            try? await Task.sleep(for: .seconds(attempt == 0 ? 0.4 : 1.0))
            guard !Task.isCancelled, autoplayRequested else { return }
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
        let socket = JobSocket(api: api, jobID: jobID)
        self.socket = socket
        for await env in socket.events() {
            handle(env)
            if Task.isCancelled { break }
        }
    }

    private func handle(_ env: JobEventEnvelope) {
        guard let data = env.data else { return }
        switch env.event {
        case "transcript":
            guard let speaker = data.speaker, let text = data.text else { return }
            let role = data.role ?? ""
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
        default:
            break
        }
    }

    private func persist(line: LiveLine) {
        persistIfNeeded(line: line, startMs: Int(currentTime * 1000))
    }

    private func persistIfNeeded(line: LiveLine, startMs: Int = 0, syncRemote: Bool = true) {
        persistIfNeeded(speaker: line.speaker,
                        role: line.role,
                        text: line.text,
                        startMs: startMs,
                        isUser: line.isUser,
                        syncRemote: syncRemote)
    }

    private func persistIfNeeded(speaker: String,
                                 role: String,
                                 text: String,
                                 startMs: Int = 0,
                                 isUser: Bool,
                                 syncRemote: Bool = true) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let exists = discussion.sortedLines.contains {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser == isUser
        }
        guard !exists else { return }
        let dto = DiscussionLineDTO(speaker: speaker, role: role, side: nil,
                                    text: trimmed, startMS: startMs, isUser: isUser)
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
                                            isUser: isUser)
            )
        }
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

    private func mergeTranscriptSnapshot(_ snapshot: [TranscriptDTO]) {
        var didChange = false
        for item in snapshot {
            let text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { continue }
            let role = item.role
            let isUser = role == "user" || role == "viewer"
            if !lines.contains(where: { $0.speaker == item.speaker && $0.role == role && $0.text == text && $0.isUser == isUser }) {
                lines.append(LiveLine(speaker: item.speaker, role: role, text: text, isUser: isUser, done: true))
                didChange = true
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
        }
    }

    // MARK: - Job status

    private func pollStatus(jobID: String) async {
        while !Task.isCancelled && !isFinished {
            if let job = try? await api.jobStatus(id: jobID) {
                phaseLabel = job.phase_label ?? phaseLabel
                applyUsageSummary(job)
                updateJobProgress(elapsedMS: job.elapsed_ms, remainingMS: job.remaining_ms)
                if job.isDone {
                    isForceStopping = false
                    isFinished = true
                    discussion.status = .ready
                    if let url = job.download_url {
                        downloadURL = URL(string: url)
                        discussion.downloadURLString = url
                    }
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
        if let token = await api.currentToken() {
            options["AVURLAssetHTTPHeaderFieldsKey"] = ["Authorization": "Bearer \(token)"]
        }
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

    private func applyUsageSummary(_ job: JobStatusDTO) {
        guard let text = job.usageSummaryText, !text.isEmpty else { return }
        usageSummaryText = text
        usageSummary = job.usageSummary
        discussion.promptTokens = job.prompt_tokens
        discussion.completionTokens = job.completion_tokens
        discussion.totalTokens = job.total_tokens
        discussion.llmCostUSD = job.llm_cost_usd
        discussion.llmCostKnown = job.llm_cost_known
        discussion.ttsCostUSD = job.tts_cost_usd
        discussion.musicCostUSD = job.music_cost_usd
        statusText = text
    }

    func markGenerationFailed(_ error: String?) {
        isForceStopping = false
        isFinished = true
        statusText = error ?? "Generation failed"
        discussion.status = .failed
        forceHideTranscriptLoading()
    }

    private func updateNowPlayingInfo() {
        var info: [String: Any] = [
            MPMediaItemPropertyTitle: nowPlayingTitle,
            MPMediaItemPropertyArtist: nowPlayingSubtitle,
            MPMediaItemPropertyAlbumTitle: "PanelFM",
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

        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
        MPNowPlayingInfoCenter.default().playbackState = isPlaying ? .playing : .paused
    }

    private var nowPlayingTitle: String {
        if !discussion.displayTitle.isEmpty { return discussion.displayTitle }
        if !discussion.topic.isEmpty { return discussion.topic }
        return "Podcast"
    }

    private var nowPlayingSubtitle: String {
        if !caption.isEmpty {
            if !captionSpeaker.isEmpty { return "\(captionSpeaker): \(caption)" }
            return caption
        }
        if !phaseLabel.isEmpty { return phaseLabel }
        if !statusText.isEmpty { return statusText }
        return "PanelFM"
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
