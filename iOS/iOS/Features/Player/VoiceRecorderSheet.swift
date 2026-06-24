import AVFoundation
import SwiftUI

/// A voice-message recorder sheet: a language picker, a live waveform + timer, and
/// a record/stop control. Recording starts on appear; tapping stop finishes and
/// hands the recording back to the caller (send-immediately flow).
struct VoiceRecorderSheet: View {
    /// The discussion's language; the default transcription locale.
    let defaultLanguage: String
    /// Called with the finished recording when the user stops. The caller uploads
    /// the audio and sends the transcript.
    let onComplete: (VoiceMessageRecorder.Recording) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var recorder: VoiceMessageRecorder
    @State private var supportedLocales: [Locale] = []
    /// Whether this podcast's language can be transcribed on-device. The language
    /// picker is only enabled when it can.
    @State private var isPodcastLanguageSupported = true
    @AppStorage("voiceMessageLocaleID") private var storedLocaleID: String = ""

    /// This podcast's language — always offered in the picker even if unsupported.
    private var podcastLocale: Locale { Locale(identifier: defaultLanguage) }

    init(defaultLanguage: String,
         onComplete: @escaping (VoiceMessageRecorder.Recording) -> Void) {
        self.defaultLanguage = defaultLanguage
        self.onComplete = onComplete
        let stored = UserDefaults.standard.string(forKey: "voiceMessageLocaleID")
        let identifier = (stored?.isEmpty == false ? stored! : defaultLanguage)
        _recorder = State(initialValue: VoiceMessageRecorder(locale: Locale(identifier: identifier)))
    }

    var body: some View {
        VStack(spacing: 24) {
            header
            Spacer(minLength: 0)
            content
            Spacer(minLength: 0)
            controls
        }
        .padding(24)
        .presentationDetents([.height(420)])
        .presentationDragIndicator(.visible)
        .task {
            // Authorize first — the supported-locale catalog can be empty until
            // Speech recognition is authorized, which would wrongly disable the picker.
            await recorder.ensureAuthorized()
            supportedLocales = await VoiceMessageRecorder.supportedLocales()
            isPodcastLanguageSupported = await VoiceMessageRecorder.isSupported(podcastLocale)
            await recorder.start()
        }
    }

    // MARK: - Header (cancel + language picker)

    private var header: some View {
        HStack {
            Button {
                recorder.cancel()
                dismiss()
            } label: {
                Image(systemName: "xmark")
                    .font(.headline)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            languageMenu
        }
    }

    private var languageMenu: some View {
        Menu {
            ForEach(localeOptions, id: \.identifier) { loc in
                Button {
                    selectLocale(loc)
                } label: {
                    if loc.identifier(.bcp47) == recorder.locale.identifier(.bcp47) {
                        Label(VoiceMessageRecorder.displayName(for: loc), systemImage: "checkmark")
                    } else {
                        Text(VoiceMessageRecorder.displayName(for: loc))
                    }
                }
            }
        } label: {
            HStack(spacing: 4) {
                Image(systemName: "globe")
                Text(VoiceMessageRecorder.displayName(for: recorder.locale))
                    .lineLimit(1)
                Image(systemName: "chevron.down").font(.caption2)
            }
            .font(.subheadline.weight(.medium))
            .foregroundStyle(isPodcastLanguageSupported ? Theme.accent : Color(uiColor: .secondaryLabel))
        }
        // Only let the user change language when this podcast's language is one the
        // device can transcribe; otherwise the picker is disabled.
        .disabled(!isPodcastLanguageSupported || recorder.isBusy)
    }

    /// Every language the device supports, plus this podcast's language (and the
    /// current selection), deduped by BCP-47 identifier.
    private var localeOptions: [Locale] {
        var options = supportedLocales
        for extra in [podcastLocale, recorder.locale]
        where !options.contains(where: { $0.identifier(.bcp47) == extra.identifier(.bcp47) }) {
            options.append(extra)
        }
        return options
    }

    private func selectLocale(_ loc: Locale) {
        guard loc.identifier(.bcp47) != recorder.locale.identifier(.bcp47) else { return }
        storedLocaleID = loc.identifier
        // Restart capture with the newly chosen language.
        recorder.cancel()
        recorder.locale = loc
        Task { await recorder.start() }
    }

    // MARK: - Content (waveform / timer / transcript / errors)

    @ViewBuilder
    private var content: some View {
        switch recorder.phase {
        case .failed(let message):
            VStack(spacing: 12) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.largeTitle)
                    .foregroundStyle(.orange)
                Text(message)
                    .font(.subheadline)
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
            }
        case .preparing:
            VStack(spacing: 12) {
                ProgressView().tint(Theme.accent)
                Text("Preparing language…",
                     comment: "Shown while the on-device transcription model loads")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        default:
            VStack(spacing: 16) {
                WaveformView(level: recorder.level, active: recorder.isRecording)
                    .frame(height: 64)
                Text(timeString(recorder.elapsed))
                    .font(.system(.title2, design: .monospaced).weight(.medium))
                    .foregroundStyle(.primary)
                if !recorder.transcript.isEmpty {
                    Text(recorder.transcript)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .lineLimit(3)
                        .frame(maxWidth: .infinity)
                } else if recorder.isRecording && !recorder.transcribesOnDevice {
                    Text("This language isn't available on-device — your audio will be transcribed after you send it.",
                         comment: "Shown while recording when on-device transcription isn't available and the server will transcribe")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                }
            }
        }
    }

    // MARK: - Controls

    @ViewBuilder
    private var controls: some View {
        switch recorder.phase {
        case .failed:
            Button {
                dismiss()
            } label: {
                Text("Close", comment: "Dismiss the voice recorder after an error")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
            }
            .buttonStyle(.borderedProminent)
            .tint(Theme.accent)
        default:
            Button {
                stopAndSend()
            } label: {
                ZStack {
                    Circle()
                        .fill(Theme.accent)
                        .frame(width: 76, height: 76)
                    Image(systemName: "stop.fill")
                        .font(.title)
                        .foregroundStyle(.white)
                }
            }
            .disabled(!recorder.isRecording)
            .opacity(recorder.isRecording ? 1 : 0.5)
            .overlay(alignment: .bottom) {
                Text("Tap to send", comment: "Hint under the stop button in the voice recorder")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .offset(y: 22)
            }
            .padding(.bottom, 16)
        }
    }

    private func stopAndSend() {
        Task {
            if let recording = await recorder.finish() {
                onComplete(recording)
            }
            dismiss()
        }
    }

    private func timeString(_ t: TimeInterval) -> String {
        let total = Int(t)
        return String(format: "%02d:%02d", total / 60, total % 60)
    }
}

/// A scrolling bar waveform driven by the recorder's live input level.
private struct WaveformView: View {
    let level: CGFloat
    let active: Bool

    private static let barCount = 32
    @State private var samples: [CGFloat] = Array(repeating: 0.04, count: WaveformView.barCount)

    var body: some View {
        GeometryReader { geo in
            let spacing: CGFloat = 3
            let barWidth = max(2, (geo.size.width - spacing * CGFloat(Self.barCount - 1)) / CGFloat(Self.barCount))
            HStack(alignment: .center, spacing: spacing) {
                ForEach(Array(samples.enumerated()), id: \.offset) { _, sample in
                    Capsule()
                        .fill(Theme.accent.opacity(active ? 0.9 : 0.4))
                        .frame(width: barWidth, height: max(3, sample * geo.size.height))
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
        }
        .onChange(of: level) { _, newValue in
            guard active else { return }
            samples.removeFirst()
            samples.append(max(0.04, newValue))
        }
        .animation(.easeOut(duration: 0.08), value: samples)
    }
}
