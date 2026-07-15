import SwiftUI

struct TranscriptRetimeState: Equatable {
    var startTimestampMs: Int64
    var endTimestampMs: Int64
    let audioDurationMs: Int64

    init(segment: TranscriptSegmentDTO, audioDurationMs: Int64) {
        startTimestampMs = segment.offsetMs
        endTimestampMs = segment.offsetMs + segment.durationMs
        self.audioDurationMs = audioDurationMs
    }

    var maximumTimestampMs: Int64 {
        audioDurationMs > 0 ? audioDurationMs : max(endTimestampMs, 1)
    }

    var isValid: Bool {
        startTimestampMs >= 0
            && endTimestampMs > startTimestampMs
            && (audioDurationMs <= 0 || endTimestampMs <= audioDurationMs)
    }

    func timestamp(for boundary: TranscriptTimestampBoundary) -> Int64 {
        boundary == .start ? startTimestampMs : endTimestampMs
    }

    mutating func set(_ boundary: TranscriptTimestampBoundary, to timestampMs: Int64) {
        let clamped = clampedTimestamp(timestampMs)
        if boundary == .start {
            startTimestampMs = clamped
        } else {
            endTimestampMs = clamped
        }
    }

    func nudgedTimestamp(from timestampMs: Int64, by deltaMs: Int64) -> Int64 {
        clampedTimestamp(timestampMs + deltaMs)
    }

    func revisedSegment(from segment: TranscriptSegmentDTO) -> TranscriptSegmentDTO {
        TranscriptSegmentDTO(
            speaker: segment.speaker,
            offsetMs: startTimestampMs,
            durationMs: endTimestampMs - startTimestampMs,
            text: segment.text
        )
    }

    private func clampedTimestamp(_ timestampMs: Int64) -> Int64 {
        guard audioDurationMs > 0 else { return max(timestampMs, 0) }
        return min(max(timestampMs, 0), audioDurationMs)
    }
}

struct TranscriptRetimeSequence: Equatable {
    private var savedSegments: [TranscriptSegmentDTO]
    private(set) var segments: [TranscriptSegmentDTO]
    let orderedIndices: [Int]
    private(set) var currentIndex: Int

    init(segments: [TranscriptSegmentDTO], initialIndex: Int) {
        precondition(segments.indices.contains(initialIndex))
        savedSegments = segments
        self.segments = segments
        orderedIndices = segments.indices.sorted { lhs, rhs in
            let left = segments[lhs]
            let right = segments[rhs]
            if left.offsetMs != right.offsetMs {
                return left.offsetMs < right.offsetMs
            }
            return lhs < rhs
        }
        currentIndex = initialIndex
    }

    var currentSegment: TranscriptSegmentDTO {
        segments[currentIndex]
    }

    var previousSegment: TranscriptSegmentDTO? {
        adjacentSegment(positionDelta: -1)
    }

    var nextSegment: TranscriptSegmentDTO? {
        adjacentSegment(positionDelta: 1)
    }

    var canMovePrevious: Bool {
        adjacentIndex(positionDelta: -1) != nil
    }

    var canMoveNext: Bool {
        adjacentIndex(positionDelta: 1) != nil
    }

    var pendingIndices: [Int] {
        orderedIndices.filter { segments[$0] != savedSegments[$0] }
    }

    var pendingUpdates: [TranscriptSegmentUpdate] {
        pendingIndices.map {
            TranscriptSegmentUpdate(index: $0, segment: segments[$0])
        }
    }

    mutating func replaceCurrent(with segment: TranscriptSegmentDTO) {
        segments[currentIndex] = segment
    }

    mutating func markSaved(at index: Int) {
        savedSegments[index] = segments[index]
    }

    mutating func movePrevious() -> Bool {
        move(positionDelta: -1)
    }

    mutating func moveNext() -> Bool {
        move(positionDelta: 1)
    }

    private func adjacentSegment(positionDelta: Int) -> TranscriptSegmentDTO? {
        guard let index = adjacentIndex(positionDelta: positionDelta) else { return nil }
        return segments[index]
    }

    private func adjacentIndex(positionDelta: Int) -> Int? {
        guard let position = orderedIndices.firstIndex(of: currentIndex) else { return nil }
        let adjacentPosition = position + positionDelta
        guard orderedIndices.indices.contains(adjacentPosition) else { return nil }
        return orderedIndices[adjacentPosition]
    }

    private mutating func move(positionDelta: Int) -> Bool {
        guard let index = adjacentIndex(positionDelta: positionDelta) else { return false }
        currentIndex = index
        return true
    }
}

private enum TranscriptRetimeNavigationDirection {
    case forward
    case backward

    var insertionEdge: Edge {
        self == .forward ? .bottom : .top
    }

    var removalEdge: Edge {
        self == .forward ? .top : .bottom
    }
}

// Haptics are inert in the simulator, but the generators still round-trip
// through media services; on a loaded CI host running parallel simulator
// clones that call can block the main thread once the editor's AVPlayer has
// an active audio session. Compile them out of simulator builds entirely.
private enum TranscriptRetimeHaptic {
    static func setBoundary() {
        #if !targetEnvironment(simulator)
        UINotificationFeedbackGenerator().notificationOccurred(.success)
        #endif
    }

    static func togglePlayback() {
        #if !targetEnvironment(simulator)
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        #endif
    }

    static func movePrevious() {
        #if !targetEnvironment(simulator)
        UIImpactFeedbackGenerator(style: .rigid).impactOccurred()
        #endif
    }

    static func moveNext() {
        #if !targetEnvironment(simulator)
        UIImpactFeedbackGenerator(style: .medium).impactOccurred()
        #endif
    }
}

struct TranscriptSegmentRetimeSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let discussionID: String
    let audioDurationMs: Int64
    let onSave: ([TranscriptSegmentUpdate]) async throws -> Void

    @State private var sequence: TranscriptRetimeSequence
    @State private var timing: TranscriptRetimeState
    @State private var selectedBoundary = TranscriptTimestampBoundary.start
    @State private var editingBoundary: TranscriptTimestampBoundary?
    @State private var audioPlayer: TranscriptEditorAudioPlayer?
    @State private var scrubberTimestampMs: Int64
    @State private var isScrubbingAudio = false
    @State private var playbackError: String?
    @State private var saveError: String?
    @State private var isSaving = false
    @State private var navigationDirection = TranscriptRetimeNavigationDirection.forward

    init(
        discussionID: String,
        segments: [TranscriptSegmentDTO],
        initialIndex: Int,
        audioDurationMs: Int64,
        onSave: @escaping ([TranscriptSegmentUpdate]) async throws -> Void
    ) {
        precondition(segments.indices.contains(initialIndex))
        self.discussionID = discussionID
        self.audioDurationMs = audioDurationMs
        self.onSave = onSave
        let segment = segments[initialIndex]
        _sequence = State(initialValue: TranscriptRetimeSequence(
            segments: segments,
            initialIndex: initialIndex
        ))
        _timing = State(initialValue: TranscriptRetimeState(
            segment: segment,
            audioDurationMs: audioDurationMs
        ))
        _scrubberTimestampMs = State(initialValue: segment.offsetMs)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 22) {
                    timestampRange
                    captionCard
                    audioControls

                    if let validationMessage {
                        Text(validationMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(20)
            }
            .background(Theme.background)
            .navigationTitle("Subtitle Timing")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSaving)
                        .accessibilityIdentifier("retime.cancel")
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: save) {
                        if isSaving {
                            ProgressView()
                        } else {
                            Text("Save")
                        }
                    }
                    .disabled(isSaving || validationMessage != nil)
                    .accessibilityIdentifier("retime.save")
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                setBoundaryButton
                    .padding(.horizontal, 20)
                    .padding(.vertical, 12)
                    .background(.ultraThinMaterial)
            }
        }
        .interactiveDismissDisabled(isSaving)
        .onAppear {
            if audioPlayer == nil {
                audioPlayer = TranscriptEditorAudioPlayer(
                    api: APIClient(tokens: auth),
                    discussionID: discussionID,
                    initialTimestampMs: scrubberTimestampMs,
                    audioDurationMs: timing.audioDurationMs
                )
            }
        }
        .onDisappear { audioPlayer?.stop() }
        .sheet(item: $editingBoundary) { boundary in
            TranscriptTimestampPickerSheet(
                boundary: boundary,
                milliseconds: timing.timestamp(for: boundary),
                maximumMs: timing.maximumTimestampMs
            ) { timestampMs in
                timing.set(boundary, to: timestampMs)
            }
        }
        .alert("Could not save transcript", isPresented: saveErrorBinding) {
            Button("OK", role: .cancel) { saveError = nil }
        } message: {
            Text(saveError ?? "")
        }
    }

    private var timestampRange: some View {
        HStack(spacing: 12) {
            timestampButton(for: .start)
            Image(systemName: "arrow.right")
                .foregroundStyle(Theme.secondaryText)
                .accessibilityHidden(true)
            timestampButton(for: .end)
        }
    }

    private func timestampButton(for boundary: TranscriptTimestampBoundary) -> some View {
        HStack(spacing: 0) {
            Button {
                selectedBoundary = boundary
            } label: {
                VStack(spacing: 5) {
                    Text(boundary.title)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.secondaryText)
                    Text(transcriptRetimeTimestamp(timing.timestamp(for: boundary)))
                        .font(.body.monospacedDigit().weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                        .minimumScaleFactor(0.65)
                }
                .frame(maxWidth: .infinity)
                .padding(.horizontal, 8)
                .padding(.vertical, 12)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(boundary.selectionTitle)
            .accessibilityValue(transcriptRetimeTimestamp(timing.timestamp(for: boundary)))
            .accessibilityIdentifier(boundary == .start ? "retime.selectStart" : "retime.selectEnd")

            Divider()
                .frame(height: 42)

            Button {
                editingBoundary = boundary
            } label: {
                Image(systemName: "slider.vertical.3")
                    .font(.body.weight(.semibold))
                    .frame(width: 38, height: 52)
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            .accessibilityLabel(boundary.adjustmentTitle)
            .accessibilityIdentifier(boundary == .start ? "retime.adjustStart" : "retime.adjustEnd")
        }
        .background(
            selectedBoundary == boundary ? Theme.accent.opacity(0.16) : Theme.rowBackground,
            in: .rect(cornerRadius: 14)
        )
        .overlay {
            RoundedRectangle(cornerRadius: 14)
                .stroke(selectedBoundary == boundary ? Theme.accent : Theme.divider, lineWidth: 1)
        }
    }

    private var captionCard: some View {
        VStack(spacing: 12) {
            captionSequence

            HStack(spacing: 12) {
                Button(action: movePrevious) {
                    Label("Previous Subtitle", systemImage: "chevron.left")
                        .frame(maxWidth: .infinity)
                }
                .disabled(!sequence.canMovePrevious || isSaving)
                .accessibilityIdentifier("retime.previous")

                Button(action: moveNext) {
                    Label("Next Subtitle", systemImage: "chevron.right")
                        .frame(maxWidth: .infinity)
                }
                .disabled(!sequence.canMoveNext || isSaving)
                .accessibilityIdentifier("retime.next")
            }
            .buttonStyle(.bordered)
        }
    }

    private var captionSequence: some View {
        VStack(spacing: 12) {
            if let previous = sequence.previousSegment {
                captionContextButton(
                    title: "Previous Subtitle",
                    segment: previous,
                    systemImage: "chevron.up",
                    action: movePrevious
                )
            }

            captionContextRow(
                title: "Current Subtitle",
                segment: sequence.currentSegment,
                isCurrent: true
            )

            if let next = sequence.nextSegment {
                captionContextButton(
                    title: "Next Subtitle",
                    segment: next,
                    systemImage: "chevron.down",
                    action: moveNext
                )
            }
        }
        .id(sequence.currentIndex)
        .transition(.asymmetric(
            insertion: .move(edge: navigationDirection.insertionEdge).combined(with: .opacity),
            removal: .move(edge: navigationDirection.removalEdge).combined(with: .opacity)
        ))
        .clipped()
    }

    private func captionContextButton(
        title: LocalizedStringKey,
        segment: TranscriptSegmentDTO,
        systemImage: String,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: systemImage)
                    .font(.caption.weight(.semibold))
                    .padding(.top, 3)
                captionContextContent(title: title, segment: segment, isCurrent: false)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.plain)
        .disabled(isSaving)
    }

    private func captionContextRow(
        title: LocalizedStringKey,
        segment: TranscriptSegmentDTO,
        isCurrent: Bool
    ) -> some View {
        captionContextContent(title: title, segment: segment, isCurrent: isCurrent)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func captionContextContent(
        title: LocalizedStringKey,
        segment: TranscriptSegmentDTO,
        isCurrent: Bool
    ) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(isCurrent ? Theme.accent : Theme.secondaryText)
            Text(segment.text)
                .font(isCurrent ? .body : .subheadline)
                .foregroundStyle(isCurrent ? .primary : Theme.secondaryText)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier(isCurrent ? "retime.currentSubtitle" : "retime.contextSubtitle")
        }
        .frame(maxWidth: .infinity, minHeight: isCurrent ? 72 : 44, alignment: .topLeading)
        .padding(14)
        .background(
            isCurrent ? Theme.accent.opacity(0.10) : Theme.rowBackground,
            in: .rect(cornerRadius: 14)
        )
        .overlay {
            RoundedRectangle(cornerRadius: 14)
                .stroke(isCurrent ? Theme.accent : Theme.divider, lineWidth: 1)
        }
    }

    private var audioControls: some View {
        audioControlsContent
    }

    private var audioControlsContent: some View {
        VStack(spacing: 14) {
            Slider(
                value: scrubberBinding,
                in: 0...Double(max(timing.maximumTimestampMs, 1)),
                onEditingChanged: scrubberEditingChanged
            )
            .disabled(audioPlayer?.isLoading == true)

            HStack {
                Text(transcriptRetimeTimestamp(currentAudioTimestampMs))
                    .accessibilityIdentifier("retime.currentTime")
                Spacer()
                Text(transcriptRetimeTimestamp(timing.audioDurationMs))
            }
            .font(.caption.monospacedDigit())
            .foregroundStyle(Theme.secondaryText)

            HStack(spacing: 32) {
                Button { nudgeAudio(by: -1_000) } label: {
                    Text(verbatim: "-1s")
                        .font(.headline.monospacedDigit())
                        .frame(minWidth: 48, minHeight: 44)
                }
                .buttonStyle(.bordered)
                .accessibilityLabel("Back 1 Second")
                .accessibilityIdentifier("retime.minus1s")

                Button(action: toggleAudioPlayback) {
                    if audioPlayer?.isLoading == true {
                        ProgressView()
                            .frame(width: 44, height: 44)
                    } else {
                        Image(systemName: audioPlayer?.isPlaying == true
                              ? "pause.circle.fill" : "play.circle.fill")
                            .font(.system(size: 44))
                    }
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .accessibilityLabel(audioPlayer?.isPlaying == true ? "Pause audio" : "Play audio")
                .accessibilityIdentifier("retime.play")

                Button { nudgeAudio(by: 1_000) } label: {
                    Text(verbatim: "+1s")
                        .font(.headline.monospacedDigit())
                        .frame(minWidth: 48, minHeight: 44)
                }
                .buttonStyle(.bordered)
                .accessibilityLabel("Forward 1 Second")
                .accessibilityIdentifier("retime.plus1s")
            }
            .disabled(audioPlayer?.isLoading == true)

            if let playbackError {
                Text(playbackError)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(16)
        .background(Theme.rowBackground, in: .rect(cornerRadius: 16))
    }

    private var setBoundaryButton: some View {
        Button(action: setBoundaryToCurrentTime) {
            Label(
                setBoundaryTitle,
                systemImage: "scope"
            )
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .controlSize(.large)
        .tint(Theme.accent)
        .disabled(isSaving)
        .accessibilityIdentifier("retime.setCurrent")
    }

    private var scrubberBinding: Binding<Double> {
        Binding(
            get: { Double(currentAudioTimestampMs) },
            set: { scrubberTimestampMs = Int64($0.rounded()) }
        )
    }

    private var currentAudioTimestampMs: Int64 {
        isScrubbingAudio ? scrubberTimestampMs : (audioPlayer?.playbackTimestampMs ?? scrubberTimestampMs)
    }

    private var setBoundaryTitle: LocalizedStringKey {
        selectedBoundary == .start ? "Set Start to Current Time" : "Set End to Current Time"
    }

    private var validationMessage: String? {
        if timing.endTimestampMs <= timing.startTimestampMs {
            return String(
                localized: "The end timestamp must be after the start timestamp.",
                comment: "Transcript segment editor validation for an inverted time range"
            )
        }
        if timing.audioDurationMs > 0, timing.endTimestampMs > timing.audioDurationMs {
            return String(
                localized: "The time range exceeds the uploaded audio duration.",
                comment: "Transcript segment editor validation when the range exceeds the source audio"
            )
        }
        return nil
    }

    private var saveErrorBinding: Binding<Bool> {
        Binding(
            get: { saveError != nil },
            set: { if !$0 { saveError = nil } }
        )
    }

    private func scrubberEditingChanged(_ isEditing: Bool) {
        if isEditing {
            scrubberTimestampMs = currentAudioTimestampMs
            isScrubbingAudio = true
            return
        }
        isScrubbingAudio = false
        seekAudio(to: scrubberTimestampMs)
    }

    private func nudgeAudio(by deltaMs: Int64) {
        seekAudio(to: timing.nudgedTimestamp(from: currentAudioTimestampMs, by: deltaMs))
    }

    private func seekAudio(to timestampMs: Int64) {
        guard let audioPlayer else { return }
        scrubberTimestampMs = timing.nudgedTimestamp(from: timestampMs, by: 0)
        playbackError = nil
        Task { @MainActor in
            do {
                try await audioPlayer.seek(to: scrubberTimestampMs)
            } catch {
                playbackError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func toggleAudioPlayback() {
        guard let audioPlayer else { return }
        TranscriptRetimeHaptic.togglePlayback()
        playbackError = nil
        Task { @MainActor in
            do {
                try await audioPlayer.togglePlayback()
            } catch {
                playbackError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func setBoundaryToCurrentTime() {
        timing.set(selectedBoundary, to: currentAudioTimestampMs)
        TranscriptRetimeHaptic.setBoundary()
        if selectedBoundary == .start {
            selectedBoundary = .end
        } else if stageCurrentTiming() {
            animateToNextSubtitle(shouldSeekAudio: false)
        }
    }

    private func movePrevious() {
        guard stageCurrentTiming(), sequence.canMovePrevious else { return }
        TranscriptRetimeHaptic.movePrevious()
        navigationDirection = .backward
        withAnimation(.snappy(duration: 0.28)) {
            _ = sequence.movePrevious()
        }
        loadCurrentSegment(shouldSeekAudio: true)
    }

    private func moveNext() {
        guard stageCurrentTiming(), sequence.canMoveNext else { return }
        TranscriptRetimeHaptic.moveNext()
        animateToNextSubtitle(shouldSeekAudio: true)
    }

    private func animateToNextSubtitle(shouldSeekAudio: Bool) {
        guard sequence.canMoveNext else { return }
        navigationDirection = .forward
        withAnimation(.snappy(duration: 0.28)) {
            _ = sequence.moveNext()
        }
        loadCurrentSegment(shouldSeekAudio: shouldSeekAudio)
    }

    private func loadCurrentSegment(shouldSeekAudio: Bool) {
        let segment = sequence.currentSegment
        timing = TranscriptRetimeState(segment: segment, audioDurationMs: audioDurationMs)
        selectedBoundary = .start
        editingBoundary = nil
        if shouldSeekAudio {
            seekAudio(to: segment.offsetMs)
        }
    }

    private func save() {
        guard stageCurrentTiming() else { return }
        // Freeze the payload before crossing the Task boundary. SwiftUI can
        // recreate this view while the async save is starting; the request
        // must still contain the exact edits present when Save was tapped.
        let updates = sequence.pendingUpdates
        guard !updates.isEmpty else {
            dismiss()
            return
        }
        isSaving = true
        saveError = nil
        Task { @MainActor in
            do {
                try await onSave(updates)
                for update in updates { sequence.markSaved(at: update.index) }
                isSaving = false
                dismiss()
            } catch {
                isSaving = false
                saveError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func stageCurrentTiming() -> Bool {
        guard timing.isValid else { return false }
        sequence.replaceCurrent(with: timing.revisedSegment(from: sequence.currentSegment))
        return true
    }
}

private struct TranscriptTimestampPickerSheet: View {
    @Environment(\.dismiss) private var dismiss

    let boundary: TranscriptTimestampBoundary
    let maximumMs: Int64
    let onApply: (Int64) -> Void

    @State private var draftMilliseconds: Int64

    init(
        boundary: TranscriptTimestampBoundary,
        milliseconds: Int64,
        maximumMs: Int64,
        onApply: @escaping (Int64) -> Void
    ) {
        self.boundary = boundary
        self.maximumMs = maximumMs
        self.onApply = onApply
        _draftMilliseconds = State(initialValue: milliseconds)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 12) {
                Text(transcriptRetimeTimestamp(draftMilliseconds))
                    .font(.title3.monospacedDigit().weight(.semibold))

                TranscriptTimestampWheel(
                    milliseconds: $draftMilliseconds,
                    maximumMs: maximumMs
                )
            }
            .padding(.horizontal, 16)
            .navigationTitle(boundary.adjustmentTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .accessibilityIdentifier("retime.picker.cancel")
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") {
                        onApply(min(max(draftMilliseconds, 0), maximumMs))
                        dismiss()
                    }
                    .accessibilityIdentifier("retime.picker.done")
                }
            }
        }
        .presentationDetents([.medium])
    }
}

func transcriptRetimeTimestamp(_ milliseconds: Int64) -> String {
    let clamped = max(milliseconds, 0)
    let totalSeconds = clamped / 1_000
    let hours = totalSeconds / 3_600
    let minutes = (totalSeconds % 3_600) / 60
    let seconds = totalSeconds % 60
    let fraction = clamped % 1_000
    return String(format: "%02d:%02d:%02d:%03d", hours, minutes, seconds, fraction)
}
