import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

extension PlayerModel {
    // MARK: - Captions

    func pollCaptions(jobID: String) async {
        while !Task.isCancelled && !isFinished {
            if let vtt = try? await api.liveSubtitles(id: jobID, language: presentationLanguage) {
                cues = Self.parseVTT(vtt)
            }
            try? await Task.sleep(for: .seconds(3))
        }
    }

    func loadFinalCaptions(jobID: String) async {
        if let vtt = try? await api.liveSubtitles(id: jobID, language: presentationLanguage) {
            cues = Self.parseVTT(vtt)
            // Seed the active lyric group so the list scrolls to the right place
            // on first appearance, before the periodic time observer fires.
            updateActiveLyricGroup(at: currentTime)
        }
    }

    /// Switches the presentation bundle while leaving the AVPlayer item and
    /// playback position untouched. The server performs field-level fallback
    /// when a translated artifact is absent.
    func switchPresentationLanguage(to language: String?) async throws {
        let normalized = language?.trimmingCharacters(in: .whitespacesAndNewlines)
        let sourceLanguage = discussion.mainLanguage ?? discussion.language
        let requested = (normalized?.isEmpty == false && normalized != sourceLanguage) ? normalized : nil
        let fresh: Discussion
        if let shareToken, !shareToken.isEmpty {
            fresh = try await api.joinViaShare(token: shareToken, language: requested)
        } else {
            fresh = try await api.playerDiscussion(id: discussion.id, language: requested)
        }
        presentationLanguage = requested
        discussion = fresh
        lines = fresh.sortedLines.compactMap { dto in
            guard Self.hasDisplayablePayload(dto) else { return nil }
            return LiveLine(speaker: dto.speaker, role: dto.role, text: dto.text,
                            isUser: dto.isUser, done: true,
                            senderUserID: dto.senderUserID, audioURL: dto.audioURL,
                            imageURL: dto.imageURL,
                            audioOffsetSeconds: Self.audioOffsetSeconds(fromMS: dto.startMS),
                            sources: dto.sources, judgementComment: dto.judgementComment)
        }
        if let jobID = fresh.jobID,
           let vtt = try? await api.liveSubtitles(id: jobID, language: requested) {
            cues = Self.parseVTT(vtt)
            updateActiveLyricGroup(at: currentTime)
        }
        uiActionsRefreshVersion += 1
        await refreshCoverAssets()
        updateNowPlayingInfo()
    }

    // MARK: - Job status

    func pollStatus(jobID: String) async {
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

    func refreshDiscussionAfterJobDone(job: JobStatusDTO) async {
        if let fresh = try? await api.discussion(id: discussion.id, includeEditTurns: false) {
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
    func refreshCover() async {
        if let fresh = try? await api.discussion(id: discussion.id, includeEditTurns: false) {
            discussion.summary = fresh.summary
            listenForJobUpdatesIfNeeded()
            if let cover = fresh.cover, cover.hasImage || cover.hasGradient {
                discussion.cover = cover
            }
        }
        await refreshCoverAssets()
    }

    func refreshCoverAssets() async {
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

    /// Adopts the tint palette from the artwork actually on screen: the timed
    /// illustration when one is showing, falling back to the cover palette
    /// otherwise. Shares `coverColorsSourceKey` with `loadCoverColors`, so
    /// repeated calls within one illustration's window are free and returning
    /// to the cover re-samples it.
    func adoptArtworkColors(from illustrationURL: URL?) async {
        guard let illustrationURL else {
            await loadCoverColors()
            return
        }
        let key = illustrationURL.absoluteString
        guard key != coverColorsSourceKey else { return }
        guard let (data, _) = try? await URLSession.shared.data(from: illustrationURL) else { return }
        let colors = await Task.detached(priority: .utility) {
            CoverPalette.dominantColors(from: data, count: 2)
        }.value
        guard colors.count >= 2 else { return }
        coverColorsSourceKey = key
        coverColors = colors
    }

    func loadNowPlayingArtwork() async {
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

    nonisolated static func gradientArtworkImage(startHex: String, endHex: String) -> UIImage {
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

    func switchToFinalAudioIfNeeded(jobID: String) async {
        guard usesLiveCaptionTiming else {
            playerLog.debug("switchToFinalAudio: already on final timing, nothing to do")
            return
        }
        let resumeTime = currentTime
        let shouldResume = isPlaying || autoplayRequested
        playerLog.debug("switchToFinalAudio: swapping live HLS -> final audio (resume=\(resumeTime, privacy: .public), shouldResume=\(shouldResume, privacy: .public))")
        await loadFinalCaptions(jobID: jobID)
        tasks.append(Task { await loadIllustrationTimeline(jobID: jobID) })

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

    func updateJobProgress(elapsedMS: Int?, remainingMS: Int?) {
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

    func updateNowPlayingInfo() {
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

    var nowPlayingTitle: String {
        if !discussion.displayTitle.isEmpty { return discussion.displayTitle }
        if !discussion.topic.isEmpty { return discussion.topic }
        return AppStringLiteral.stationNameRaw
    }

    var nowPlayingSubtitle: String {
        if !caption.isEmpty {
            if !captionSpeaker.isEmpty { return "\(captionSpeaker): \(caption)" }
            return caption
        }
        if !phaseLabel.isEmpty { return phaseLabel }
        if !statusText.isEmpty { return statusText }
        return AppStringLiteral.appTitleRaw
    }

    var nowPlayingDuration: Double {
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

    nonisolated static func parseTimestamp(_ raw: String) -> Double? {
        let s = raw.trimmingCharacters(in: .whitespaces)
        let comps = s.components(separatedBy: ":")
        guard !comps.isEmpty else { return nil }
        var seconds = 0.0
        for part in comps { seconds = seconds * 60 + (Double(part.replacingOccurrences(of: ",", with: ".")) ?? 0) }
        return seconds
    }
}
