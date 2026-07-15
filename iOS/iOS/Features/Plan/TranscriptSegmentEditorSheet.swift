import SwiftUI

struct TranscriptSegmentEditorSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussionID: String
    let speakerOptions: [String]
    let audioDurationMs: Int64
    let onSave: (TranscriptSegmentDTO) async throws -> Void

    @State private var speaker: String
    @State private var startTimestampMs: Int64
    @State private var endTimestampMs: Int64
    @State private var selectedTimestamp = TranscriptTimestampBoundary.start
    @State private var content: String
    @State private var audioPlayer: TranscriptEditorAudioPlayer?
    @State private var isScrubbingAudio = false
    @State private var scrubberTimestampMs: Int64
    @State private var playbackError: String?
    @State private var isSaving = false
    @State private var errorMessage: String?

    init(discussionID: String,
         segment: TranscriptSegmentDTO,
         speakerOptions: [String],
         audioDurationMs: Int64,
         onSave: @escaping (TranscriptSegmentDTO) async throws -> Void) {
        self.discussionID = discussionID
        self.speakerOptions = speakerOptions
        self.audioDurationMs = audioDurationMs
        self.onSave = onSave
        _speaker = State(initialValue: segment.speaker)
        _startTimestampMs = State(initialValue: segment.offsetMs)
        _endTimestampMs = State(initialValue: segment.offsetMs + segment.durationMs)
        _scrubberTimestampMs = State(initialValue: segment.offsetMs)
        _content = State(initialValue: segment.text)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Speaker") {
                    Picker("Speaker", selection: $speaker) {
                        ForEach(allSpeakerOptions, id: \.self) { name in
                            Text(name).tag(name)
                        }
                    }
                }

                Section("Audio") {
                    audioPlayerControls
                    if let playbackError {
                        Text(playbackError)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }

                Section("Time Range") {
                    Picker("Timestamp", selection: $selectedTimestamp) {
                        ForEach(TranscriptTimestampBoundary.allCases) { boundary in
                            Text(boundary.title).tag(boundary)
                        }
                    }
                    .pickerStyle(.segmented)

                    TranscriptTimestampWheel(
                        milliseconds: selectedTimestampBinding,
                        maximumMs: timestampMaximumMs
                    )

                    Button {
                        selectedTimestampBinding.wrappedValue = currentAudioTimestampMs
                    } label: {
                        Label("Use Current Audio Time", systemImage: "scope")
                    }
                    .disabled(audioPlayer?.isLoading == true)

                    LabeledContent("Start", value: transcriptTimestamp(
                        startTimestampMs,
                        includesMilliseconds: true
                    ))
                    LabeledContent("End", value: transcriptTimestamp(
                        endTimestampMs,
                        includesMilliseconds: true
                    ))
                }

                Section("Content") {
                    TextEditor(text: $content)
                        .frame(minHeight: 150)
                }

                if let validationMessage {
                    Text(validationMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
            }
            .navigationTitle("Edit Transcript")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSaving)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: save) {
                        if isSaving { ProgressView() } else { Text("Save") }
                    }
                    .disabled(isSaving || validationMessage != nil)
                }
            }
        }
        .interactiveDismissDisabled(isSaving)
        .onAppear {
            if audioPlayer == nil {
                audioPlayer = TranscriptEditorAudioPlayer(
                    api: APIClient(tokens: auth),
                    discussionID: discussionID,
                    initialTimestampMs: startTimestampMs,
                    audioDurationMs: audioDurationMs
                )
            }
        }
        .onDisappear { audioPlayer?.stop() }
        .alert("Could not save transcript", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    private var allSpeakerOptions: [String] {
        var result: [String] = []
        var seen = Set<String>()
        for name in [speaker] + speakerOptions {
            let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
            let key = trimmed.lowercased()
            guard !trimmed.isEmpty, seen.insert(key).inserted else { continue }
            result.append(trimmed)
        }
        return result
    }

    private var audioPlayerControls: some View {
        TimelineView(.periodic(from: .now, by: 0.1)) { _ in
            audioPlayerControlsContent
        }
    }

    private var audioPlayerControlsContent: some View {
        VStack(spacing: 10) {
            HStack(spacing: 12) {
                Button(action: toggleAudioPlayback) {
                    if audioPlayer?.isLoading == true {
                        ProgressView()
                            .frame(width: 28, height: 28)
                    } else {
                        Image(systemName: audioPlayer?.isPlaying == true
                              ? "pause.circle.fill" : "play.circle.fill")
                            .font(.title)
                    }
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .accessibilityLabel(audioPlayer?.isPlaying == true ? "Pause audio" : "Play audio")

                Slider(
                    value: scrubberBinding,
                    in: 0...Double(max(timestampMaximumMs, 1)),
                    onEditingChanged: scrubberEditingChanged
                )
                .disabled(audioPlayer?.isLoading == true)
            }

            HStack {
                Text(transcriptTimestamp(currentAudioTimestampMs, includesMilliseconds: true))
                Spacer()
                Text(transcriptTimestamp(audioDurationMs))
            }
            .font(.caption.monospacedDigit())
            .foregroundStyle(Theme.secondaryText)
        }
    }

    private var scrubberBinding: Binding<Double> {
        Binding(
            get: { Double(currentAudioTimestampMs) },
            set: { scrubberTimestampMs = Int64($0.rounded()) }
        )
    }

    private var selectedTimestampBinding: Binding<Int64> {
        selectedTimestamp == .start ? $startTimestampMs : $endTimestampMs
    }

    private var currentAudioTimestampMs: Int64 {
        let timestampMs = isScrubbingAudio
            ? scrubberTimestampMs
            : (audioPlayer?.playbackTimestampMs ?? scrubberTimestampMs)
        return min(max(timestampMs, 0), timestampMaximumMs)
    }

    private var timestampMaximumMs: Int64 {
        max(audioDurationMs, endTimestampMs, 1)
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private var validationMessage: String? {
        if speaker.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return String(localized: "Enter a speaker name.",
                          comment: "Transcript segment editor validation when speaker is empty")
        }
        if content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return String(localized: "Enter transcript content.",
                          comment: "Transcript segment editor validation when content is empty")
        }
        if endTimestampMs <= startTimestampMs {
            return String(localized: "The end timestamp must be after the start timestamp.",
                          comment: "Transcript segment editor validation for an inverted time range")
        }
        if audioDurationMs > 0, endTimestampMs > audioDurationMs {
            return String(localized: "The time range exceeds the uploaded audio duration.",
                          comment: "Transcript segment editor validation when the range exceeds the source audio")
        }
        return nil
    }

    private func toggleAudioPlayback() {
        guard let audioPlayer else { return }
        playbackError = nil
        Task { @MainActor in
            do {
                try await audioPlayer.togglePlayback()
            } catch {
                playbackError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func scrubberEditingChanged(_ isEditing: Bool) {
        if isEditing {
            scrubberTimestampMs = currentAudioTimestampMs
            isScrubbingAudio = true
            return
        }
        isScrubbingAudio = false
        guard let audioPlayer else { return }
        playbackError = nil
        Task { @MainActor in
            do {
                try await audioPlayer.seek(to: scrubberTimestampMs)
            } catch {
                playbackError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func save() {
        guard validationMessage == nil else { return }
        let revised = TranscriptSegmentDTO(
            speaker: speaker.trimmingCharacters(in: .whitespacesAndNewlines),
            offsetMs: startTimestampMs,
            durationMs: endTimestampMs - startTimestampMs,
            text: content.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        isSaving = true
        errorMessage = nil
        Task { @MainActor in
            do {
                try await onSave(revised)
                isSaving = false
                dismiss()
            } catch {
                isSaving = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
