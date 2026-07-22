import AVFoundation
import Foundation
import SwiftUI

/// Records podcast audio to an `.m4a` file with pause/resume support. Unlike
/// `VoiceMessageRecorder` there is no on-device transcription and no voice
/// processing (echo cancellation / AGC would degrade podcast fidelity) — the
/// server transcribes the audio after upload.
///
/// Audio capture runs on a realtime thread (the engine tap), so the pieces it
/// touches are `nonisolated(unsafe)`; everything user-facing stays on the main
/// actor. The file is written directly to its final destination so a recording
/// survives even if the app dies mid-capture.
@MainActor
@Observable
final class PodcastRecorder {
    enum Phase: Equatable {
        case idle
        case preparing
        case recording
        case paused
        case finishing
        case failed(String)
    }

    /// A finished recording, already durable on disk.
    struct Finished {
        let fileURL: URL
        let duration: TimeInterval
        let sizeBytes: Int64
        let mimeType: String
    }

    enum RecorderError: LocalizedError {
        case permissionDenied

        var errorDescription: String? {
            switch self {
            case .permissionDenied:
                return String(localized: "Microphone access is needed to record audio.",
                              comment: "Shown when mic permission is denied for podcast recording")
            }
        }
    }

    private(set) var phase: Phase = .idle
    /// Normalized 0...1 input level, for the waveform.
    private(set) var level: CGFloat = 0
    private(set) var elapsed: TimeInterval = 0

    var isRecording: Bool { phase == .recording }
    var isPaused: Bool { phase == .paused }
    var isBusy: Bool { phase == .preparing || phase == .finishing }
    /// Whether enough audio has been captured that dismissing should confirm.
    var hasContent: Bool { elapsed > 0.5 && (isRecording || isPaused) }

    // MARK: - Audio plumbing (touched on the realtime tap thread)

    private let engine = AVAudioEngine()
    private nonisolated(unsafe) var audioFile: AVAudioFile?

    private var timer: Timer?
    /// Elapsed time folded in from previous record segments (across pauses).
    private var accumulated: TimeInterval = 0
    /// Start of the current record segment; nil while paused/idle.
    private var segmentStart: Date?
    private var fileURL: URL?
    private var observers: [NSObjectProtocol] = []

    // MARK: - Lifecycle

    /// Requests microphone authorization.
    @discardableResult
    func ensureAuthorized() async -> Bool {
        await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            AVAudioApplication.requestRecordPermission { granted in cont.resume(returning: granted) }
        }
    }

    /// Starts recording into `url`, writing audio directly to that destination.
    func start(writingTo url: URL) async {
        switch phase {
        case .idle, .failed: break
        default: return
        }
        phase = .preparing
        level = 0
        elapsed = 0
        accumulated = 0
        segmentStart = nil

        guard await ensureAuthorized() else {
            phase = .failed(RecorderError.permissionDenied.localizedDescription)
            return
        }
        do {
            try beginRecording(at: url)
            registerSessionObservers()
            segmentStart = Date()
            phase = .recording
            startTimer()
        } catch {
            cleanupCapture()
            deactivateSession()
            phase = .failed(error.localizedDescription)
        }
    }

    /// Pauses capture, keeping the file open so `resume()` appends to it.
    func pause() {
        guard phase == .recording else { return }
        engine.pause()
        foldElapsed()
        stopTimer()
        level = 0
        phase = .paused
    }

    /// Resumes capture after `pause()`.
    func resume() {
        guard phase == .paused else { return }
        do {
            #if !os(macOS)
            // An interruption (phone call) may have deactivated the session.
            try AVAudioSession.sharedInstance().setActive(true, options: [])
            #endif
            try engine.start()
            segmentStart = Date()
            phase = .recording
            startTimer()
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    /// Stops recording, closes the file, and returns the finished recording (or
    /// nil if nothing usable was captured).
    func finish() async -> Finished? {
        guard phase == .recording || phase == .paused else { return nil }
        phase = .finishing
        foldElapsed()
        stopTimer()
        let duration = accumulated

        stopEngine()
        audioFile = nil
        removeSessionObservers()
        deactivateSession()

        guard let url = fileURL, duration > 0 else { phase = .idle; return nil }
        let attributes = try? FileManager.default.attributesOfItem(atPath: url.path)
        let size = (attributes?[.size] as? NSNumber)?.int64Value ?? 0
        phase = .idle
        return Finished(fileURL: url, duration: duration, sizeBytes: size, mimeType: "audio/mp4")
    }

    /// Stops recording and deletes the partial file.
    func cancel() {
        stopTimer()
        stopEngine()
        audioFile = nil
        removeSessionObservers()
        if let url = fileURL { try? FileManager.default.removeItem(at: url) }
        fileURL = nil
        deactivateSession()
        level = 0
        elapsed = 0
        accumulated = 0
        segmentStart = nil
        phase = .idle
    }

    // MARK: - Setup

    private func beginRecording(at url: URL) throws {
        #if !os(macOS)
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .default, options: [.allowBluetoothHFP])
        try session.setActive(true, options: [])
        #endif

        let inputNode = engine.inputNode
        let inputFormat = inputNode.outputFormat(forBus: 0)

        fileURL = url
        let fileSettings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: inputFormat.sampleRate,
            AVNumberOfChannelsKey: inputFormat.channelCount,
            AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue,
        ]
        audioFile = try AVAudioFile(forWriting: url, settings: fileSettings)

        inputNode.installTap(onBus: 0, bufferSize: 4096, format: inputFormat) { [weak self] buffer, _ in
            self?.process(buffer: buffer)
        }
        engine.prepare()
        try engine.start()
    }

    private func stopEngine() {
        if engine.isRunning || phase == .paused {
            engine.inputNode.removeTap(onBus: 0)
            engine.stop()
        }
    }

    private func cleanupCapture() {
        if engine.isRunning {
            engine.inputNode.removeTap(onBus: 0)
            engine.stop()
        }
        audioFile = nil
    }

    // MARK: - Session events

    private func registerSessionObservers() {
        #if !os(macOS)
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVAudioSession.interruptionNotification,
                                            object: nil, queue: .main) { [weak self] note in
            guard let raw = note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                  let type = AVAudioSession.InterruptionType(rawValue: raw) else { return }
            Task { @MainActor [weak self] in
                // Pause on interruption; the user resumes manually so the mic
                // never hot-restarts behind their back.
                if type == .began { self?.pause() }
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.routeChangeNotification,
                                            object: nil, queue: .main) { [weak self] note in
            guard let raw = note.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt,
                  let reason = AVAudioSession.RouteChangeReason(rawValue: raw) else { return }
            Task { @MainActor [weak self] in
                // The input device went away (headset unplugged) — pause rather
                // than silently switching microphones mid-recording.
                if reason == .oldDeviceUnavailable { self?.pause() }
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification,
                                            object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self, self.phase == .recording || self.phase == .paused else { return }
                self.foldElapsed()
                self.stopTimer()
                self.stopEngine()
                self.audioFile = nil
                self.phase = .failed(String(localized: "Recording stopped because audio services were reset. The captured audio was kept.",
                                            comment: "Shown when the system audio daemon restarts mid-recording"))
            }
        })
        #endif
    }

    private func removeSessionObservers() {
        for observer in observers { NotificationCenter.default.removeObserver(observer) }
        observers = []
    }

    // MARK: - Realtime capture (nonisolated)

    private nonisolated func process(buffer: AVAudioPCMBuffer) {
        if let file = audioFile {
            try? file.write(from: buffer)
        }
        let level = Self.meterLevel(of: buffer)
        Task { @MainActor [weak self] in
            guard let self, self.isRecording else { return }
            self.level = level
        }
    }

    private nonisolated static func meterLevel(of buffer: AVAudioPCMBuffer) -> CGFloat {
        guard let channelData = buffer.floatChannelData else { return 0 }
        let channelCount = Int(buffer.format.channelCount)
        let frameLength = Int(buffer.frameLength)
        guard frameLength > 0, channelCount > 0 else { return 0 }
        var sum: Float = 0
        for ch in 0 ..< channelCount {
            let data = channelData[ch]
            for i in 0 ..< frameLength {
                let sample = data[i]
                sum += sample * sample
            }
        }
        let rms = sqrt(sum / Float(frameLength * channelCount))
        return min(1, max(0, CGFloat(rms) * 6))
    }

    // MARK: - Helpers

    private func foldElapsed() {
        if let start = segmentStart {
            accumulated += Date().timeIntervalSince(start)
        }
        segmentStart = nil
        elapsed = accumulated
    }

    private func deactivateSession() {
        #if !os(macOS)
        try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
        // Restore the playback category the podcast player expects.
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
        #endif
    }

    private func startTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self, let start = self.segmentStart else { return }
                self.elapsed = self.accumulated + Date().timeIntervalSince(start)
            }
        }
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }
}
