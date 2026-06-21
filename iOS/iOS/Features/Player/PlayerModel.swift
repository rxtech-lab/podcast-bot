import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftData

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

/// Drives the live podcast experience: streams the audio (live HLS while
/// generating, final MP3 when ready), surfaces the per-agent transcript over the
/// job WebSocket, polls live captions, and tracks job completion.
@MainActor
@Observable
final class PlayerModel {
    nonisolated static let liveCaptionLeadSeconds = 1.5

    let discussion: Discussion
    private let api: APIClient
    private let context: ModelContext
    private let username: String

    let player = AVPlayer()
    var isPlaying = false
    var currentTime: Double = 0
    var duration: Double = 0
    var elapsedTime: Double = 0
    var remainingTime: Double = 0
    var caption: String = ""
    var phaseLabel: String = ""
    var statusText: String = ""
    var isFinished = false
    var downloadURL: URL?
    var lines: [LiveLine] = []
    var transcriptScrollToken: TranscriptScrollToken {
        TranscriptScrollToken.make(for: lines)
    }

    private var socket: JobSocket?
    private var timeObserver: Any?
    private var remoteCommandTargets: [Any] = []
    private var usesLiveCaptionTiming = false
    private var cues: [VTTCue] = []
    private var tasks: [Task<Void, Never>] = []

    init(discussion: Discussion, api: APIClient, context: ModelContext, username: String) {
        self.discussion = discussion
        self.api = api
        self.context = context
        self.username = username
    }

    func start() {
        configureAudioSession()
        // Replay persisted transcript for a finished discussion.
        lines = discussion.sortedLines.map {
            LiveLine(speaker: $0.speaker, role: $0.role, text: $0.text, isUser: $0.isUser, done: true)
        }
        if let s = discussion.downloadURLString { downloadURL = URL(string: s) }
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
            play()
        }
    }

    func send(_ text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let line = LiveLine(speaker: username, role: "user", text: trimmed, isUser: true, done: true)
        lines.append(line)
        persistIfNeeded(line: line)
        Task { await socket?.send(text: trimmed, username: username) }
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
            Task { @MainActor in self?.play() }
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
        player.play()
        isPlaying = true
        updateNowPlayingInfo()
    }

    private func pause() {
        player.pause()
        isPlaying = false
        updateNowPlayingInfo()
    }

    private func seek(to seconds: Double) {
        guard seconds.isFinite, seconds >= 0 else { return }
        let time = CMTime(seconds: seconds, preferredTimescale: 600)
        player.seek(to: time)
        currentTime = seconds
        updateCaption(at: seconds)
        updateNowPlayingInfo()
    }

    private func setupPlayer(jobID: String) async {
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
        let asset = AVURLAsset(url: url, options: options)
        let item = AVPlayerItem(asset: asset)
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
        play()
    }

    private func updateCaption(at time: Double) {
        caption = Self.captionText(in: cues, at: captionLookupTime(playbackTime: time))
        updateNowPlayingInfo()
    }

    private func captionLookupTime(playbackTime: Double) -> Double {
        Self.captionLookupTime(playbackTime: playbackTime, usesLiveCaptionTiming: usesLiveCaptionTiming)
    }

    nonisolated static func captionLookupTime(playbackTime: Double, usesLiveCaptionTiming: Bool) -> Double {
        usesLiveCaptionTiming ? playbackTime + liveCaptionLeadSeconds : playbackTime
    }

    nonisolated static func captionText(in cues: [VTTCue], at time: Double) -> String {
        cues.first(where: { time >= $0.start && time <= $0.end })?.text ?? ""
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

    private func persistIfNeeded(line: LiveLine, startMs: Int = 0) {
        persistIfNeeded(speaker: line.speaker,
                        role: line.role,
                        text: line.text,
                        startMs: startMs,
                        isUser: line.isUser)
    }

    private func persistIfNeeded(speaker: String, role: String, text: String, startMs: Int = 0, isUser: Bool) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        let exists = discussion.sortedLines.contains {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser == isUser
        }
        guard !exists else { return }
        let model = TranscriptLine(ordinal: discussion.sortedLines.count,
                                   speaker: speaker, role: role,
                                   text: trimmed, startMs: startMs, isUser: isUser)
        model.discussion = discussion
        context.insert(model)
        discussion.updatedAt = Date()
        try? context.save()
    }

    private func loadTranscriptSnapshot(jobID: String) async {
        guard let snapshot = try? await api.jobTranscript(id: jobID) else { return }
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
                updateJobProgress(elapsedMS: job.elapsed_ms, remainingMS: job.remaining_ms)
                if job.isDone {
                    isFinished = true
                    discussion.status = .ready
                    if let url = job.download_url {
                        downloadURL = URL(string: url)
                        discussion.downloadURLString = url
                    }
                    discussion.updatedAt = Date()
                    try? context.save()
                    return
                } else if job.isError {
                    isFinished = true
                    statusText = job.error ?? "Generation failed"
                    discussion.status = .failed
                    try? context.save()
                    return
                }
            }
            try? await Task.sleep(for: .seconds(2))
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

    private func updateNowPlayingInfo() {
        var info: [String: Any] = [
            MPMediaItemPropertyTitle: nowPlayingTitle,
            MPMediaItemPropertyArtist: nowPlayingSubtitle,
            MPMediaItemPropertyAlbumTitle: "Debate Bot",
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
        if !discussion.title.isEmpty { return discussion.title }
        if !discussion.topic.isEmpty { return discussion.topic }
        return "Podcast"
    }

    private var nowPlayingSubtitle: String {
        if !caption.isEmpty { return caption }
        if !phaseLabel.isEmpty { return phaseLabel }
        if !statusText.isEmpty { return statusText }
        return "Debate Bot"
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
