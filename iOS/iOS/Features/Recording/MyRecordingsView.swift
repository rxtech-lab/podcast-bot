import AVFoundation
import SwiftUI

/// Library of on-device recordings, reached from the account menu. A row opens
/// a dedicated player; swipe to delete or rename, and the context menu also
/// offers sharing the file and creating a podcast via the upload-own-audio flow.
struct MyRecordingsView: View {
    @Environment(\.dismiss) private var dismiss
    /// Called when a discussion was created from a recording.
    var onCreated: (Discussion) -> Void = { _ in }

    @State private var renameTarget: RecordingStore.Recording?
    @State private var renameDraft = ""
    @State private var deleteTarget: RecordingStore.Recording?
    @State private var submitTarget: RecordingStore.Recording?

    private var store: RecordingStore { RecordingStore.shared }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                if store.recordings.isEmpty {
                    ContentUnavailableView(
                        String(localized: "No Recordings", comment: "Empty recordings list title"),
                        systemImage: "recordingtape",
                        description: Text(String(localized: "Audio you record in the app is saved here.",
                                                 comment: "Empty recordings list description"))
                    )
                } else {
                    list
                }
            }
            .navigationTitle(String(localized: "My Recordings", comment: "Recordings list title"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                }
            }
        }
        .task { store.loadIfNeeded() }
        .alert(
            String(localized: "Rename Recording", comment: "Rename recording alert title"),
            isPresented: renameBinding,
            presenting: renameTarget
        ) { recording in
            TextField(String(localized: "Title", comment: "Recording title field"), text: $renameDraft)
            Button(String(localized: "Save", comment: "Save rename button")) {
                store.rename(id: recording.id, to: renameDraft)
            }
            Button(String(localized: "Cancel", comment: "Cancel rename button"), role: .cancel) {}
        }
        .confirmationDialog(
            String(localized: "Delete this recording?", comment: "Confirm deleting a recording"),
            isPresented: deleteBinding,
            titleVisibility: .visible,
            presenting: deleteTarget
        ) { recording in
            Button(String(localized: "Delete", comment: "Delete recording button"), role: .destructive) {
                store.delete(id: recording.id)
            }
            Button(String(localized: "Cancel", comment: "Cancel delete button"), role: .cancel) {}
        }
        .sheet(item: $submitTarget) { recording in
            UploadAudioSheet(
                onCreated: { discussion in
                    store.setUploadedDiscussion(id: recording.id, discussionID: discussion.id)
                    dismiss()
                    onCreated(discussion)
                },
                initialFileURL: store.url(for: recording),
                initialFilename: RecordAudioSheet.uploadFilename(for: recording.title)
            )
        }
    }

    private var list: some View {
        List {
            ForEach(store.recordings) { recording in
                row(recording)
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
    }

    private func row(_ recording: RecordingStore.Recording) -> some View {
        NavigationLink {
            RecordingPlayerView(
                recording: recording,
                url: store.url(for: recording)
            ) { discussion in
                store.setUploadedDiscussion(id: recording.id, discussionID: discussion.id)
                dismiss()
                onCreated(discussion)
            }
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "waveform.circle.fill")
                    .font(.title)
                    .foregroundStyle(Theme.accent)
                VStack(alignment: .leading, spacing: 2) {
                    Text(recording.title)
                        .font(.headline)
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(subtitle(for: recording))
                        .font(.subheadline.monospacedDigit())
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(1)
                }
                Spacer()
                if recording.uploadedDiscussionID != nil {
                    Image(systemName: "checkmark.seal.fill")
                        .foregroundStyle(Theme.accent)
                        .accessibilityLabel(String(localized: "Podcast created", comment: "Recording already turned into a podcast"))
                }
            }
            .padding(.vertical, 4)
        }
        .buttonStyle(.plain)
        .listRowBackground(Color.clear)
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button(role: .destructive) {
                deleteTarget = recording
            } label: {
                Label(String(localized: "Delete", comment: "Delete recording swipe action"), systemImage: "trash")
            }
            Button {
                beginRename(recording)
            } label: {
                Label(String(localized: "Rename", comment: "Rename recording swipe action"), systemImage: "pencil")
            }
            .tint(Theme.accent)
        }
        .contextMenu {
            Button {
                submitTarget = recording
            } label: {
                Label(String(localized: "Create Podcast", comment: "Create podcast from recording"), systemImage: "waveform.badge.plus")
            }
            Button {
                beginRename(recording)
            } label: {
                Label(String(localized: "Rename", comment: "Rename recording"), systemImage: "pencil")
            }
            ShareLink(item: store.url(for: recording),
                      preview: SharePreview(recording.title)) {
                Label(String(localized: "Share", comment: "Share recording"), systemImage: "square.and.arrow.up")
            }
            Divider()
            Button(role: .destructive) {
                deleteTarget = recording
            } label: {
                Label(String(localized: "Delete", comment: "Delete recording"), systemImage: "trash")
            }
        }
    }

    private func subtitle(for recording: RecordingStore.Recording) -> String {
        let date = recording.createdAt.formatted(date: .abbreviated, time: .shortened)
        let duration = RecordAudioSheet.timeString(recording.duration)
        let size = UploadAudioCoordinator.formatBytes(recording.sizeBytes)
        return "\(date) · \(duration) · \(size)"
    }

    private func beginRename(_ recording: RecordingStore.Recording) {
        renameDraft = recording.title
        renameTarget = recording
    }

    private var renameBinding: Binding<Bool> {
        Binding(
            get: { renameTarget != nil },
            set: { if !$0 { renameTarget = nil } }
        )
    }

    private var deleteBinding: Binding<Bool> {
        Binding(
            get: { deleteTarget != nil },
            set: { if !$0 { deleteTarget = nil } }
        )
    }
}

/// Full player for one local recording. Podcast creation intentionally reuses
/// the upload-own-audio sheet so its server-driven fields stay authoritative.
struct RecordingPlayerView: View {
    @Environment(PlayerSessionStore.self) private var playerSessions

    let recording: RecordingStore.Recording
    let url: URL
    var onCreated: (Discussion) -> Void = { _ in }

    @State private var playback: RecordingPlayback
    @State private var isScrubbing = false
    @State private var scrubTime: TimeInterval = 0
    @State private var showingSubmit = false

    init(recording: RecordingStore.Recording,
         url: URL,
         onCreated: @escaping (Discussion) -> Void = { _ in }) {
        self.recording = recording
        self.url = url
        self.onCreated = onCreated
        _playback = State(initialValue: RecordingPlayback(
            url: url,
            expectedDuration: recording.duration
        ))
    }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            VStack(spacing: 28) {
                Spacer()
                Image(systemName: "waveform.circle.fill")
                    .font(.system(size: 112))
                    .foregroundStyle(Theme.accent)
                    .accessibilityHidden(true)
                Text(recording.title)
                    .font(.title2.weight(.semibold))
                    .multilineTextAlignment(.center)
                    .lineLimit(2)

                VStack(spacing: 8) {
                    Slider(
                        value: Binding(
                            get: { isScrubbing ? scrubTime : playback.currentTime },
                            set: { scrubTime = $0 }
                        ),
                        in: 0 ... max(playback.duration, 0.1),
                        onEditingChanged: scrubChanged
                    )
                    .tint(Theme.accent)
                    .disabled(playback.duration <= 0)
                    .accessibilityLabel(String(localized: "Playback position", comment: "Recording player seek slider"))

                    HStack {
                        Text(Self.timeString(isScrubbing ? scrubTime : playback.currentTime))
                        Spacer()
                        Text(Self.timeString(playback.duration))
                    }
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(Theme.secondaryText)
                }

                Button {
                    if !playback.isPlaying {
                        playerSessions.stopAll()
                    }
                    playback.toggle()
                } label: {
                    Image(systemName: playback.isPlaying ? "pause.circle.fill" : "play.circle.fill")
                        .font(.system(size: 72))
                        .foregroundStyle(Theme.accent)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(playback.isPlaying
                    ? String(localized: "Pause", comment: "Pause recording playback button")
                    : String(localized: "Play", comment: "Play recording playback button"))
                .accessibilityIdentifier("recording.playPause")
                Spacer()
            }
            .padding(24)
        }
        .navigationTitle(String(localized: "Recording", comment: "Recording player title"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    playback.pause()
                    showingSubmit = true
                } label: {
                    Label(String(localized: "Create Podcast", comment: "Create podcast from recording toolbar button"),
                          systemImage: "waveform.badge.plus")
                }
                .accessibilityIdentifier("recording.createPodcast")
            }
        }
        .sheet(isPresented: $showingSubmit) {
            UploadAudioSheet(
                onCreated: onCreated,
                initialFileURL: url,
                initialFilename: RecordAudioSheet.uploadFilename(for: recording.title)
            )
        }
        .alert(
            String(localized: "Playback Failed", comment: "Recording playback error alert title"),
            isPresented: playbackErrorBinding
        ) {
            Button(String(localized: "OK", comment: "Dismiss error button"), role: .cancel) {}
        } message: {
            Text(playback.errorMessage ?? "")
        }
        .onDisappear { playback.stop() }
    }

    private func scrubChanged(_ editing: Bool) {
        if editing {
            scrubTime = playback.currentTime
            isScrubbing = true
        } else {
            playback.seek(to: scrubTime)
            isScrubbing = false
        }
    }

    private var playbackErrorBinding: Binding<Bool> {
        Binding(
            get: { playback.errorMessage != nil },
            set: { if !$0 { playback.clearError() } }
        )
    }

    static func timeString(_ seconds: TimeInterval) -> String {
        guard seconds.isFinite, seconds >= 0 else { return "0:00" }
        let total = Int(seconds)
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}

/// Local recording playback state with periodic progress updates for the seek
/// slider. The player is created lazily when playback first starts.
@MainActor
@Observable
final class RecordingPlayback {
    private(set) var currentTime: TimeInterval = 0
    private(set) var duration: TimeInterval
    private(set) var isPlaying = false
    private(set) var errorMessage: String?

    private let url: URL
    private var player: AVAudioPlayer?
    private var progressTask: Task<Void, Never>?
    private let delegateProxy = DelegateProxy()

    init(url: URL, expectedDuration: TimeInterval) {
        self.url = url
        duration = max(expectedDuration, 0)
    }

    func toggle() {
        isPlaying ? pause() : play()
    }

    func play() {
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playback, mode: .spokenAudio)
            try session.setActive(true, options: [])
            let player = try loadPlayerIfNeeded()
            if duration > 0, currentTime >= duration - 0.05 {
                currentTime = 0
            }
            player.currentTime = currentTime
            guard player.play() else {
                throw RecordingPlaybackError.couldNotStart
            }
            isPlaying = true
            startProgressUpdates()
        } catch {
            pause()
            errorMessage = error.localizedDescription
        }
    }

    func pause() {
        syncProgress()
        player?.pause()
        isPlaying = false
        progressTask?.cancel()
        progressTask = nil
    }

    func seek(to time: TimeInterval) {
        currentTime = min(max(time, 0), duration)
        player?.currentTime = currentTime
    }

    func stop() {
        progressTask?.cancel()
        progressTask = nil
        player?.stop()
        player = nil
        currentTime = 0
        isPlaying = false
    }

    func clearError() {
        errorMessage = nil
    }

    private func loadPlayerIfNeeded() throws -> AVAudioPlayer {
        if let player { return player }
        let player = try AVAudioPlayer(contentsOf: url)
        duration = max(player.duration, 0)
        delegateProxy.onFinish = { [weak self] in self?.finishedPlaying() }
        player.delegate = delegateProxy
        player.prepareToPlay()
        self.player = player
        return player
    }

    private func startProgressUpdates() {
        progressTask?.cancel()
        progressTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                guard !Task.isCancelled else { return }
                self?.syncProgress()
            }
        }
    }

    private func syncProgress() {
        guard let player else { return }
        currentTime = min(max(player.currentTime, 0), duration)
    }

    private func finishedPlaying() {
        syncProgress()
        currentTime = duration
        isPlaying = false
        progressTask?.cancel()
        progressTask = nil
    }

    private final class DelegateProxy: NSObject, AVAudioPlayerDelegate {
        nonisolated(unsafe) var onFinish: (() -> Void)?

        nonisolated func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
            DispatchQueue.main.async { [weak self] in self?.onFinish?() }
        }
    }

    private enum RecordingPlaybackError: LocalizedError {
        case couldNotStart

        var errorDescription: String? {
            String(localized: "The recording could not be played.", comment: "Recording playback start failure")
        }
    }
}
