import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

extension PlayerModel {
    // MARK: - Timed illustrations (audiobook artwork)

    /// Whether the player is still on the live (non-seekable) timeline. Live
    /// artwork shows the most recently arrived illustration; playback artwork
    /// is looked up by `currentTime` against `illustrationTimeline`.
    var isLivePlayback: Bool { usesLiveCaptionTiming }

    /// One audiobook illustration with its audio-timeline start.
    struct IllustrationCue: Equatable {
        let start: Double
        let url: URL
        let caption: String
    }

    /// Illustration cues ordered by audio offset, exactly as served by
    /// GET /api/jobs/{id}/illustrations. The backend owns all timing —
    /// including the legacy synthesis for audiobooks generated before
    /// per-image offsets existed — so the player never reconstructs a
    /// timeline from transcript lines. Empty when the discussion has no timed
    /// artwork (the artwork slot stays on the cover).
    /// The most recently arrived illustration — what live streaming shows.
    var latestIllustrationCue: IllustrationCue? {
        for line in lines.reversed() where line.hasImage {
            if let raw = line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
               let url = URL(string: raw) {
                return IllustrationCue(start: line.audioOffsetSeconds ?? 0,
                                       url: url,
                                       caption: line.displayText)
            }
        }
        return nil
    }

    var latestIllustrationURL: URL? {
        latestIllustrationCue?.url
    }

    /// The illustration on screen at playback time `time`, or nil before the
    /// first cue (artwork falls back to the cover).
    func illustrationCue(at time: Double) -> IllustrationCue? {
        illustrationTimeline.last(where: { $0.start <= time })
    }

    func illustrationURL(at time: Double) -> URL? {
        illustrationCue(at: time)?.url
    }

    /// Maps the server's illustration DTOs into player cues, dropping entries
    /// whose URL doesn't parse and keeping the array ordered by start time.
    nonisolated static func illustrationCues(from dtos: [IllustrationCueDTO]) -> [IllustrationCue] {
        dtos.compactMap { dto -> IllustrationCue? in
            let raw = dto.imageURL.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !raw.isEmpty, let url = URL(string: raw) else { return nil }
            return IllustrationCue(start: max(0, Double(dto.startMS) / 1000.0),
                                   url: url,
                                   caption: dto.caption?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
        }
        .sorted { $0.start < $1.start }
    }

    /// Fetches the backend-owned illustration timeline. Waits briefly for the
    /// player to report a duration first — the server only needs it to
    /// synthesize an even split for legacy audiobooks with no recorded
    /// offsets — then retries a few times so a transient network error
    /// doesn't leave a finished audiobook stuck on its cover.
    func loadIllustrationTimeline(jobID: String) async {
        for _ in 0..<10 where duration <= 0 {
            guard !Task.isCancelled else { return }
            try? await Task.sleep(for: .milliseconds(500))
        }
        for attempt in 0..<3 {
            guard !Task.isCancelled else { return }
            let durationMS = duration > 0 ? Int(duration * 1000) : nil
            if let dtos = try? await api.jobIllustrations(id: jobID, durationMS: durationMS) {
                illustrationTimeline = Self.illustrationCues(from: dtos)
                return
            }
            try? await Task.sleep(for: .seconds(Double(attempt + 1) * 2))
        }
    }

    nonisolated static func audioOffsetSeconds(fromMS ms: Int?) -> Double? {
        guard let ms, ms > 0 else { return nil }
        return Double(ms) / 1000.0
    }

    /// Audiobook generation has one long narration phase whose localized label is
    /// just "Chapter". For playback surfaces, recover the current chapter title
    /// from the structured plan by looking back through captions already heard.
    var currentAudioBookChapterTitle: String {
        Self.audioBookChapterTitle(
            at: captionLookupTime(playbackTime: currentTime),
            in: cues,
            script: discussion.script
        ) ?? ""
    }

    /// Recomputes the active lyric group at `time` and publishes it only when it
    /// changes. Cheap: `lyricCueGroups` is memoized, so this is an O(groups) scan.
    func updateActiveLyricGroup(at time: Double) {
        guard supportsLyrics else {
            if activeLyricGroupID != nil { activeLyricGroupID = nil }
            return
        }
        let groups = lyricCueGroups
        let t = captionLookupTime(playbackTime: time)
        // Last match, not first: overlapping cues (see `captionCue`) would
        // otherwise pin the highlight to an earlier group past its real end.
        let index = groups.lastIndex(where: { t >= $0.start && t <= $0.end })
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

    func start() {
        configureAudioSession()
        // Decide autoplay from the entry status only — not from which asset
        // `setupPlayer` ends up installing. A generating entry that flips to
        // `.ready` mid-setup still autoplays its final audio.
        autoplayOnEntry = !(discussion.status == .ready || isFinished)
        // Replay persisted transcript for a finished discussion.
        lines = discussion.sortedLines.map {
            LiveLine(speaker: $0.speaker, role: $0.role, text: $0.text, isUser: $0.isUser, done: true,
                     senderUserID: $0.senderUserID, audioURL: $0.audioURL,
                     imageURL: $0.imageURL,
                     audioOffsetSeconds: Self.audioOffsetSeconds(fromMS: $0.startMS),
                     sources: $0.sources, judgementComment: $0.judgementComment)
        }
        if discussion.jobID != nil && !hasPodcastTranscript {
            showTranscriptLoadingIfNeeded()
        }
        if let s = discussion.downloadURLString { downloadURL = URL(string: s) }
        guard let jobID = discussion.jobID else {
            // Pick up cover art that may have been generated in the background
            // after the library handed us this discussion.
            tasks.append(Task { await self.refreshCover() })
            return
        }
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
            tasks.append(Task { await loadIllustrationTimeline(jobID: jobID) })
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
    func resumePlayback() {
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
        // Close any agent bubble still streaming so this message lands after it in
        // its own bubble, and the agent's next words start a fresh bubble below —
        // instead of the earlier bubble continuing to grow past the user message.
        LiveLine.finalizeLastOpenAgentLine(in: &lines)
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

    func removeRejectedUserLine(_ line: LiveLine) {
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

}
