import SwiftUI

/// Apple Music-style album page: large cover, title, episode count, play
/// button, and the episode list in album order (audiobook batches by chapter,
/// then creation time). The toolbar menu carries the album actions: generate
/// more chapters, add podcasts, rename, and remove the grouping.
///
/// Episode rows push `LibraryDestination.discussion` values. When the album is
/// shown inside the library's `NavigationStack` (whose typed path only accepts
/// `LibraryDestination`), the stack-root registration resolves them; when it
/// runs in its own stack (the player's album sheet), pass
/// `ownsNavigation: true` so the view registers its own destination.
struct AlbumView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let albumID: String
    var ownsNavigation: Bool = false

    @State private var detail: AlbumDetailResponse?
    @State private var actionItems: [DiscussionUIActionItem] = []
    @State private var isLoading = true
    @State private var errorMessage: String?
    @State private var actionError: String?
    @State private var showingChapterChecklist = false
    @State private var showingAddPodcasts = false
    @State private var showingCoverEditor = false
    @State private var showingRename = false
    @State private var showingRemoveConfirm = false
    @State private var renameTitle = ""

    var body: some View {
        if ownsNavigation {
            core.navigationDestination(for: LibraryDestination.self) { destination in
                albumDestination(destination)
            }
        } else {
            core
        }
    }

    private var core: some View {
        Group {
            if isLoading {
                ProgressView().tint(Theme.accent).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let errorMessage {
                ContentUnavailableView {
                    Label("Couldn't load album", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(errorMessage)
                } actions: {
                    Button("Retry") { Task { await load() } }
                }
            } else if let detail {
                content(detail)
            }
        }
        .background(Theme.background.ignoresSafeArea())
        .navigationTitle(detail?.album.title ?? "Album")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { albumToolbar }
        .sheet(isPresented: $showingChapterChecklist) {
            chapterChecklistSheet
        }
        .sheet(isPresented: $showingAddPodcasts) {
            addPodcastsSheet
        }
        .sheet(isPresented: $showingCoverEditor) {
            coverEditorSheet
        }
        .alert("Rename Album", isPresented: $showingRename) {
            TextField("Album name", text: $renameTitle)
            Button("Rename") { rename() }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Remove this album?",
            isPresented: $showingRemoveConfirm,
            titleVisibility: .visible
        ) {
            Button("Remove Album", role: .destructive) { removeAlbum() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The grouping is removed; the podcasts stay in your library.")
        }
        .alert("Couldn't update the album", isPresented: actionErrorBinding) {
            Button("OK", role: .cancel) { actionError = nil }
        } message: {
            Text(actionError ?? "")
        }
        .task { await load() }
        .refreshable { await load() }
        .accessibilityIdentifier("album.view")
    }

    // MARK: - Toolbar (server-rendered)

    @ToolbarContentBuilder
    private var albumToolbar: some ToolbarContent {
        // In the player's album sheet the view owns its own stack, so give it
        // an explicit way to close besides swiping the sheet down.
        if ownsNavigation {
            ToolbarItem(placement: .topBarLeading) {
                Button("Close") { dismiss() }
                    .accessibilityIdentifier("album.close")
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            DiscussionActionsMenu(
                items: actionItems,
                labelSystemImage: "ellipsis",
                accessibilityLabel: "Album actions",
                isBusy: { _ in false },
                perform: performAlbumAction
            )
            .accessibilityIdentifier("album.more")
        }
    }

    /// Routes a server-provided album action by its validated deep link
    /// (debatepod://album/{id}/...), mirroring the podcast toolbars.
    private func performAlbumAction(_ item: DiscussionUIActionItem) {
        guard let path = validatedAlbumActionPath(item) else { return }
        switch path {
        case ["sheet", "generate-chapters"]:
            showingChapterChecklist = true
        case ["sheet", "add-podcasts"]:
            showingAddPodcasts = true
        case ["sheet", "cover"]:
            showingCoverEditor = true
        case ["sheet", "rename"]:
            renameTitle = detail?.album.title ?? ""
            showingRename = true
        case ["action", "remove"]:
            showingRemoveConfirm = true
        default:
            break
        }
    }

    private func validatedAlbumActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "album" else { return nil }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == albumID else { return nil }
        return Array(components.dropFirst())
    }

    /// The discussion whose plan holds the album's full chapter list: the auto
    /// album's root, or the first audiobook episode as a fallback.
    private var audioBookRootID: String? {
        if let rootID = detail?.album.rootDiscussionID, !rootID.isEmpty {
            return rootID
        }
        return detail?.episodes.first { $0.script?.type == "audio-book" }?.id
    }

    private var chapterChecklistSheet: some View {
        ChapterChecklistSheet(mode: .discussion(id: audioBookRootID ?? albumID)) { indices in
            guard let rootID = audioBookRootID else { return }
            _ = try await APIClient(tokens: auth).generateChapters(id: rootID, chapters: indices)
            showingChapterChecklist = false
            await load()
        }
    }

    @ViewBuilder
    private var coverEditorSheet: some View {
        if let album = detail?.album {
            AlbumCoverEditorSheet(album: album) { updated in
                detail?.album = updated
            }
        }
    }

    private var addPodcastsSheet: some View {
        AlbumAddPodcastsSheet(albumID: albumID) {
            showingAddPodcasts = false
            Task { await load() }
        }
    }

    private var actionErrorBinding: Binding<Bool> {
        Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )
    }

    private func rename() {
        let title = renameTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        Task {
            do {
                let renamed = try await APIClient(tokens: auth).renameAlbum(id: albumID, title: title)
                detail?.album = renamed
            } catch {
                actionError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func removeAlbum() {
        Task {
            do {
                try await APIClient(tokens: auth).deleteAlbum(id: albumID)
                dismiss()
            } catch {
                actionError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func removeEpisode(_ episode: Discussion) {
        Task {
            do {
                try await APIClient(tokens: auth).removeFromAlbum(id: albumID, discussionID: episode.id)
                await load()
            } catch {
                actionError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func content(_ detail: AlbumDetailResponse) -> some View {
        List {
            Section {
                header(detail)
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            Section {
                ForEach(Array(detail.episodes.enumerated()), id: \.element.id) { index, episode in
                    NavigationLink(value: LibraryDestination.discussion(episode)) {
                        AlbumEpisodeRow(episode: episode, number: index + 1)
                    }
                    .accessibilityIdentifier("album.episode.\(episode.id)")
                    .listRowBackground(Color.clear)
                    .swipeActions(edge: .trailing) {
                        Button(role: .destructive) {
                            removeEpisode(episode)
                        } label: {
                            Label("Remove from Album", systemImage: "rectangle.stack.badge.minus")
                        }
                    }
                }
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
    }

    private func header(_ detail: AlbumDetailResponse) -> some View {
        VStack(spacing: 14) {
            AlbumCoverThumbnail(cover: detail.album.cover, size: 220)
                .shadow(color: .black.opacity(0.25), radius: 18, y: 8)
            VStack(spacing: 4) {
                Text(detail.album.title)
                    .font(.title2.weight(.bold))
                    .multilineTextAlignment(.center)
                Text("\(detail.episodes.count) episode\(detail.episodes.count == 1 ? "" : "s")")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            if let first = firstPlayableEpisode(detail) {
                NavigationLink(value: LibraryDestination.discussion(first)) {
                    Label("Play", systemImage: "play.fill")
                        .font(.body.weight(.semibold))
                        .padding(.horizontal, 36)
                        .padding(.vertical, 4)
                }
                .buttonStyle(.glassProminent)
                .tint(Theme.accent)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 12)
    }

    private func firstPlayableEpisode(_ detail: AlbumDetailResponse) -> Discussion? {
        detail.episodes.first { $0.status == .ready }
    }

    /// Local destination resolution for the album's own stack (sheet mode).
    @ViewBuilder
    private func albumDestination(_ destination: LibraryDestination) -> some View {
        switch destination {
        case .discussion(let episode):
            episodeDestination(episode)
        case .album(let id):
            AlbumView(albumID: id)
        }
    }

    @ViewBuilder
    private func episodeDestination(_ episode: Discussion) -> some View {
        switch episode.status {
        case .planning, .failed:
            PlanConversationView(discussion: episode) { _ in
                Task { await load() }
            }
        case .generating, .ready:
            // Follow-up batches created from here appear in the album on
            // return; navigation stays within the album context.
            PodcastPlayerView(discussion: episode, onCreatedFollowUp: { _ in
                Task { await load() }
            })
        }
    }

    private func load() async {
        guard !albumID.isEmpty else {
            errorMessage = String(localized: "This podcast doesn't belong to an album yet.")
            isLoading = false
            return
        }
        do {
            detail = try await APIClient(tokens: auth).album(id: albumID)
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
        // The toolbar menu is server-rendered; a fetch failure just leaves the
        // menu in its loading state until the next refresh.
        if let actions = try? await APIClient(tokens: auth).albumUIActions(id: albumID) {
            actionItems = actions.items
        }
    }
}

/// Multi-select picker of ungrouped podcasts for the album's "Add Podcasts"
/// action. Podcasts already in another album are excluded (the server would
/// reject them with a 400).
private struct AlbumAddPodcastsSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let albumID: String
    var onAdded: () -> Void

    @State private var candidates: [Discussion] = []
    @State private var selected: Set<String> = []
    @State private var isLoading = true
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView().tint(Theme.accent).frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if candidates.isEmpty {
                    ContentUnavailableView(
                        "Nothing to add",
                        systemImage: "rectangle.stack",
                        description: Text("Every podcast already belongs to an album.")
                    )
                } else {
                    List(candidates) { candidate in
                        Button {
                            toggle(candidate.id)
                        } label: {
                            HStack(spacing: 12) {
                                DiscussionCoverThumbnail(discussion: candidate, size: 40)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(candidate.displayTitle)
                                        .font(.body.weight(.medium))
                                        .foregroundStyle(.primary)
                                        .lineLimit(2)
                                }
                                Spacer(minLength: 0)
                                Image(systemName: selected.contains(candidate.id) ? "checkmark.circle.fill" : "circle")
                                    .font(.title3)
                                    .foregroundStyle(selected.contains(candidate.id) ? Theme.accent : Color.secondary)
                            }
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("albumAdd.row.\(candidate.id)")
                    }
                    .listStyle(.plain)
                    .scrollContentBackground(.hidden)
                }
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle("Add Podcasts")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        submit()
                    } label: {
                        if isSubmitting { ProgressView() } else { Text("Add") }
                    }
                    .disabled(selected.isEmpty || isSubmitting)
                    .accessibilityIdentifier("albumAdd.submit")
                }
            }
        }
        .presentationDetents([.medium, .large])
        .alert("Couldn't add podcasts", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .task { await load() }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private func toggle(_ id: String) {
        if selected.contains(id) {
            selected.remove(id)
        } else {
            selected.insert(id)
        }
    }

    private func load() async {
        do {
            let all = try await APIClient(tokens: auth).discussions(limit: 100)
            candidates = all.filter { ($0.albumID ?? "").isEmpty }
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
    }

    private func submit() {
        guard !selected.isEmpty else { return }
        isSubmitting = true
        Task {
            do {
                _ = try await APIClient(tokens: auth).addToAlbum(id: albumID, discussionIDs: Array(selected))
                isSubmitting = false
                onAdded()
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// One episode row inside the album page: track number (or chapter range),
/// title, and status/duration.
private struct AlbumEpisodeRow: View {
    let episode: Discussion
    let number: Int

    var body: some View {
        HStack(spacing: 12) {
            Text("\(number)")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
                .frame(width: 26)
            VStack(alignment: .leading, spacing: 3) {
                Text(episode.displayTitle)
                    .font(.body.weight(.medium))
                    .lineLimit(2)
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer(minLength: 0)
            trailing
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder
    private var trailing: some View {
        switch episode.status {
        case .generating:
            ProgressView().controlSize(.small).tint(Theme.accent)
        case .ready:
            Image(systemName: "play.circle")
                .font(.title3)
                .foregroundStyle(Theme.accent)
        case .planning:
            Image(systemName: "pencil.and.list.clipboard")
                .foregroundStyle(Theme.secondaryText)
        case .failed:
            Image(systemName: "exclamationmark.triangle")
                .foregroundStyle(.orange)
        }
    }

    private var subtitle: String {
        var parts: [String] = []
        if let range = chapterRangeLabel {
            parts.append(range)
        }
        switch episode.status {
        case .generating:
            parts.append(String(localized: "Generating…"))
        case .planning:
            parts.append(String(localized: "Planning"))
        case .failed:
            parts.append(String(localized: "Failed"))
        case .ready:
            if let duration = durationLabel {
                parts.append(duration)
            }
        }
        return parts.isEmpty ? String(localized: "Episode") : parts.joined(separator: " · ")
    }

    /// "Chapters 6-8" for audiobook batch episodes, from the recorded global
    /// chapter indices.
    private var chapterRangeLabel: String? {
        guard let indices = episode.script?.audioBookChapterIndices, !indices.isEmpty else { return nil }
        let sorted = indices.sorted()
        if sorted.count == 1 { return String(localized: "Chapter \(sorted[0])") }
        let contiguous = zip(sorted, sorted.dropFirst()).allSatisfy { $1 == $0 + 1 }
        if contiguous { return String(localized: "Chapters \(sorted.first!)-\(sorted.last!)") }
        return String(localized: "Chapters \(sorted.map(String.init).joined(separator: ", "))")
    }

    private var durationLabel: String? {
        guard let seconds = episode.durationSeconds, seconds > 0 else { return nil }
        let total = Int(seconds.rounded())
        let minutes = total / 60
        let remainder = total % 60
        return String(format: "%d:%02d", minutes, remainder)
    }
}

/// Renders an album cover (image, gradient, or waveform fallback) — the album
/// counterpart of `DiscussionCoverThumbnail`.
struct AlbumCoverThumbnail: View {
    let cover: DiscussionCover?
    let size: CGFloat

    var body: some View {
        Group {
            if let url = cover?.renderableImageURL {
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
            } else if let cover, cover.hasGradient {
                LinearGradient(colors: [color(cover.gradientStart), color(cover.gradientEnd)],
                               startPoint: .topLeading,
                               endPoint: .bottomTrailing)
            } else {
                fallback
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: size > 100 ? 16 : 8))
    }

    private var fallback: some View {
        ZStack {
            LinearGradient(colors: [Theme.accent.opacity(0.75), Color.orange.opacity(0.72)],
                           startPoint: .topLeading,
                           endPoint: .bottomTrailing)
            Image(systemName: "rectangle.stack")
                .font(.system(size: size * 0.34, weight: .semibold))
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
