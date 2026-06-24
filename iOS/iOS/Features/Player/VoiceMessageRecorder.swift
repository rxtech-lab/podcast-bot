import AVFoundation
import Foundation
import Speech
import SwiftUI

/// Records a voice message while transcribing it on-device with the iOS 26
/// `SpeechAnalyzer` / `SpeechTranscriber` API. The recorded `.m4a` is uploaded so
/// other participants can replay it; the transcript is what the agent reads.
///
/// Audio capture runs on a realtime thread (the engine tap), so the few pieces it
/// touches are `nonisolated(unsafe)`; everything user-facing stays on the main
/// actor.
@MainActor
@Observable
final class VoiceMessageRecorder {
    enum Phase: Equatable {
        case idle
        case preparing
        case recording
        case finishing
        case failed(String)
    }

    /// A finished recording, ready to upload + send.
    struct Recording {
        let fileURL: URL
        let transcript: String
        let duration: TimeInterval
        let mimeType: String
    }

    enum RecorderError: LocalizedError {
        case permissionDenied
        case localeUnsupported

        var errorDescription: String? {
            switch self {
            case .permissionDenied:
                return String(localized: "Microphone and speech recognition access are needed to record a voice message.",
                              comment: "Shown when mic/speech permission is denied")
            case .localeUnsupported:
                return String(localized: "This language isn't available for on-device transcription on this device.",
                              comment: "Shown when the chosen voice-message language has no on-device model")
            }
        }
    }

    private(set) var phase: Phase = .idle
    /// Finalized transcript plus the current volatile (in-progress) hypothesis,
    /// for live display while recording.
    private(set) var transcript: String = ""
    /// Normalized 0...1 input level, for the waveform.
    private(set) var level: CGFloat = 0
    private(set) var elapsed: TimeInterval = 0
    /// Whether the current recording is being transcribed on-device. False when
    /// the language has no on-device model (or Speech is unauthorized): the audio
    /// still records and the server transcribes it after upload.
    private(set) var transcribesOnDevice = false
    /// Locale driving the on-device model. Settable while idle.
    var locale: Locale
    /// When true, skip on-device Speech and let the backend transcribe the uploaded audio.
    var usesCloudTranscription: Bool

    var isRecording: Bool { phase == .recording }
    var isBusy: Bool { phase == .preparing || phase == .finishing }

    // MARK: - Audio plumbing (touched on the realtime tap thread)

    private let engine = AVAudioEngine()
    private nonisolated(unsafe) var audioFile: AVAudioFile?
    private nonisolated(unsafe) var converter: AVAudioConverter?
    private nonisolated(unsafe) var analyzerFormat: AVAudioFormat?
    private nonisolated(unsafe) var inputContinuation: AsyncStream<AnalyzerInput>.Continuation?

    private var analyzer: SpeechAnalyzer?
    private var transcriber: SpeechTranscriber?
    private var recognizerTask: Task<Void, Never>?
    private var timer: Timer?
    private var startDate: Date?
    private var fileURL: URL?
    private var finalizedText: String = ""
    /// Whether Speech recognition was authorized. Mic permission is required to
    /// record; Speech is best-effort — without it we record audio-only.
    private var speechAuthorized = false

    init(locale: Locale, usesCloudTranscription: Bool = false) {
        self.locale = locale
        self.usesCloudTranscription = usesCloudTranscription
    }

    // MARK: - Locale discovery

    /// Languages this device can transcribe on-device, sorted by display name.
    static func supportedLocales() async -> [Locale] {
        await SpeechTranscriber.supportedLocales.sorted {
            displayName(for: $0).localizedCaseInsensitiveCompare(displayName(for: $1)) == .orderedAscending
        }
    }

    /// Whether the given language can be transcribed on-device.
    static func isSupported(_ locale: Locale) async -> Bool {
        let target = locale.identifier(.bcp47)
        return await SpeechTranscriber.supportedLocales.contains { $0.identifier(.bcp47) == target }
    }

    /// A human-readable language name in the current UI language.
    static func displayName(for locale: Locale) -> String {
        Locale.current.localizedString(forIdentifier: locale.identifier)
            ?? locale.localizedString(forIdentifier: locale.identifier)
            ?? locale.identifier
    }

    // MARK: - Lifecycle

    /// Requests microphone + speech authorization. Call before enumerating
    /// supported locales: `SpeechTranscriber.supportedLocales` can report nothing
    /// until Speech recognition has been authorized.
    @discardableResult
    func ensureAuthorized() async -> Bool {
        await requestPermissions()
    }

    func start() async {
        switch phase {
        case .idle, .failed: break
        default: return
        }
        phase = .preparing
        transcript = ""
        finalizedText = ""
        level = 0
        elapsed = 0
        transcribesOnDevice = false

        // Only the microphone is required to record. Speech authorization is
        // best-effort: without it we record audio-only and the server transcribes.
        guard await requestPermissions() else {
            phase = .failed(RecorderError.permissionDenied.localizedDescription)
            return
        }
        do {
            try await beginRecording()
            startDate = Date()
            phase = .recording
            startTimer()
        } catch {
            cleanupCapture()
            phase = .failed(error.localizedDescription)
        }
    }

    /// Stops recording and returns the recording (with its transcript), or nil if
    /// nothing usable was captured.
    func finish() async -> Recording? {
        guard phase == .recording else { return nil }
        phase = .finishing
        stopTimer()
        let duration = elapsed

        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        inputContinuation?.finish()
        inputContinuation = nil

        // Flush trailing audio through the analyzer, then drain the result loop.
        try? await analyzer?.finalizeAndFinishThroughEndOfInput()
        await recognizerTask?.value
        recognizerTask = nil
        analyzer = nil
        transcriber = nil
        audioFile = nil
        converter = nil
        deactivateSession()

        let text = transcript.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = fileURL else { phase = .idle; return nil }
        phase = .idle
        return Recording(fileURL: url, transcript: text, duration: duration, mimeType: "audio/m4a")
    }

    /// Stops recording and discards the partial file/transcript.
    func cancel() {
        stopTimer()
        if engine.isRunning {
            engine.inputNode.removeTap(onBus: 0)
            engine.stop()
        }
        recognizerTask?.cancel()
        recognizerTask = nil
        cleanupCapture()
        if let url = fileURL { try? FileManager.default.removeItem(at: url) }
        fileURL = nil
        deactivateSession()
        transcript = ""
        finalizedText = ""
        level = 0
        elapsed = 0
        phase = .idle
    }

    // MARK: - Setup

    private func beginRecording() async throws {
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .voiceChat,
                                options: [.allowBluetooth])
        try session.setActive(true, options: [])

        let inputNode = engine.inputNode
        try? inputNode.setVoiceProcessingEnabled(true)
        let inputFormat = inputNode.outputFormat(forBus: 0)

        // The audio file is always written, so a voice message can be sent (and
        // server-transcribed) even when on-device transcription isn't available.
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("voice-\(UUID().uuidString)")
            .appendingPathExtension("m4a")
        fileURL = url
        let fileSettings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: inputFormat.sampleRate,
            AVNumberOfChannelsKey: inputFormat.channelCount,
            AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
        ]
        audioFile = try AVAudioFile(forWriting: url, settings: fileSettings)

        // On-device transcription is best-effort: skip it when Speech isn't
        // authorized or this language has no on-device model, and fall back to
        // audio-only (the server transcribes after upload) on any setup failure.
        transcribesOnDevice = false
        if !usesCloudTranscription, speechAuthorized, await Self.isSupported(locale) {
            do {
                try await startOnDeviceTranscription(inputFormat: inputFormat)
                transcribesOnDevice = true
            } catch {
                tearDownTranscriber()
            }
        }

        inputNode.installTap(onBus: 0, bufferSize: 4096, format: inputFormat) { [weak self] buffer, _ in
            self?.process(buffer: buffer)
        }
        engine.prepare()
        try engine.start()
    }

    /// Wires up the on-device `SpeechAnalyzer` / `SpeechTranscriber` pipeline and
    /// starts streaming. Throws if the model can't be prepared, in which case the
    /// caller falls back to audio-only capture.
    private func startOnDeviceTranscription(inputFormat: AVAudioFormat) async throws {
        let transcriber = SpeechTranscriber(locale: locale,
                                            transcriptionOptions: [],
                                            reportingOptions: [.volatileResults],
                                            attributeOptions: [])
        self.transcriber = transcriber
        try await ensureModel(for: transcriber)

        let analyzer = SpeechAnalyzer(modules: [transcriber])
        self.analyzer = analyzer
        analyzerFormat = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber])

        if let analyzerFormat {
            converter = AVAudioConverter(from: inputFormat, to: analyzerFormat)
        }

        let (stream, continuation) = AsyncStream<AnalyzerInput>.makeStream()
        inputContinuation = continuation
        recognizerTask = Task { [weak self] in
            guard let self else { return }
            do {
                for try await result in transcriber.results {
                    await self.handleResult(text: result.text, isFinal: result.isFinal)
                }
            } catch {
                // Transcription ended/failed; the audio file still records fine.
            }
        }
        try await analyzer.start(inputSequence: stream)
    }

    /// Tears down only the transcription pipeline, leaving audio capture intact.
    private func tearDownTranscriber() {
        inputContinuation?.finish()
        inputContinuation = nil
        recognizerTask?.cancel()
        recognizerTask = nil
        analyzer = nil
        transcriber = nil
        converter = nil
        analyzerFormat = nil
    }

    private func ensureModel(for transcriber: SpeechTranscriber) async throws {
        let target = locale.identifier(.bcp47)
        let supported = await SpeechTranscriber.supportedLocales
        guard supported.contains(where: { $0.identifier(.bcp47) == target }) else {
            throw RecorderError.localeUnsupported
        }
        let installed = await SpeechTranscriber.installedLocales
        if installed.contains(where: { $0.identifier(.bcp47) == target }) {
            return
        }
        if let request = try await AssetInventory.assetInstallationRequest(supporting: [transcriber]) {
            try await request.downloadAndInstall()
        }
    }

    // MARK: - Result handling

    private func handleResult(text: AttributedString, isFinal: Bool) {
        let chunk = String(text.characters)
        if isFinal {
            finalizedText += chunk
            transcript = finalizedText
        } else {
            transcript = finalizedText + chunk
        }
    }

    // MARK: - Realtime capture (nonisolated)

    private nonisolated func process(buffer: AVAudioPCMBuffer) {
        if let file = audioFile {
            try? file.write(from: buffer)
        }
        if let continuation = inputContinuation {
            if let analyzerFormat, let converter,
               let converted = Self.convert(buffer: buffer, using: converter, to: analyzerFormat) {
                continuation.yield(AnalyzerInput(buffer: converted))
            } else if analyzerFormat == nil {
                continuation.yield(AnalyzerInput(buffer: buffer))
            }
        }
        let level = Self.meterLevel(of: buffer)
        Task { @MainActor [weak self] in self?.level = level }
    }

    private nonisolated static func convert(buffer: AVAudioPCMBuffer,
                                            using converter: AVAudioConverter,
                                            to format: AVAudioFormat) -> AVAudioPCMBuffer? {
        let ratio = format.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 1024
        guard let output = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { return nil }
        var consumed = false
        var error: NSError?
        let status = converter.convert(to: output, error: &error) { _, inStatus in
            if consumed {
                inStatus.pointee = .noDataNow
                return nil
            }
            consumed = true
            inStatus.pointee = .haveData
            return buffer
        }
        if status == .error || output.frameLength == 0 { return nil }
        return output
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

    /// Requests microphone (required) and Speech (best-effort) authorization.
    /// Returns whether the microphone was granted — the only hard requirement to
    /// record. Speech authorization is remembered to decide on-device transcription.
    private func requestPermissions() async -> Bool {
        let mic = await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            AVAudioApplication.requestRecordPermission { granted in cont.resume(returning: granted) }
        }
        guard mic else { return false }
        let speech = await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            SFSpeechRecognizer.requestAuthorization { status in
                cont.resume(returning: status == .authorized)
            }
        }
        speechAuthorized = speech
        return true
    }

    private func cleanupCapture() {
        inputContinuation?.finish()
        inputContinuation = nil
        audioFile = nil
        converter = nil
        analyzerFormat = nil
        analyzer = nil
        transcriber = nil
    }

    private func deactivateSession() {
        try? AVAudioSession.sharedInstance().setActive(false, options: [.notifyOthersOnDeactivation])
        // Restore the playback category the podcast player expects.
        try? AVAudioSession.sharedInstance().setCategory(.playback, mode: .spokenAudio)
    }

    private func startTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self, let start = self.startDate else { return }
                self.elapsed = Date().timeIntervalSince(start)
            }
        }
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }
}
