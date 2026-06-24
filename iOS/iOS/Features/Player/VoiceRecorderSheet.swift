import AVFoundation
import SwiftUI

/// A voice-message recorder sheet: a language picker, a live waveform + timer, and
/// a review step. Recording starts on appear; tapping stop finishes locally so the
/// user can confirm or edit the transcript before the caller uploads and sends it.
struct VoiceRecorderSheet: View {
    /// The discussion's language; the default transcription locale.
    let defaultLanguage: String
    /// Called with the finished recording when the user stops. The caller uploads
    /// the audio and sends the transcript.
    let onComplete: (VoiceMessageRecorder.Recording) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var recorder: VoiceMessageRecorder
    @State private var supportedLocales: [Locale] = []
    @State private var pendingRecording: VoiceMessageRecorder.Recording?
    @State private var transcriptDraft = ""
    @State private var didComplete = false
    @State private var showingLanguagePicker = false
    @State private var usesAutomaticTranscription = false
    @AppStorage("voiceMessageLocaleID") private var storedLocaleID: String = ""

    private static let automaticTranscriptionID = "__auto__"

    /// This podcast's language — always offered in the picker even if unsupported.
    private var podcastLocale: Locale { Locale(identifier: defaultLanguage) }

    init(defaultLanguage: String,
         onComplete: @escaping (VoiceMessageRecorder.Recording) -> Void) {
        self.defaultLanguage = defaultLanguage
        self.onComplete = onComplete
        let stored = UserDefaults.standard.string(forKey: "voiceMessageLocaleID")
        let startsWithAuto = stored == Self.automaticTranscriptionID
        let identifier = (!startsWithAuto && stored?.isEmpty == false ? stored! : defaultLanguage)
        _recorder = State(initialValue: VoiceMessageRecorder(locale: Locale(identifier: identifier),
                                                             usesCloudTranscription: startsWithAuto))
        _usesAutomaticTranscription = State(initialValue: startsWithAuto)
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
        .presentationDetents([.height(520)])
        .presentationDragIndicator(.visible)
        .task {
            // Authorize first — the supported-locale catalog can be empty until
            // Speech recognition is authorized, which would wrongly disable the picker.
            await recorder.ensureAuthorized()
            supportedLocales = await VoiceMessageRecorder.supportedLocales()
            await recorder.start()
        }
        .onDisappear {
            guard !didComplete else { return }
            recorder.cancel()
            discardPendingRecording()
        }
    }

    // MARK: - Header (cancel + language picker)

    private var header: some View {
        HStack {
            Button {
                cancelAndDismiss()
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
        Button {
            showingLanguagePicker = true
        } label: {
            HStack(spacing: 4) {
                Image(systemName: "globe")
                Text(selectedLanguageLabel)
                    .lineLimit(1)
                Image(systemName: "chevron.down").font(.caption2)
            }
            .font(.subheadline.weight(.medium))
            .foregroundStyle(canPickLanguage ? Theme.accent : Color(uiColor: .secondaryLabel))
        }
        .buttonStyle(.plain)
        .disabled(!canPickLanguage)
        .popover(isPresented: $showingLanguagePicker, arrowEdge: .top) {
            languagePickerPopover
                .presentationCompactAdaptation(.popover)
        }
    }

    private var canPickLanguage: Bool {
        pendingRecording == nil && !recorder.isBusy
    }

    private var languagePickerHeight: CGFloat {
        min(max(CGFloat(localeOptions.count + 1) * 44, 44), 320)
    }

    private var languagePickerPopover: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                languagePickerRow(title: String(localized: "Auto",
                                                comment: "Voice message language option that uses backend transcription"),
                                  isSelected: usesAutomaticTranscription) {
                    selectAutomaticTranscription()
                }
                ForEach(localeOptions, id: \.identifier) { loc in
                    languagePickerRow(title: VoiceMessageRecorder.displayName(for: loc),
                                      isSelected: isSelectedLocale(loc)) {
                        selectLocale(loc)
                    }
                }
            }
        }
        .frame(width: 300, height: languagePickerHeight)
    }

    private var selectedLanguageLabel: String {
        usesAutomaticTranscription
            ? String(localized: "Auto",
                     comment: "Voice message language option that uses backend transcription")
            : VoiceMessageRecorder.displayName(for: recorder.locale)
    }

    private func languagePickerRow(title: String, isSelected: Bool, action: @escaping () -> Void) -> some View {
        Button {
            action()
            showingLanguagePicker = false
        } label: {
            HStack(spacing: 10) {
                Text(title)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                Spacer(minLength: 12)
                Image(systemName: "checkmark")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.accent)
                    .opacity(isSelected ? 1 : 0)
            }
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .padding(.horizontal, 14)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
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

    private func isSelectedLocale(_ loc: Locale) -> Bool {
        !usesAutomaticTranscription && loc.identifier(.bcp47) == recorder.locale.identifier(.bcp47)
    }

    private func selectAutomaticTranscription() {
        guard pendingRecording == nil else { return }
        guard !usesAutomaticTranscription else { return }
        storedLocaleID = Self.automaticTranscriptionID
        usesAutomaticTranscription = true
        recorder.cancel()
        recorder.usesCloudTranscription = true
        Task { await recorder.start() }
    }

    private func selectLocale(_ loc: Locale) {
        guard pendingRecording == nil else { return }
        guard usesAutomaticTranscription || loc.identifier(.bcp47) != recorder.locale.identifier(.bcp47) else { return }
        storedLocaleID = loc.identifier
        // Restart capture with the newly chosen language.
        usesAutomaticTranscription = false
        recorder.cancel()
        recorder.locale = loc
        recorder.usesCloudTranscription = false
        Task { await recorder.start() }
    }

    // MARK: - Content (waveform / timer / transcript / errors)

    @ViewBuilder
    private var content: some View {
        if let pendingRecording {
            transcriptReview(recording: pendingRecording)
        } else {
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
                        Text(usesAutomaticTranscription
                             ? "Auto uses backend transcription after you send."
                             : "This language isn't available on-device — your audio will be transcribed after you send it.",
                             comment: "Shown while recording when backend transcription will be used")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                            .frame(maxWidth: .infinity)
                    }
                }
            }
        }
    }

    // MARK: - Controls

    @ViewBuilder
    private var controls: some View {
        if pendingRecording != nil {
            VStack(spacing: 12) {
                Button {
                    confirmAndSend()
                } label: {
                    Text("Send Voice Message", comment: "Confirms and sends the reviewed voice message")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                }
                .buttonStyle(.borderedProminent)
                .tint(Theme.accent)

                Button(role: .cancel) {
                    cancelAndDismiss()
                } label: {
                    Text("Discard", comment: "Discards a recorded voice message before sending")
                        .frame(maxWidth: .infinity)
                }
            }
        } else {
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
                    stopAndReview()
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
                    Text("Tap to review", comment: "Hint under the stop button in the voice recorder")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .offset(y: 22)
                }
                .padding(.bottom, 16)
            }
        }
    }

    private func transcriptReview(recording: VoiceMessageRecorder.Recording) -> some View {
        VStack(spacing: 14) {
            Image(systemName: "waveform.circle.fill")
                .font(.largeTitle)
                .foregroundStyle(Theme.accent)
            Text(timeString(recording.duration))
                .font(.system(.title3, design: .monospaced).weight(.medium))
            VStack(alignment: .leading, spacing: 8) {
                Text("Transcript", comment: "Label above the reviewed voice-message transcript")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                TextEditor(text: $transcriptDraft)
                    .font(.body)
                    .scrollContentBackground(.hidden)
                    .padding(10)
                    .frame(height: 118)
                    .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .overlay {
                        if transcriptDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                            Text("No transcript yet. You can type one, or send the audio for transcription.",
                                 comment: "Placeholder shown when a reviewed voice message has no transcript")
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.leading)
                                .padding(16)
                                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                                .allowsHitTesting(false)
                        }
                    }
            }
        }
    }

    private func stopAndReview() {
        Task {
            if let recording = await recorder.finish() {
                pendingRecording = recording
                transcriptDraft = recording.transcript
            }
        }
    }

    private func confirmAndSend() {
        guard let pendingRecording else { return }
        didComplete = true
        let transcript = transcriptDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        let recording = VoiceMessageRecorder.Recording(
            fileURL: pendingRecording.fileURL,
            transcript: transcript,
            duration: pendingRecording.duration,
            mimeType: pendingRecording.mimeType
        )
        self.pendingRecording = nil
        onComplete(recording)
        dismiss()
    }

    private func cancelAndDismiss() {
        recorder.cancel()
        discardPendingRecording()
        dismiss()
    }

    private func discardPendingRecording() {
        if let url = pendingRecording?.fileURL {
            try? FileManager.default.removeItem(at: url)
        }
        pendingRecording = nil
        transcriptDraft = ""
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
