import SwiftUI

/// Coordinates the deep-link-driven parent-discussion picker for the backend-rendered
/// New Discussion form.
///
/// The form is rendered by `JSONSchemaForm`, so the `discussionPicker` widget cannot
/// present a sheet that writes back into the view's `formData` on its own. Instead the
/// widget hands this coordinator the backend-declared deep link plus a write-back
/// closure; `NewDiscussionView` owns the actual sheet and reports the selection here,
/// which both updates the form value and caches the chosen reference for display.
///
/// This is deliberately a form-local coordinator rather than the global
/// `DeepLinkRouter`: the global router presents at the app root and cannot reach the
/// form's state, whereas the parent selection must flow back into `formData`.
@Observable
@MainActor
final class NewDiscussionFormCoordinator {
    /// Host of the deep link the backend uses for the parent-discussion picker
    /// (`debatepod://discussion-picker`).
    static let pickerHost = "discussion-picker"

    /// Whether the parent-discussion picker sheet should be presented.
    var isPresenting = false

    /// The field id (e.g. `root_reference_discussion_id`) currently driving the picker.
    private(set) var activeFieldID: String?

    /// Cached references keyed by field id, used by the widget to show a title for the
    /// current selection (including pre-filled and restored values).
    private var selections: [String: PodcastReference] = [:]

    /// Write-back closure supplied by the active widget, applied when a selection is made.
    private var onSelect: ((PodcastReference?) -> Void)?

    /// Id of the active field's current selection, used to mark the checked row.
    var activeSelectionID: String? {
        activeFieldID.flatMap { selections[$0]?.id }
    }

    /// The reference currently selected for `fieldID`, if known.
    func reference(for fieldID: String) -> PodcastReference? {
        selections[fieldID]
    }

    /// Cache a reference for display (e.g. a pre-filled parent, or one resolved by id).
    func cache(_ reference: PodcastReference, for fieldID: String) {
        selections[fieldID] = reference
    }

    /// Interpret a backend-declared deep link and, when it targets the picker, present it.
    /// `onSelect` receives the chosen reference (or nil when cleared) so the widget can
    /// write the id back into its form value.
    func open(
        deepLink: String,
        fieldID: String,
        onSelect: @escaping (PodcastReference?) -> Void
    ) {
        guard let url = URL(string: deepLink), url.host == Self.pickerHost else { return }
        activeFieldID = fieldID
        self.onSelect = onSelect
        isPresenting = true
    }

    /// Report the user's choice from the picker sheet: cache it, write it back into the
    /// form value, and dismiss.
    func complete(with reference: PodcastReference?) {
        if let activeFieldID {
            if let reference {
                selections[activeFieldID] = reference
            } else {
                selections.removeValue(forKey: activeFieldID)
            }
        }
        onSelect?(reference)
        finish()
    }

    /// Clear a selection without opening the picker (the row's clear button).
    func clear(fieldID: String) {
        selections.removeValue(forKey: fieldID)
    }

    /// Dismiss the picker without changing the selection.
    func cancel() {
        finish()
    }

    private func finish() {
        isPresenting = false
        activeFieldID = nil
        onSelect = nil
    }
}

extension Discussion {
    /// Lightweight reference used as a parent discussion for follow-up planning.
    var podcastReference: PodcastReference {
        PodcastReference(id: id, title: displayTitle, topic: topic)
    }
}

/// Searchable sheet listing the user's existing discussions to pick a parent.
/// Ported from the original hand-built New Discussion form; reports the selection
/// through `onSelect` rather than a binding so it can be driven by the form coordinator.
struct ReferencePodcastPickerSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    /// Id of the currently selected discussion, used to show the checkmark.
    let selectedID: String?
    let onSelect: (PodcastReference?) -> Void
    @State private var discussions: [Discussion] = []
    @State private var query = ""
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var searchTask: Task<Void, Never>?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                List {
                    if isLoading && discussions.isEmpty {
                        HStack {
                            Spacer()
                            ProgressView().tint(Theme.accent)
                            Spacer()
                        }
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                    ForEach(discussions) { discussion in
                        Button {
                            onSelect(discussion.podcastReference)
                            dismiss()
                        } label: {
                            HStack(spacing: 12) {
                                DiscussionCoverThumbnail(discussion: discussion, size: 44)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(discussion.displayTitle)
                                        .font(.subheadline.weight(.semibold))
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                    Text(discussion.topic)
                                        .font(.caption)
                                        .foregroundStyle(Theme.secondaryText)
                                        .lineLimit(2)
                                }
                                Spacer()
                                if selectedID == discussion.id {
                                    Image(systemName: "checkmark.circle.fill")
                                        .foregroundStyle(Theme.accent)
                                }
                            }
                            .padding(.vertical, 4)
                        }
                        .buttonStyle(.plain)
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                if let errorMessage, discussions.isEmpty {
                    ContentUnavailableView("Could not load stations",
                                           systemImage: "exclamationmark.triangle",
                                           description: Text(errorMessage))
                } else if !isLoading && discussions.isEmpty {
                    ContentUnavailableView("No Station",
                                           systemImage: "waveform",
                                           description: Text("Create or search for a podcast to use as follow-up context."))
                }
            }
            .navigationTitle("Parent Station")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search stations")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .task {
                await load()
            }
            .onChange(of: query) { _, value in
                searchTask?.cancel()
                searchTask = Task {
                    try? await Task.sleep(for: .milliseconds(250))
                    guard !Task.isCancelled else { return }
                    await load(search: value)
                }
            }
            .onDisappear {
                searchTask?.cancel()
            }
        }
    }

    @MainActor
    private func load(search: String? = nil) async {
        isLoading = true
        defer { isLoading = false }
        do {
            discussions = try await APIClient(tokens: auth).parentPodcasts(limit: 50, query: search ?? query)
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// Square cover thumbnail for a discussion row (image, gradient, or waveform fallback).
struct DiscussionCoverThumbnail: View {
    let discussion: Discussion
    let size: CGFloat

    var body: some View {
        Group {
            if let url = discussion.cover?.renderableImageURL {
                AsyncImage(url: url) { phase in
                    switch phase {
                    case let .success(image):
                        image
                            .resizable()
                            .scaledToFill()
                    default:
                        fallback
                    }
                }
            } else if let cover = discussion.cover, cover.hasGradient {
                LinearGradient(colors: [color(cover.gradientStart), color(cover.gradientEnd)],
                               startPoint: .topLeading,
                               endPoint: .bottomTrailing)
            } else {
                fallback
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: 8))
    }

    private var fallback: some View {
        ZStack {
            LinearGradient(colors: [Theme.accent.opacity(0.75), Color.orange.opacity(0.72)],
                           startPoint: .topLeading,
                           endPoint: .bottomTrailing)
            Image(systemName: "waveform")
                .font(.system(size: size * 0.38, weight: .semibold))
                .foregroundStyle(.white)
        }
    }

    private func color(_ hex: String?) -> Color {
        guard let hex else { return Theme.accent }
        let trimmed = hex.trimmingCharacters(in: CharacterSet(charactersIn: "# "))
        guard trimmed.count == 6, let value = Int(trimmed, radix: 16) else {
            return Theme.accent
        }
        let red = Double((value >> 16) & 0xff) / 255.0
        let green = Double((value >> 8) & 0xff) / 255.0
        let blue = Double(value & 0xff) / 255.0
        return Color(red: red, green: green, blue: blue)
    }
}
