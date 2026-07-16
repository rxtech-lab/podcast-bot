import SwiftUI

/// Records podcast audio: a circular live waveform, elapsed timer, and
/// pause/resume controls. Finished recordings are already durable on disk (the
/// recorder writes straight into `RecordingStore`); the review step then lets
/// the user rename and either create a podcast through the upload-own-audio
/// flow or keep the recording local.
///
/// Stopping only pauses the recorder — the file stays open so the review step
/// can offer "Continue Recording". The file is closed lazily the first time it
/// is actually needed (create podcast, close, or dismiss).
struct RecordAudioSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(PlayerSessionStore.self) private var playerSessions
    /// Called when a discussion was created from the recording.
    var onCreated: (Discussion) -> Void = { _ in }

    @State private var recorder = PodcastRecorder()
    @State private var entry: RecordingStore.Recording?
    /// Showing the review step. The recorder may still be paused (file open,
    /// so recording can continue) until `finished` is set.
    @State private var reviewing = false
    @State private var finished: PodcastRecorder.Finished?
    @State private var titleDraft = ""
    @State private var showingDiscardConfirm = false
    @State private var showingSubmit = false
    /// Set when the sheet dismisses on purpose, so `onDisappear` cleanup only
    /// runs for interactive dismissals.
    @State private var completed = false

    private var store: RecordingStore { RecordingStore.shared }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                content
            }
            .navigationTitle(reviewing
                ? String(localized: "Recording Ready", comment: "Record audio review step title")
                : String(localized: "Record Audio", comment: "Record audio sheet title"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button {
                        requestClose()
                    } label: {
                        Image(systemName: "xmark")
                    }
                    .disabled(recorder.isBusy)
                    .accessibilityLabel(String(localized: "Cancel recording", comment: "Close button on the record audio sheet"))
                }
            }
        }
        .interactiveDismissDisabled((!reviewing && recorder.hasContent) || recorder.isBusy)
        .confirmationDialog(
            String(localized: "Discard this recording?", comment: "Confirm discarding an audio recording"),
            isPresented: $showingDiscardConfirm,
            titleVisibility: .visible
        ) {
            Button(String(localized: "Discard", comment: "Discard recording button"), role: .destructive) {
                discardAndDismiss()
            }
            Button(String(localized: "Keep Recording", comment: "Keep recording button"), role: .cancel) {}
        }
        .sheet(isPresented: $showingSubmit) {
            if let entry {
                UploadAudioSheet(
                    onCreated: { discussion in
                        store.setUploadedDiscussion(id: entry.id, discussionID: discussion.id)
                        completed = true
                        dismiss()
                        onCreated(discussion)
                    },
                    initialFileURL: store.url(for: entry),
                    initialFilename: Self.uploadFilename(for: titleDraft)
                )
            }
        }
        .task { await beginSession() }
        .onDisappear {
            guard !completed else { return }
            if reviewing {
                // Dismissed from the review step: close the file (the recorder
                // may still be paused) and keep the recording saved.
                Task {
                    _ = await ensureFinished()
                    commitRename()
                }
            } else if finished == nil {
                // Interactive dismissal before anything was captured: drop the
                // pending entry. (With content, dismiss is disabled above.)
                recorder.cancel()
                if let entry { store.delete(id: entry.id) }
            }
        }
        .preventsIdleSleep()
    }

    // MARK: - Content

    @ViewBuilder
    private var content: some View {
        if case let .failed(message) = recorder.phase {
            failedView(message: message)
        } else if reviewing {
            reviewView
        } else {
            recordingView
        }
    }

    private var recordingView: some View {
        VStack(spacing: 24) {
            Spacer()
            CircularWaveformView(level: recorder.level, isActive: recorder.isRecording)
                .frame(width: 280, height: 280)
            VStack(spacing: 8) {
                Text(Self.timeString(recorder.elapsed))
                    .font(.system(size: 44, weight: .medium, design: .monospaced))
                    .contentTransition(.numericText())
                Text(statusText)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            controls
                .padding(.bottom, 32)
        }
        .padding(24)
    }

    private var statusText: String {
        switch recorder.phase {
        case .preparing:
            return String(localized: "Preparing...", comment: "Recorder preparing status")
        case .paused:
            return String(localized: "Paused", comment: "Recorder paused status")
        case .recording:
            return String(localized: "Recording", comment: "Recorder active status")
        default:
            return ""
        }
    }

    private var controls: some View {
        HStack(spacing: 40) {
            Button {
                if recorder.isPaused {
                    recorder.resume()
                } else {
                    recorder.pause()
                }
            } label: {
                Image(systemName: recorder.isPaused ? "arrow.clockwise.circle.fill" : "pause.fill")
                    .font(.title2.weight(.semibold))
                    .frame(width: 60, height: 60)
            }
            .buttonStyle(.glass)
            .clipShape(Circle())
            .disabled(!recorder.isRecording && !recorder.isPaused)
            .accessibilityLabel(recorder.isPaused
                ? String(localized: "Resume recording", comment: "Resume button")
                : String(localized: "Pause recording", comment: "Pause button"))

            Button(action: finishTapped) {
                ZStack {
                    Circle()
                        .fill(Theme.accent)
                        .frame(width: 76, height: 76)
                    Image(systemName: "stop.fill")
                        .font(.title2.weight(.bold))
                        .foregroundStyle(.white)
                }
            }
            .buttonStyle(.plain)
            .disabled(!recorder.hasContent)
            .opacity(recorder.hasContent ? 1 : 0.4)
            .accessibilityLabel(String(localized: "Finish recording", comment: "Finish button"))

            // Balances the pause button so the stop button stays centered.
            Color.clear.frame(width: 60, height: 60)
        }
    }

    private var reviewDuration: TimeInterval {
        finished?.duration ?? recorder.elapsed
    }

    /// File size shown on the review step. While the recorder is only paused
    /// the file is still open, so probe the bytes written so far on disk.
    private var reviewSizeBytes: Int64 {
        if let finished { return finished.sizeBytes }
        guard let entry else { return 0 }
        let attributes = try? FileManager.default.attributesOfItem(atPath: store.url(for: entry).path)
        return (attributes?[.size] as? NSNumber)?.int64Value ?? 0
    }

    private var reviewView: some View {
        VStack(spacing: 24) {
            Spacer()
            Image(systemName: "waveform.circle.fill")
                .font(.system(size: 72))
                .foregroundStyle(Theme.accent)
            VStack(spacing: 12) {
                TextField(String(localized: "Recording title", comment: "Recording title field placeholder"),
                          text: $titleDraft)
                    .font(.headline)
                    .multilineTextAlignment(.center)
                    .textFieldStyle(.plain)
                    .padding(12)
                    .glassEffect(in: .rect(cornerRadius: 14))
                    .onSubmit(commitRename)
                Text("\(Self.timeString(reviewDuration)) · \(UploadAudioCoordinator.formatBytes(reviewSizeBytes))")
                    .font(.subheadline.monospacedDigit())
                    .foregroundStyle(Theme.secondaryText)
                Text(String(localized: "Saved to My Recordings. Create a podcast from it now, or keep it for later.",
                            comment: "Recording review step hint"))
                    .font(.footnote)
                    .foregroundStyle(Theme.secondaryText)
                    .multilineTextAlignment(.center)
            }
            Spacer()
            VStack(spacing: 12) {
                Button(action: createPodcastTapped) {
                    Label(String(localized: "Create Podcast", comment: "Create podcast from recording button"),
                          systemImage: "waveform.badge.plus")
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 6)
                }
                .buttonStyle(.glassProminent)
                .tint(Theme.accent)
                .disabled(recorder.isBusy)

                // The recorder is only paused until the file is truly closed,
                // so more audio can still be appended.
                if finished == nil, recorder.isPaused {
                    Button {
                        reviewing = false
                        recorder.resume()
                    } label: {
                        Label(String(localized: "Continue Recording", comment: "Resume recording from the review step"),
                              systemImage: "mic.badge.plus")
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 6)
                    }
                    .buttonStyle(.glass)
                }

                Button(role: .destructive) {
                    showingDiscardConfirm = true
                } label: {
                    Text(String(localized: "Discard", comment: "Discard finished recording button"))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 6)
                }
                .buttonStyle(.glass)
            }
            .padding(.bottom, 16)
        }
        .padding(24)
    }

    private func failedView(message: String) -> some View {
        VStack(spacing: 16) {
            Spacer()
            Image(systemName: "exclamationmark.triangle")
                .font(.system(size: 44))
                .foregroundStyle(.secondary)
            Text(message)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
            Spacer()
            Button {
                closeAfterFailure()
            } label: {
                Text(String(localized: "Close", comment: "Close recorder after failure"))
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.glass)
            .padding(.bottom, 16)
        }
        .padding(24)
    }

    // MARK: - Actions

    private func beginSession() async {
        // The mic and the podcast player can't share the audio session.
        playerSessions.stopAll()
        store.loadIfNeeded()
        let entry = store.createPending()
        self.entry = entry
        titleDraft = entry.title
        await recorder.start(writingTo: store.url(for: entry))
    }

    /// Stop only pauses the recorder: the file stays open so the review step
    /// can continue the recording.
    private func finishTapped() {
        guard recorder.hasContent else { return }
        if recorder.isRecording { recorder.pause() }
        reviewing = true
    }

    /// Closes the audio file (if it is still open) and finalizes the store
    /// entry. Safe to call repeatedly.
    @discardableResult
    private func ensureFinished() async -> PodcastRecorder.Finished? {
        if let finished { return finished }
        guard let result = await recorder.finish() else { return nil }
        finished = result
        if let entry { store.finalize(id: entry.id, duration: result.duration) }
        return result
    }

    private func createPodcastTapped() {
        Task {
            guard await ensureFinished() != nil else {
                discardAndDismiss()
                return
            }
            commitRename()
            showingSubmit = true
        }
    }

    private func requestClose() {
        if reviewing {
            // The recording is already saved; closing keeps it.
            Task {
                await ensureFinished()
                commitRename()
                completed = true
                dismiss()
            }
        } else if recorder.hasContent {
            showingDiscardConfirm = true
        } else {
            discardAndDismiss()
        }
    }

    private func discardAndDismiss() {
        recorder.cancel()
        if let entry { store.delete(id: entry.id) }
        completed = true
        dismiss()
    }

    /// After a mid-recording failure the captured audio is kept when usable
    /// (the launch sweep in `RecordingStore` finalizes it); otherwise the
    /// pending entry is dropped.
    private func closeAfterFailure() {
        if let entry {
            if recorder.elapsed > 0.5 {
                store.finalize(id: entry.id, duration: recorder.elapsed)
            } else {
                store.delete(id: entry.id)
            }
        }
        completed = true
        dismiss()
    }

    private func commitRename() {
        guard let entry else { return }
        store.rename(id: entry.id, to: titleDraft)
    }

    // MARK: - Formatting

    static func timeString(_ interval: TimeInterval) -> String {
        let total = Int(interval)
        let hours = total / 3600
        let minutes = (total % 3600) / 60
        let seconds = total % 60
        if hours > 0 {
            return String(format: "%02d:%02d:%02d", hours, minutes, seconds)
        }
        return String(format: "%02d:%02d", minutes, seconds)
    }

    /// Filename sent to the server; it titles the episode, so derive it from
    /// the user's title.
    static func uploadFilename(for title: String) -> String {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let base = trimmed.isEmpty ? "Recording" : trimmed
        let sanitized = base.map { char -> Character in
            if char.isLetter || char.isNumber || char == " " || char == "-" || char == "_" {
                return char
            }
            return "-"
        }
        return String(sanitized) + ".m4a"
    }
}
