import AVFoundation
import Observation
import SwiftUI

struct PlanSnapshot {
    let title: String
    let topic: String
    let isAudioBook: Bool
    let isUploadedAudio: Bool
    let uploadedAudioDurationMs: Int64
    let transcriptSegments: [TranscriptSegmentDTO]
    let style: String
    let background: String
    let chapters: [PlanChapterSnapshot]
    let people: [PlanPersonSnapshot]
    let sources: [PlanSourceSnapshot]

    init(discussion: Discussion) {
        title = discussion.title
        topic = discussion.topic
        isAudioBook = discussion.script?.type == "audio-book"
        isUploadedAudio = discussion.script?.type == "uploaded-audio"
        uploadedAudioDurationMs = discussion.script?.uploadedAudioDurationMs ?? 0
        transcriptSegments = discussion.script?.transcriptSegments ?? []
        style = PlanSnapshot.displayStyle(for: discussion.script)
        background = PlanSnapshot.displayBackground(for: discussion.script)
        chapters = PlanSnapshot.displayChapters(for: discussion.script)
        people = PlanSnapshot.displayPeople(for: discussion.script)
        sources = discussion.sortedSources
    }

    /// Builds a snapshot from a persisted plan edit-turn (the plan as it stood at
    /// that point in the chat). `topic` carries over from the owning discussion,
    /// which a per-turn snapshot doesn't store.
    init(turn: DiscussionEditTurnDTO, topic: String) {
        self.title = turn.script?.title ?? ""
        self.topic = topic
        self.isAudioBook = turn.script?.type == "audio-book"
        self.isUploadedAudio = turn.script?.type == "uploaded-audio"
        self.uploadedAudioDurationMs = turn.script?.uploadedAudioDurationMs ?? 0
        self.transcriptSegments = turn.script?.transcriptSegments ?? []
        self.style = PlanSnapshot.displayStyle(for: turn.script)
        self.background = PlanSnapshot.displayBackground(for: turn.script)
        self.chapters = PlanSnapshot.displayChapters(for: turn.script)
        self.people = PlanSnapshot.displayPeople(for: turn.script)
        self.sources = (turn.sources ?? turn.script?.sources ?? []).map {
            PlanSourceSnapshot(
                title: $0.title,
                urlString: $0.url,
                snippet: $0.snippet ?? "",
                markdown: $0.markdown ?? ""
            )
        }
    }

    private static func displayBackground(for script: ScriptDTO?) -> String {
        guard let script else { return "" }
        if script.type == "audio-book" {
            let summary = script.background ?? ""
            return summary
        }
        return script.background ?? ""
    }

    private static func displayStyle(for script: ScriptDTO?) -> String {
        guard script?.type == "audio-book" else { return "" }
        let raw = script?.audioBookStyle?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !raw.isEmpty else { return "" }
        return raw
            .split(separator: "-")
            .map { $0.prefix(1).uppercased() + $0.dropFirst() }
            .joined(separator: " ")
    }

    private static func displayChapters(for script: ScriptDTO?) -> [PlanChapterSnapshot] {
        if script?.type == "uploaded-audio" {
            // Transcript segments reuse the chapter presentation: the row title
            // is the segment's timestamp + speaker, the summary is its text.
            return (script?.transcriptSegments ?? []).enumerated().map { idx, segment in
                PlanChapterSnapshot(
                    number: idx + 1,
                    title: "\(clockLabel(ms: segment.offsetMs)) · \(segment.speaker)",
                    summary: segment.text
                )
            }
        }
        guard script?.type == "audio-book" else { return [] }
        return (script?.audioBookChapters ?? []).enumerated().compactMap { idx, chapter in
            let title = chapter.title.trimmingCharacters(in: .whitespacesAndNewlines)
            let summary = chapter.summary.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !title.isEmpty || !summary.isEmpty else { return nil }
            return PlanChapterSnapshot(number: idx + 1, title: title, summary: summary)
        }
    }

    private static func clockLabel(ms: Int64) -> String {
        let total = ms / 1000
        let hours = total / 3600
        let minutes = (total % 3600) / 60
        let seconds = total % 60
        if hours > 0 {
            return String(format: "%d:%02d:%02d", hours, minutes, seconds)
        }
        return String(format: "%d:%02d", minutes, seconds)
    }

    private static func displayPeople(for script: ScriptDTO?) -> [PlanPersonSnapshot] {
        var people: [PlanPersonSnapshot] = []
        if script?.type == "uploaded-audio" {
            var seenNames = Set<String>()
            for segment in script?.transcriptSegments ?? [] {
                let key = normalizedPersonName(segment.speaker)
                guard !key.isEmpty, !seenNames.contains(key) else { continue }
                people.append(PlanPersonSnapshot(name: segment.speaker, aspect: "", isHost: people.isEmpty))
                seenNames.insert(key)
            }
            return people
        }
        if script?.type == "audio-book" {
            var seenNames = Set<String>()
            if let host = script?.audioBookHost, !host.name.isEmpty {
                people.append(PlanPersonSnapshot(name: host.name, aspect: "Narrator", isHost: true))
                seenNames.insert(normalizedPersonName(host.name))
            }
            for speaker in script?.audioBookSpeakers ?? [] {
                let key = normalizedPersonName(speaker.name)
                guard !key.isEmpty, !seenNames.contains(key) else { continue }
                people.append(PlanPersonSnapshot(
                    name: speaker.name,
                    aspect: speaker.description ?? speaker.gender ?? "",
                    isHost: false
                ))
                seenNames.insert(key)
            }
            return people
        }
        if let host = script?.host, !host.name.isEmpty {
            people.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        people.append(contentsOf: (script?.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        return people
    }

    private static func normalizedPersonName(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
    }
}

struct PlanChapterSnapshot: Identifiable, Hashable {
    let number: Int
    let title: String
    let summary: String

    var id: Int { number }
}

struct PlanChaptersPresentation: Identifiable {
    let id = UUID()
    let title: String
    let chapters: [PlanChapterSnapshot]
}

struct UploadedAudioTranscriptPresentation: Identifiable {
    let id = UUID()
    let title: String
    let durationMs: Int64
    let segments: [TranscriptSegmentDTO]

    init(snapshot: PlanSnapshot) {
        title = snapshot.title
        durationMs = snapshot.uploadedAudioDurationMs
        segments = snapshot.transcriptSegments
    }
}

struct PlanPersonSnapshot: Identifiable {
    let id = UUID()
    let name: String
    let aspect: String
    let isHost: Bool

    init(name: String, aspect: String, isHost: Bool) {
        self.name = name
        self.aspect = aspect
        self.isHost = isHost
    }
}

struct PlanSourceSnapshot: Identifiable {
    var id: String { urlString.isEmpty ? title : urlString }
    let title: String
    let urlString: String
    let snippet: String
    let markdown: String

    init(title: String, urlString: String, snippet: String, markdown: String = "") {
        self.title = title
        self.urlString = urlString
        self.snippet = snippet
        self.markdown = markdown
    }

    var url: URL? { URL(string: urlString) }
    var displayTitle: String { title.isEmpty ? urlString : title }
    var detailMarkdown: String {
        let content = markdown.trimmingCharacters(in: .whitespacesAndNewlines)
        if !content.isEmpty { return content }
        return snippet.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

struct PlanSnapshotCard: View {
    let label: String
    let snapshot: PlanSnapshot
    var onSourcesTapped: (() -> Void)? = nil
    var onChaptersTapped: (() -> Void)? = nil
    /// When set, a "Models" button is shown in the Panelists header that opens
    /// the per-speaker model editor. nil hides it (e.g. read-only previews).
    var onEditModels: (() -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 6) {
                Text(label.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                if !snapshot.title.isEmpty {
                    Text(snapshot.title)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(.primary)
                }
                if !snapshot.topic.isEmpty {
                    Text("Topic: \(snapshot.topic)")
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }

            if snapshot.isAudioBook {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Style")
                        .font(.headline)
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: "slider.horizontal.3")
                            .foregroundStyle(Theme.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(snapshot.style.isEmpty ? "Not selected" : snapshot.style)
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(.primary)
                            Text(styleDetailText)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                }
            }

            if !snapshot.background.isEmpty {
                MarkdownText(snapshot.background)
                    .font(.body)
                    .foregroundStyle(Theme.secondaryText)
            }

            if !snapshot.chapters.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text(snapshot.isUploadedAudio ? "Transcript" : "Chapters")
                        .font(.headline)
                    if let onChaptersTapped {
                        Button(action: onChaptersTapped) {
                            chaptersSentence
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("plan.chapters")
                    } else {
                        chaptersSentence
                    }
                }
            }

            if !snapshot.people.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        Text(peopleHeading).font(.headline)
                        if let onEditModels {
                            Spacer()
                            Button(action: onEditModels) {
                                Label("Models", systemImage: "cpu")
                                    .font(.subheadline.weight(.semibold))
                            }
                            .buttonStyle(.plain)
                            .foregroundStyle(Theme.accent)
                            .accessibilityIdentifier("plan.editModels")
                        }
                    }
                    ForEach(snapshot.people) { person in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 8) {
                                Image(systemName: person.isHost ? "person.wave.2.fill" : "person.fill")
                                    .foregroundStyle(Theme.accent)
                                    .frame(width: 20)
                                Text(person.name)
                                    .font(.body.weight(.semibold))
                            }
                            if !person.aspect.isEmpty {
                                Text(person.aspect)
                                    .font(.subheadline)
                                    .foregroundStyle(Theme.secondaryText)
                                    .padding(.leading, 28)
                            }
                        }
                    }
                }
            }

            if !snapshot.sources.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Sources")
                        .font(.headline)
                    if let onSourcesTapped {
                        Button(action: onSourcesTapped) {
                            sourcesSentence
                        }
                        .buttonStyle(.plain)
                    } else {
                        sourcesSentence
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var peopleHeading: String {
        if snapshot.isUploadedAudio { return "Speakers" }
        return snapshot.chapters.isEmpty ? "Panelists" : "Voices"
    }

    private var chaptersSentence: some View {
        HStack(spacing: 8) {
            Image(systemName: snapshot.isUploadedAudio ? "text.quote" : "book.closed")
                .foregroundStyle(Theme.accent)
            Text(chapterSentenceText)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
            if onChaptersTapped != nil {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
    }

    private var sourcesSentence: some View {
        HStack(spacing: 8) {
            Image(systemName: "doc.text.magnifyingglass")
                .foregroundStyle(Theme.accent)
            Text(sourceSentenceText)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
            if onSourcesTapped != nil {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
    }

    private var chapterSentenceText: String {
        let count = snapshot.chapters.count
        if snapshot.isUploadedAudio {
            return "\(count) transcript segment\(count == 1 ? "" : "s") — tap to review."
        }
        return "\(count) chapter section\(count == 1 ? "" : "s") in this audiobook plan."
    }

    private var styleDetailText: String {
        switch snapshot.style.lowercased() {
        case "news":
            return "A main presenter leads with supporting voices."
        case "conversational":
            return "One main speaker leads while others ask, clarify, or respond."
        case "podcast":
            return "A host-led podcast format with supporting speakers."
        case "meeting":
            return "A facilitator guides participant questions and discussion."
        case "audiobook":
            return "A classic narrator-led audiobook format."
        default:
            return "The agent should choose the production format for this audiobook."
        }
    }

    private var sourceSentenceText: String {
        let count = snapshot.sources.count
        return "Found \(count) source\(count == 1 ? "" : "s") for this plan."
    }
}

struct AudioBookChaptersSheet: View {
    @Environment(\.dismiss) private var dismiss

    let presentation: PlanChaptersPresentation

    var body: some View {
        NavigationStack {
            List {
                ForEach(presentation.chapters) { chapter in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text("\(chapter.number)")
                                .font(.caption.weight(.bold))
                                .foregroundStyle(.white)
                                .frame(width: 24, height: 24)
                                .background(Theme.accent, in: .circle)
                            Text(chapter.title)
                                .font(.body.weight(.semibold))
                                .foregroundStyle(.primary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if !chapter.summary.isEmpty {
                            Text(chapter.summary)
                                .font(.subheadline)
                                .foregroundStyle(Theme.secondaryText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(.vertical, 6)
                }
            }
            .navigationTitle(presentation.title.isEmpty ? "Chapters" : presentation.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// Transcript-specific companion to `AudioBookChaptersSheet`. Each row can
/// replay exactly its source-audio time range; editable plan screens also add a
/// trailing swipe action for correcting the speaker, timing, and content.
struct UploadedAudioTranscriptSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussionID: String
    let presentation: UploadedAudioTranscriptPresentation
    var allowsEditing = true
    var onUpdated: (Discussion) -> Void = { _ in }

    @State private var segments: [TranscriptSegmentDTO]
    @State private var editingSegment: TranscriptSegmentEdit?
    @State private var clipPlayer: TranscriptClipPlayer?
    @State private var playbackError: String?

    init(discussionID: String,
         presentation: UploadedAudioTranscriptPresentation,
         allowsEditing: Bool = true,
         onUpdated: @escaping (Discussion) -> Void = { _ in }) {
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
                                    editingSegment = TranscriptSegmentEdit(index: index, segment: segment)
                                } label: {
                                    Label("Edit", systemImage: "pencil")
                                }
                                .tint(Theme.accent)
                            }
                        }
                }
            }
            .navigationTitle(presentation.title.isEmpty ? "Transcript" : presentation.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .sheet(item: $editingSegment) { edit in
            TranscriptSegmentEditorSheet(
                segment: edit.segment,
                audioDurationMs: presentation.durationMs
            ) { revised in
                let updated = try await APIClient(tokens: auth).updateTranscriptSegment(
                    id: discussionID,
                    index: edit.index,
                    segment: revised
                )
                if let authoritative = updated.script?.transcriptSegments {
                    segments = authoritative
                } else if segments.indices.contains(edit.index) {
                    segments[edit.index] = revised
                }
                clipPlayer?.stop()
                onUpdated(updated)
            }
        }
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
}

private struct TranscriptSegmentEdit: Identifiable {
    let index: Int
    let segment: TranscriptSegmentDTO
    var id: Int { index }
}

@MainActor
@Observable
private final class TranscriptClipPlayer {
    private let api: APIClient
    private let discussionID: String
    private var playbackURL: URL?
    private var player: AVPlayer?
    private var endMonitorTask: Task<Void, Never>?
    private var reachedEnd = false

    private(set) var activeIndex: Int?
    private(set) var loadingIndex: Int?
    private(set) var isPlaying = false

    init(api: APIClient, discussionID: String) {
        self.api = api
        self.discussionID = discussionID
    }

    func toggle(index: Int, segment: TranscriptSegmentDTO) async throws {
        if activeIndex == index, let player {
            if isPlaying {
                player.pause()
                isPlaying = false
                return
            }
            if reachedEnd {
                await seek(player, to: segment.offsetMs)
                reachedEnd = false
                monitorPlaybackEnd(index: index, endMs: segment.offsetMs + segment.durationMs)
            }
            player.play()
            isPlaying = true
            return
        }

        loadingIndex = index
        defer { loadingIndex = nil }
        let url: URL
        if let playbackURL {
            url = playbackURL
        } else {
            url = try await api.uploadedAudioPlaybackURL(id: discussionID)
            playbackURL = url
        }

        stop(clearActiveIndex: false)
        let item = AVPlayerItem(url: url)
        item.forwardPlaybackEndTime = CMTime(
            value: segment.offsetMs + segment.durationMs,
            timescale: 1000
        )
        let player = AVPlayer(playerItem: item)
        self.player = player
        activeIndex = index
        reachedEnd = false
        await seek(player, to: segment.offsetMs)
        monitorPlaybackEnd(index: index, endMs: segment.offsetMs + segment.durationMs)
        player.play()
        isPlaying = true
    }

    func stop(clearActiveIndex: Bool = true) {
        player?.pause()
        player = nil
        endMonitorTask?.cancel()
        endMonitorTask = nil
        isPlaying = false
        reachedEnd = false
        if clearActiveIndex { activeIndex = nil }
    }

    private func seek(_ player: AVPlayer, to milliseconds: Int64) async {
        await player.seek(
            to: CMTime(value: milliseconds, timescale: 1000),
            toleranceBefore: .zero,
            toleranceAfter: .zero
        )
    }

    private func monitorPlaybackEnd(index: Int, endMs: Int64) {
        endMonitorTask?.cancel()
        endMonitorTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                guard !Task.isCancelled, let self,
                      self.activeIndex == index,
                      let seconds = self.player?.currentTime().seconds,
                      seconds.isFinite else { continue }
                if seconds * 1000 >= Double(max(endMs - 25, 0)) {
                    self.isPlaying = false
                    self.reachedEnd = true
                    return
                }
            }
        }
    }
}

private struct TranscriptSegmentEditorSheet: View {
    @Environment(\.dismiss) private var dismiss

    let audioDurationMs: Int64
    let onSave: (TranscriptSegmentDTO) async throws -> Void

    @State private var speaker: String
    @State private var startTimestamp: String
    @State private var endTimestamp: String
    @State private var content: String
    @State private var isSaving = false
    @State private var errorMessage: String?

    init(segment: TranscriptSegmentDTO,
         audioDurationMs: Int64,
         onSave: @escaping (TranscriptSegmentDTO) async throws -> Void) {
        self.audioDurationMs = audioDurationMs
        self.onSave = onSave
        _speaker = State(initialValue: segment.speaker)
        _startTimestamp = State(initialValue: transcriptTimestamp(segment.offsetMs, includesMilliseconds: true))
        _endTimestamp = State(initialValue: transcriptTimestamp(segment.offsetMs + segment.durationMs, includesMilliseconds: true))
        _content = State(initialValue: segment.text)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Speaker") {
                    TextField("Speaker name", text: $speaker)
                        .textInputAutocapitalization(.words)
                }

                Section("Time Range") {
                    LabeledContent("Start") {
                        TextField("0:00.000", text: $startTimestamp)
                            .keyboardType(.numbersAndPunctuation)
                            .multilineTextAlignment(.trailing)
                    }
                    LabeledContent("End") {
                        TextField("0:00.000", text: $endTimestamp)
                            .keyboardType(.numbersAndPunctuation)
                            .multilineTextAlignment(.trailing)
                    }
                    Text("Use M:SS.mmm or H:MM:SS.mmm.")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
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
        .alert("Could not save transcript", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private var parsedRange: (start: Int64, end: Int64)? {
        guard let start = parseTranscriptTimestamp(startTimestamp),
              let end = parseTranscriptTimestamp(endTimestamp) else { return nil }
        return (start, end)
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
        guard let range = parsedRange else {
            return String(localized: "Enter valid start and end timestamps.",
                          comment: "Transcript segment editor validation for malformed timestamps")
        }
        if range.end <= range.start {
            return String(localized: "The end timestamp must be after the start timestamp.",
                          comment: "Transcript segment editor validation for an inverted time range")
        }
        if audioDurationMs > 0, range.end > audioDurationMs {
            return String(localized: "The time range exceeds the uploaded audio duration.",
                          comment: "Transcript segment editor validation when the range exceeds the source audio")
        }
        return nil
    }

    private func save() {
        guard validationMessage == nil, let range = parsedRange else { return }
        let revised = TranscriptSegmentDTO(
            speaker: speaker.trimmingCharacters(in: .whitespacesAndNewlines),
            offsetMs: range.start,
            durationMs: range.end - range.start,
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

private func transcriptTimestamp(_ milliseconds: Int64,
                                 includesMilliseconds: Bool = false) -> String {
    let clamped = max(milliseconds, 0)
    let totalSeconds = clamped / 1000
    let hours = totalSeconds / 3600
    let minutes = (totalSeconds % 3600) / 60
    let seconds = totalSeconds % 60
    let fraction = clamped % 1000
    if hours > 0 {
        return includesMilliseconds
            ? String(format: "%d:%02d:%02d.%03d", hours, minutes, seconds, fraction)
            : String(format: "%d:%02d:%02d", hours, minutes, seconds)
    }
    return includesMilliseconds
        ? String(format: "%d:%02d.%03d", minutes, seconds, fraction)
        : String(format: "%d:%02d", minutes, seconds)
}

private func parseTranscriptTimestamp(_ raw: String) -> Int64? {
    let parts = raw.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: ":")
    guard (1 ... 3).contains(parts.count) else { return nil }
    let values = parts.compactMap { Double($0) }
    guard values.count == parts.count, values.allSatisfy({ $0 >= 0 }) else { return nil }

    let seconds: Double
    switch values.count {
    case 1:
        seconds = values[0]
    case 2:
        guard values[1] < 60 else { return nil }
        seconds = values[0] * 60 + values[1]
    case 3:
        guard values[1] < 60, values[2] < 60 else { return nil }
        seconds = values[0] * 3600 + values[1] * 60 + values[2]
    default:
        return nil
    }
    guard seconds.isFinite, seconds <= Double(Int64.max) / 1000 else { return nil }
    return Int64((seconds * 1000).rounded())
}
