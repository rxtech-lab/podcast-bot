import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

extension PlayerModel {
    // MARK: - Playback

    func configureAudioSession() {
        #if os(iOS)
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
        try? AVAudioSession.sharedInstance().setActive(true)
        configureAudioSessionObservers()
        #endif
    }

    func activateAudioSessionForManualPlayback() -> Bool {
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

    func configureAudioSessionObservers() {
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

    func removeAudioSessionObservers() {
        if let audioInterruptionObserver {
            NotificationCenter.default.removeObserver(audioInterruptionObserver)
            self.audioInterruptionObserver = nil
        }
        isAudioSessionInterrupted = false
        shouldResumeAfterAudioInterruption = false
    }

    func handleAudioSessionInterruption(_ notification: Notification) {
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

    func suppressPlaybackForAudioInterruption() {
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

    func configureRemoteCommands() {
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

    func removeRemoteCommands() {
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

    func play() {
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

    func pause() {
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

    func setupPlayer(jobID: String, retryingPlayback: Bool = false) async {
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
    func beginPlayback(item: AVPlayerItem, autoplay: Bool = true) {
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

    func handlePlaybackItemFailed(_ item: AVPlayerItem) {
        guard player.currentItem === item else { return }
        itemStatusObservation?.invalidate()
        itemStatusObservation = nil
        let message = item.error?.localizedDescription ?? "unknown"
        playerLog.error("beginPlayback: item failed: \(message, privacy: .public)")
        schedulePlaybackRetry()
    }

    func schedulePlaybackRetry() {
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
    func applyPendingResume(on item: AVPlayerItem) {
        guard let resume = pendingResumeTime, resume > 0 else { return }
        pendingResumeTime = nil
        let dur = item.duration.seconds
        let target = (dur.isFinite && dur > 0) ? min(resume, max(dur - 1, 0)) : resume
        playerLog.debug("applyPendingResume: seeking to \(target, privacy: .public) (requested \(resume, privacy: .public), dur \(dur, privacy: .public))")
        player.seek(to: CMTime(seconds: target, preferredTimescale: 600))
        currentTime = target
        updateCaption(at: target)
    }

    func startConfirmedPlayback() {
        play()
        tasks.append(Task { await confirmPlayback() })
    }

    /// Confirms playback truly started, kicking the player (pause→play — the
    /// same recovery the user does by hand) if it stalls instead of rendering.
    /// "Started" means `timeControlStatus == .playing`; a bare repeated
    /// `play()` does nothing to a player stuck in `.waitingToPlayAtSpecifiedRate`,
    /// so we cycle it. Bounded so a genuinely failed stream eventually gives up.
    func confirmPlayback() async {
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


    func updateCaption(at time: Double) {
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

    func captionLookupTime(playbackTime: Double) -> Double {
        Self.captionLookupTime(playbackTime: playbackTime, usesLiveCaptionTiming: usesLiveCaptionTiming)
    }

    nonisolated static func captionLookupTime(playbackTime: Double, usesLiveCaptionTiming: Bool) -> Double {
        usesLiveCaptionTiming ? playbackTime + liveCaptionLeadSeconds : playbackTime
    }

    nonisolated static func captionText(in cues: [VTTCue], at time: Double) -> String {
        captionCue(in: cues, at: time)?.text ?? ""
    }

    /// Prefers the latest-starting cue containing `time`: stored segments can
    /// overlap (an STT phrase may overrun the next one), and cue starts are
    /// monotonic, so the latest match is the line actually being spoken.
    nonisolated static func captionCue(in cues: [VTTCue], at time: Double) -> VTTCue? {
        cues.last(where: { time >= $0.start && time <= $0.end })
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

    nonisolated static func shouldMergeLyricCue(_ group: LyricCueGroup,
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

    nonisolated static func userIDAliases(_ id: String) -> Set<String> {
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
        if let image = dto.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !image.isEmpty,
           line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false {
            line.imageURL = image
        }
        if line.audioOffsetSeconds == nil,
           let offset = audioOffsetSeconds(fromMS: dto.startMS) {
            line.audioOffsetSeconds = offset
        }
        if let sources = dto.sources, !(sources.isEmpty) {
            line.sources = sources
        }
        if let judgement = dto.judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines),
           !judgement.isEmpty {
            line.judgementComment = judgement
        }
    }

    /// User-authored rows are visible once they are part of local discussion
    /// state. Role-only user echoes are still hidden so the WebSocket cannot
    /// duplicate an optimistic send as a second transcript row.
    nonisolated static func isVisibleTranscriptLine(_ line: LiveLine) -> Bool {
        line.hasRenderablePayload && (line.isUser || !isUserRole(line.role))
    }

    nonisolated static func visibleTranscriptLines(_ lines: [LiveLine]) -> [LiveLine] {
        let visible = lines.filter { isVisibleTranscriptLine($0) }
        guard visible.count > 1 else { return visible }
        var out: [LiveLine] = []
        for index in visible.indices {
            if index < visible.index(before: visible.endIndex),
               isRedundantPrefixLine(visible[index], before: visible[visible.index(after: index)]) {
                continue
            }
            out.append(visible[index])
        }
        return out
    }

    nonisolated static func isRedundantPrefixLine(_ line: LiveLine, before next: LiveLine) -> Bool {
        guard !line.isUser,
              !next.isUser,
              line.speaker == next.speaker,
              line.role == next.role,
              line.done,
              (line.audioURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true),
              (line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true),
              (line.sources?.isEmpty ?? true),
              (line.judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true) else {
            return false
        }
        let text = line.text.trimmingCharacters(in: .whitespacesAndNewlines)
        let nextText = next.text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, text != nextText, nextText.hasPrefix(text) else { return false }
        let suffix = nextText.dropFirst(text.count)
        guard let boundary = suffix.first else { return false }
        return boundary.isWhitespace || isTranscriptBoundary(boundary) || text.last.map(isTranscriptBoundary) == true
    }

    nonisolated static func isTranscriptBoundary(_ char: Character) -> Bool {
        ".。!！?？，,;；:：、".contains(char)
    }

    nonisolated static func captionSpeaker(for caption: String, in lines: [LiveLine]) -> String? {
        let needle = normalizedCaptionMatchText(caption)
        guard !needle.isEmpty else { return nil }
        return lines.last(where: { line in
            !line.isUser && normalizedCaptionMatchText(line.text).contains(needle)
        })?.speaker
    }

    nonisolated static func audioBookChapterTitle(at time: Double,
                                                  in cues: [VTTCue],
                                                  script: ScriptDTO?) -> String? {
        guard script?.type == "audio-book" else { return nil }
        let titles = (script?.audioBookChapters ?? []).compactMap { chapter -> (raw: String, key: String)? in
            let raw = chapter.title.trimmingCharacters(in: .whitespacesAndNewlines)
            let key = normalizedCaptionMatchText(raw)
            guard !raw.isEmpty, !key.isEmpty else { return nil }
            return (raw, key)
        }
        guard !titles.isEmpty,
              let cueIndex = cues.lastIndex(where: { $0.start <= time + audioBookChapterTitleLeadSeconds }) else { return nil }

        for cue in cues[...cueIndex].reversed() {
            let cueKey = normalizedCaptionMatchText(cue.text)
            guard !cueKey.isEmpty else { continue }
            if let match = titles.first(where: { cueKey.contains($0.key) }) {
                return match.raw
            }
        }
        return nil
    }

    nonisolated static func normalizedCaptionMatchText(_ raw: String) -> String {
        var scalars = String.UnicodeScalarView()
        for scalar in raw.unicodeScalars where CharacterSet.alphanumerics.contains(scalar) {
            scalars.append(scalar)
        }
        return String(scalars).lowercased()
    }

}
