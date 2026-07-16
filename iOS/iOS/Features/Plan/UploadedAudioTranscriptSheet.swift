import AVFoundation
import Observation
import SwiftUI
import TipKit

struct UploadedAudioTranscriptSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussionID: String
    let presentation: UploadedAudioTranscriptPresentation
    var allowsEditing = true
    var onUpdated: (Discussion) -> Void = { _ in }

    @State private var segments: [TranscriptSegmentDTO]
    @State private var editingSegment: TranscriptSegmentEdit?
    @State private var retimingSegment: TranscriptSegmentEdit?
    @State private var clipPlayer: TranscriptClipPlayer?
    @State private var playbackError: String?

    init(discussionID: String,
         presentation: UploadedAudioTranscriptPresentation,
         allowsEditing: Bool = true,
         onUpdated: @escaping (Discussion) -> Void = { _ in })
    {
        self.discussionID = discussionID
        self.presentation = presentation
        self.allowsEditing = allowsEditing
        self.onUpdated = onUpdated
        _segments = State(initialValue: presentation.segments)
    }

    var body: some View {
        NavigationStack {
            List {
                ForEach(Array(segments.enumerated()), id: \.offset) { index, segment in
                    transcriptRow(index: index, segment: segment)
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            if allowsEditing {
                                Button {
                                    clipPlayer?.stop()
                                    retimingSegment = TranscriptSegmentEdit(index: index, segment: segment)
                                } label: {
                                    Label("Timestamp", systemImage: "clock.arrow.trianglehead.counterclockwise.rotate.90")
                                }
                                .tint(.blue)

                                Button {
                                    clipPlayer?.stop()
                                    editingSegment = TranscriptSegmentEdit(index: index, segment: segment)
                                } label: {
                                    Label("Edit", systemImage: "pencil")
                                }
                                .tint(Theme.accent)
                            }
                        }
                        .popoverTip(
                            index == segments.startIndex && allowsEditing ? TranscriptEditingTip() : nil,
                            arrowEdge: .top
                        )
                }
            }
            .navigationTitle(presentation.title.isEmpty ? "Transcript" : presentation.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if allowsEditing {
                    ToolbarItem(placement: .primaryAction) {
                        Button(action: retimeFromStart) {
                            Label(
                                "Edit From Start",
                                systemImage: "clock.arrow.trianglehead.counterclockwise.rotate.90"
                            )
                        }
                        .disabled(segments.isEmpty)
                        .accessibilityIdentifier("transcript.editFromStart")
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: dismiss.callAsFunction) {
                        Image(systemName: "xmark")
                    }
                    .accessibilityIdentifier("transcript.done")
                }
            }
        }
        .sheet(item: $editingSegment) { edit in
            TranscriptSegmentEditorSheet(
                discussionID: discussionID,
                segment: edit.segment,
                speakerOptions: transcriptSpeakerOptions,
                audioDurationMs: presentation.durationMs
            ) { revised in
                try await updateSegments([
                    TranscriptSegmentUpdate(index: edit.index, segment: revised)
                ])
            }
        }
        .sheet(item: $retimingSegment) { edit in
            TranscriptSegmentRetimeSheet(
                discussionID: discussionID,
                segments: segments,
                initialIndex: edit.index,
                audioDurationMs: presentation.durationMs
            ) { updates in
                try await updateSegments(updates)
            }
        }
        .interactiveDismissDisabled()
        .alert("Could not play audio", isPresented: playbackErrorBinding) {
            Button("OK", role: .cancel) { playbackError = nil }
        } message: {
            Text(playbackError ?? "")
        }
        .onDisappear { clipPlayer?.stop() }
    }

    private var playbackErrorBinding: Binding<Bool> {
        Binding(
            get: { playbackError != nil },
            set: { if !$0 { playbackError = nil } }
        )
    }

    private var transcriptSpeakerOptions: [String] {
        var seen = Set<String>()
        return (presentation.speakers + segments.map(\.speaker)).compactMap { rawName in
            let name = rawName.trimmingCharacters(in: .whitespacesAndNewlines)
            let key = name.lowercased()
            guard !name.isEmpty, seen.insert(key).inserted else { return nil }
            return name
        }
    }

    private func transcriptRow(index: Int, segment: TranscriptSegmentDTO) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text("\(index + 1)")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white)
                    .frame(width: 24, height: 24)
                    .background(Theme.accent, in: .circle)

                Text("\(transcriptTimestamp(segment.offsetMs))–\(transcriptTimestamp(segment.offsetMs + segment.durationMs)) · \(segment.speaker)")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(.primary)
                    .fixedSize(horizontal: false, vertical: true)

                Spacer(minLength: 8)

                Button {
                    play(index: index, segment: segment)
                } label: {
                    if clipPlayer?.loadingIndex == index {
                        ProgressView()
                            .controlSize(.small)
                    } else {
                        Image(systemName: isPlaying(index) ? "pause.circle.fill" : "play.circle.fill")
                            .font(.title2)
                    }
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .accessibilityLabel(isPlaying(index) ? "Pause audio clip" : "Play audio clip")
            }

            if !segment.text.isEmpty {
                Text(segment.text)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.vertical, 6)
        .contentShape(.rect)
    }

    private func isPlaying(_ index: Int) -> Bool {
        clipPlayer?.activeIndex == index && clipPlayer?.isPlaying == true
    }

    private func retimeFromStart() {
        guard let first = segments.enumerated().min(by: {
            $0.element.offsetMs < $1.element.offsetMs
        }) else { return }
        clipPlayer?.stop()
        retimingSegment = TranscriptSegmentEdit(index: first.offset, segment: first.element)
    }

    private func play(index: Int, segment: TranscriptSegmentDTO) {
        let player = clipPlayer ?? TranscriptClipPlayer(
            api: APIClient(tokens: auth),
            discussionID: discussionID
        )
        clipPlayer = player
        Task { @MainActor in
            do {
                try await player.toggle(index: index, segment: segment)
            } catch {
                playbackError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    @MainActor
    private func updateSegments(_ updates: [TranscriptSegmentUpdate]) async throws {
        let updated = try await APIClient(tokens: auth).updateTranscriptSegments(
            id: discussionID,
            updates: updates
        )
        if let authoritative = updated.script?.transcriptSegments {
            segments = authoritative
        } else {
            for update in updates where segments.indices.contains(update.index) {
                segments[update.index] = update.segment
            }
        }
        clipPlayer?.stop()
        onUpdated(updated)
    }
}
