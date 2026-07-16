import SwiftUI
import TipKit

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
    static let shortGapThresholdMs: Int64 = 300

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

    mutating func removeGaps(shorterThan thresholdMs: Int64) {
        guard thresholdMs > 0, orderedIndices.count > 1 else { return }

        for position in orderedIndices.indices.dropFirst() {
            let previous = segments[orderedIndices[position - 1]]
            let currentIndex = orderedIndices[position]
            let current = segments[currentIndex]
            let previousEndMs = previous.offsetMs + previous.durationMs
            let currentEndMs = current.offsetMs + current.durationMs
            let gapMs = current.offsetMs - previousEndMs

            guard gapMs > 0, gapMs < thresholdMs else { continue }
            segments[currentIndex] = TranscriptSegmentDTO(
                speaker: current.speaker,
                offsetMs: previousEndMs,
                durationMs: currentEndMs - previousEndMs,
                text: current.text
            )
        }
    }

    mutating func movePrevious() -> Bool {
        move(positionDelta: -1)
    }

    mutating func moveNext() -> Bool {
        move(positionDelta: 1)
    }

    mutating func select(index: Int) -> Bool {
        guard orderedIndices.contains(index) else { return false }
        currentIndex = index
        return true
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
    @State private var removesShortGaps = true
    @State private var showsConfiguration = false

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
            ZStack(alignment: .bottom) {
                VStack(spacing: 22) {
                    timestampRange
                    captionCard
                }
                .padding(20)

                VStack(spacing: 0) {
                    if let message = inlineErrorMessage {
                        errorBanner(message)
                    }
                    playerOverlay
                }
                .animation(.spring(response: 0.3, dampingFraction: 0.85), value: inlineErrorMessage)
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
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showsConfiguration = true
                    } label: {
                        Image(systemName: "gearshape")
                    }
                    .disabled(isSaving)
                    .accessibilityLabel("Timing Settings")
                    .accessibilityIdentifier("retime.settings")
                    .popoverTip(TranscriptRetimeSettingsTip(), arrowEdge: .top)
                }
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
        .sheet(isPresented: $showsConfiguration) {
            NavigationStack {
                VStack {
                    gapRemovalToggle
                    Spacer()
                }
                .padding(20)
                .background(Theme.background)
                .navigationTitle("Timing Settings")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") {
                            showsConfiguration = false
                        }
                    }
                }
            }
            .presentationDetents([.medium])
            .presentationDragIndicator(.visible)
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
        GeometryReader { geometry in
            ScrollView(.vertical) {
                LazyVStack(spacing: 12) {
                    ForEach(sequence.orderedIndices, id: \.self) { index in
                        captionWheelRow(
                            segment: sequence.segments[index],
                            isCurrent: index == sequence.currentIndex
                        )
                        .frame(height: captionWheelRowHeight)
                        .id(index)
                        .scrollTransition(.interactive, axis: .vertical) { content, phase in
                            content
                                .scaleEffect(phase.isIdentity ? 1 : 0.94)
                                .opacity(phase.isIdentity ? 1 : 0.62)
                        }
                        .accessibilityIdentifier("retime.subtitle.\(index)")
                    }
                }
                .scrollTargetLayout()
            }
            .scrollIndicators(.hidden)
            .scrollTargetBehavior(.viewAligned(limitBehavior: .always))
            .scrollPosition(id: subtitleScrollPosition, anchor: captionWheelAnchor)
            .contentMargins(
                .top,
                max(
                    (geometry.size.height - captionWheelRowHeight) * captionWheelAnchor.y,
                    0
                ),
                for: .scrollContent
            )
            .contentMargins(
                .bottom,
                max(
                    (geometry.size.height - captionWheelRowHeight) * (1 - captionWheelAnchor.y),
                    0
                ),
                for: .scrollContent
            )
            .disabled(isSaving)
            .clipped()
            .accessibilityLabel("Subtitle")
            .accessibilityIdentifier("retime.subtitleWheel")
        }
        .frame(minHeight: 282)
    }

    private var captionWheelRowHeight: CGFloat { 106 }
    private var captionWheelAnchor: UnitPoint { UnitPoint(x: 0.5, y: 0.32) }

    private func captionWheelRow(segment: TranscriptSegmentDTO, isCurrent: Bool) -> some View {
        Text(segment.text)
            .font(.body)
            .fontWeight(.regular)
            .foregroundStyle(isCurrent ? .primary : Theme.secondaryText)
            .lineLimit(4)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            .padding(.horizontal, 14)
            .background(
                isCurrent ? Theme.accent.opacity(0.10) : Theme.rowBackground,
                in: .rect(cornerRadius: 14)
            )
            .overlay {
                RoundedRectangle(cornerRadius: 14)
                    .stroke(isCurrent ? Theme.accent : Theme.divider, lineWidth: 1)
            }
            .animation(.snappy(duration: 0.2), value: isCurrent)
            .accessibilityAddTraits(isCurrent ? .isSelected : [])
    }

    private var audioControls: some View {
        audioControlsContent
    }

    private var playerOverlay: some View {
        VStack(spacing: 12) {
            audioControls
            setBoundaryButton
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
        .background(.ultraThinMaterial)
    }

    private func errorBanner(_ message: String) -> some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.red)
                .font(.subheadline.weight(.semibold))
            Text(message)
                .font(.footnote.weight(.medium))
                .foregroundStyle(.primary)
                .lineLimit(2)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(Color.red.opacity(0.5), in: .rect(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .stroke(.red.opacity(0.28), lineWidth: 1)
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 8)
        .transition(.move(edge: .bottom).combined(with: .opacity))
        .accessibilityIdentifier("retime.error")
    }

    private var gapRemovalToggle: some View {
        Toggle(isOn: $removesShortGaps) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Remove Short Gaps")
                    .font(.body.weight(.semibold))
                Text("On save, gaps under 0.3 seconds are closed without changing subtitle end times.")
                    .font(.footnote)
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .tint(Theme.accent)
        .disabled(isSaving)
        .accessibilityIdentifier("retime.removeShortGaps")
        .padding(16)
        .background(Theme.rowBackground, in: .rect(cornerRadius: 16))
    }

    private var audioControlsContent: some View {
        VStack(spacing: 14) {
            Slider(
                value: scrubberBinding,
                in: 0 ... Double(max(timing.maximumTimestampMs, 1)),
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
                    Label("1s", systemImage: "gobackward")
                        .font(.subheadline)
                        .frame(minWidth: 36, minHeight: 32)
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
                    Label("1s", systemImage: "goforward")
                        .font(.subheadline)
                        .frame(minWidth: 36, minHeight: 32)
                }
                .buttonStyle(.bordered)
                .accessibilityLabel("Forward 1 Second")
                .accessibilityIdentifier("retime.plus1s")
            }
            .disabled(audioPlayer?.isLoading == true)
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

    private var subtitleScrollPosition: Binding<Int?> {
        Binding(
            get: { sequence.currentIndex },
            set: { index in
                if let index {
                    selectSubtitle(at: index)
                }
            }
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

    private var inlineErrorMessage: String? {
        validationMessage ?? playbackError
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

    private func selectSubtitle(at index: Int) {
        guard index != sequence.currentIndex, stageCurrentTiming() else { return }
        let currentPosition = sequence.orderedIndices.firstIndex(of: sequence.currentIndex)
        let selectedPosition = sequence.orderedIndices.firstIndex(of: index)
        guard sequence.select(index: index) else { return }
        if let currentPosition, let selectedPosition, selectedPosition < currentPosition {
            TranscriptRetimeHaptic.movePrevious()
        } else {
            TranscriptRetimeHaptic.moveNext()
        }
        loadCurrentSegment(shouldSeekAudio: true)
    }

    private func animateToNextSubtitle(shouldSeekAudio: Bool) {
        guard sequence.canMoveNext else { return }
        _ = sequence.moveNext()
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
        if removesShortGaps {
            sequence.removeGaps(shorterThan: TranscriptRetimeSequence.shortGapThresholdMs)
        }
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
                for update in updates {
                    sequence.markSaved(at: update.index)
                }
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

#if DEBUG
private let previewSegments: [TranscriptSegmentDTO] = [
    TranscriptSegmentDTO(speaker: "Alice", offsetMs: 0, durationMs: 3_200, text: "Welcome to today's debate on artificial intelligence policy."),
    TranscriptSegmentDTO(speaker: "Bob", offsetMs: 3_500, durationMs: 4_800, text: "Thank you Alice. I believe strong regulation is essential to ensure public safety."),
    TranscriptSegmentDTO(speaker: "Alice", offsetMs: 8_400, durationMs: 3_900, text: "While I agree oversight matters, overly strict rules could stifle innovation."),
    TranscriptSegmentDTO(speaker: "Bob", offsetMs: 12_500, durationMs: 5_100, text: "Innovation without guardrails has historically led to unforeseen consequences."),
]

#Preview("Subtitle Timing · middle segment") {
    TranscriptSegmentRetimeSheet(
        discussionID: "preview-discussion",
        segments: previewSegments,
        initialIndex: 1,
        audioDurationMs: 18_000
    ) { _ in }
    .environment(AuthManager())
}

#Preview("Subtitle Timing · first segment") {
    TranscriptSegmentRetimeSheet(
        discussionID: "preview-discussion",
        segments: previewSegments,
        initialIndex: 0,
        audioDurationMs: 18_000
    ) { _ in }
    .environment(AuthManager())
}
#endif
