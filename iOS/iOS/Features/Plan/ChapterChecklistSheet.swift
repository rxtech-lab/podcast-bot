import SwiftUI

/// Todo-list style chapter picker for audiobook batch generation.
///
/// Shows the root plan's full chapter list: generated chapters are checked and
/// locked, the chapter currently generating shows a spinner, and pending
/// chapters are checkable up to the per-batch cap (server hard cap 5). Used in
/// two places:
/// - `.plan`: before the first generation, from the plan review screen — all
///   chapters pending, no network fetch.
/// - `.discussion`: from a generated podcast ("Generate more chapters") — the
///   chapter progress is fetched from `GET /api/discussions/{id}/chapters`.
struct ChapterChecklistSheet: View {
    enum Mode {
        case plan(ScriptDTO)
        case discussion(id: String)
    }

    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let mode: Mode
    var editableDiscussion: Binding<Discussion>?
    /// Performs the generation for the checked chapters. Errors thrown here
    /// (including the server's 400 over-limit message) surface as an alert in
    /// the sheet; on success the sheet dismisses.
    var onGenerate: ([Int]) async throws -> Void

    /// Preferred batch size preselected for the user; configurable in
    /// Settings, always clamped to the server's max batch size.
    @AppStorage("audiobook.defaultBatchChapters") private var defaultBatchSize = 3

    @State private var chapters: [ChapterStatusDTO] = []
    @State private var maxBatchSize = 5
    @State private var selected: Set<Int> = []
    @State private var isLoading = false
    @State private var isSubmitting = false
    @State private var loadError: String?
    @State private var submitError: String?
    @State private var didLoad = false
    @State private var speakerDiscussion: Discussion?
    @State private var speakerLoadError: String?
    @State private var showingSpeakerModels = false

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView().tint(Theme.accent).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if let loadError {
                    ContentUnavailableView {
                        Label("Couldn't load chapters", systemImage: "exclamationmark.triangle")
                    } description: {
                        Text(loadError)
                    }
                } else {
                    chapterList
                }
            }
            .navigationTitle("Chapters")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .safeAreaInset(edge: .bottom) {
                if !isLoading, loadError == nil {
                    generateButton
                }
            }
        }
        .presentationDetents([.medium, .large])
        .alert("Couldn't start generation", isPresented: submitErrorBinding) {
            Button("OK", role: .cancel) { submitError = nil }
        } message: {
            Text(submitError ?? "")
        }
        .task { await loadIfNeeded() }
        .sheet(isPresented: $showingSpeakerModels) {
            speakerEditorSheet
        }
        .accessibilityIdentifier("chapters.checklist")
    }

    private var chapterList: some View {
        List {
            Section {
                Button {
                    showingSpeakerModels = true
                } label: {
                    HStack(spacing: 10) {
                        Label("Speaker Models", systemImage: "person.wave.2.fill")
                            .foregroundStyle(canEditSpeakers ? Theme.accent : Theme.secondaryText)
                        Spacer(minLength: 0)
                        Image(systemName: "chevron.right")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(Theme.secondaryText)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(!canEditSpeakers || isSubmitting)
                .accessibilityIdentifier("chapters.speakerModels")
            } footer: {
                if let speakerLoadError {
                    Text(speakerLoadError)
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
            }

            Section {
                ForEach(chapters) { chapter in
                    chapterRow(chapter)
                        .accessibilityIdentifier("chapters.row.\(chapter.index)")
                }
            } footer: {
                Text("Generate up to \(maxBatchSize) chapters at a time. Remaining chapters can be generated later as follow-up episodes in the same album.")
                    .font(.footnote)
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
    }

    @ViewBuilder
    private var speakerEditorSheet: some View {
        if let binding = speakerEditorBinding {
            SpeakerModelsSheet(discussion: binding,
                               visibleSpeakerNames: selectedSpeakerNames)
        } else {
            NavigationStack {
                ContentUnavailableView {
                    Label("Couldn't load speakers", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(speakerLoadError ?? "Speaker editing is unavailable for this audiobook.")
                }
            }
        }
    }

    private var speakerEditorBinding: Binding<Discussion>? {
        if let editableDiscussion {
            return editableDiscussion
        }
        guard speakerDiscussion != nil else { return nil }
        return Binding(
            get: { speakerDiscussion! },
            set: { speakerDiscussion = $0 }
        )
    }

    private var canEditSpeakers: Bool {
        speakerEditorBinding != nil
    }

    private var selectedSpeakerNames: Set<String> {
        var names = Set<String>()
        for chapter in chapters where selected.contains(chapter.index) {
            for name in chapter.speakers ?? [] {
                let key = Self.normalizedSpeakerName(name)
                if !key.isEmpty {
                    names.insert(key)
                }
            }
        }
        return names
    }

    @ViewBuilder
    private func chapterRow(_ chapter: ChapterStatusDTO) -> some View {
        let isSelected = selected.contains(chapter.index)
        let selectable = chapter.isPending && (isSelected || selected.count < maxBatchSize)
        Button {
            toggle(chapter)
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: 12) {
                statusIcon(chapter, isSelected: isSelected)
                    .frame(width: 26)
                VStack(alignment: .leading, spacing: 3) {
                    Text("\(chapter.index). \(chapter.title)")
                        .font(.body.weight(.semibold))
                        .foregroundStyle(chapter.isDone ? Theme.secondaryText : .primary)
                        .fixedSize(horizontal: false, vertical: true)
                    if !chapter.summary.isEmpty {
                        Text(chapter.summary)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(2)
                    }
                    if chapter.isGenerating {
                        Text("Generating…")
                            .font(.caption.weight(.medium))
                            .foregroundStyle(Theme.accent)
                    }
                }
                Spacer(minLength: 0)
            }
            .padding(.vertical, 4)
            .contentShape(.rect)
            .opacity(chapter.isPending && !selectable && !isSelected ? 0.45 : 1)
        }
        .buttonStyle(.plain)
        .disabled(!chapter.isPending || isSubmitting || (!isSelected && selected.count >= maxBatchSize))
    }

    @ViewBuilder
    private func statusIcon(_ chapter: ChapterStatusDTO, isSelected: Bool) -> some View {
        if chapter.isDone {
            Image(systemName: "checkmark.square.fill")
                .font(.title3)
                .foregroundStyle(Theme.secondaryText)
        } else if chapter.isGenerating {
            ProgressView()
                .controlSize(.small)
                .tint(Theme.accent)
        } else {
            Image(systemName: isSelected ? "checkmark.square.fill" : "square")
                .font(.title3)
                .foregroundStyle(isSelected ? Theme.accent : Color.secondary)
                .contentTransition(.symbolEffect(.replace))
        }
    }

    private var generateButton: some View {
        Button {
            submit()
        } label: {
            HStack {
                if isSubmitting {
                    ProgressView().tint(.white)
                } else {
                    Image(systemName: "waveform")
                }
                Text(generateButtonTitle)
                    .font(.body.weight(.semibold))
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(selected.isEmpty ? Color.secondary.opacity(0.3) : Theme.accent, in: .capsule)
            .foregroundStyle(.white)
        }
        .buttonStyle(.plain)
        .disabled(selected.isEmpty || isSubmitting)
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(.ultraThinMaterial)
        .accessibilityIdentifier("chapters.generate")
    }

    private var generateButtonTitle: String {
        let count = selected.count
        if count == 0 { return String(localized: "Select chapters to generate") }
        return String(localized: "Generate \(count) chapter\(count == 1 ? "" : "s")")
    }

    private var submitErrorBinding: Binding<Bool> {
        Binding(
            get: { submitError != nil },
            set: { if !$0 { submitError = nil } }
        )
    }

    private func toggle(_ chapter: ChapterStatusDTO) {
        guard chapter.isPending else { return }
        if selected.contains(chapter.index) {
            selected.remove(chapter.index)
        } else if selected.count < maxBatchSize {
            selected.insert(chapter.index)
        }
    }

    private func loadIfNeeded() async {
        guard !didLoad else { return }
        didLoad = true
        switch mode {
        case .plan(let script):
            chapters = (script.audioBookChapters ?? []).enumerated().map { index, chapter in
                ChapterStatusDTO(index: index + 1,
                                 title: chapter.title,
                                 summary: chapter.summary,
                                 mode: chapter.mode,
                                 speakers: chapter.speakers,
                                 status: "pending")
            }
            preselectDefaults()
        case .discussion(let id):
            isLoading = true
            speakerLoadError = nil
            do {
                let client = APIClient(tokens: auth)
                let response = try await client.discussionChapters(id: id)
                chapters = response.chapters
                maxBatchSize = max(1, response.maxBatchSize)
                preselectDefaults()
                do {
                    speakerDiscussion = try await client.discussion(id: response.rootID, includeEditTurns: false)
                } catch {
                    speakerLoadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
                }
            } catch {
                loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
            isLoading = false
        }
    }

    /// Checks the first pending chapters up to the user's preferred batch size
    /// (never above the server cap).
    private func preselectDefaults() {
        let target = min(max(defaultBatchSize, 1), maxBatchSize)
        selected = Set(chapters.filter(\.isPending).prefix(target).map(\.index))
    }

    private func submit() {
        let indices = selected.sorted()
        guard !indices.isEmpty else { return }
        isSubmitting = true
        Task {
            do {
                try await onGenerate(indices)
                isSubmitting = false
                dismiss()
            } catch {
                isSubmitting = false
                submitError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    nonisolated private static func normalizedSpeakerName(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}
