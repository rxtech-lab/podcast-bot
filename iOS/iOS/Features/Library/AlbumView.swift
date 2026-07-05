import Kingfisher
import SwiftUI

enum AlbumViewMode {
    case owner
    case publicMarket
}

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
    var mode: AlbumViewMode = .owner

    @State private var detail: AlbumDetailResponse?
    @State private var actionItems: [DiscussionUIActionItem] = []
    @State private var isLoading = true
    @State private var errorMessage: String?
    @State private var actionError: String?
    @State private var showingChapterChecklist = false
    @State private var showingAddPodcasts = false
    @State private var showingPublish = false
    @State private var showingCoverEditor = false
    @State private var showingRename = false
    @State private var showingRemoveConfirm = false
    @State private var renameTitle = ""
    @State private var renamingEpisode: Discussion?
    @State private var renamingEpisodeTitle = ""

    var body: some View {
        if ownsNavigation || mode == .publicMarket {
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
        .sheet(isPresented: $showingPublish) {
            publishAlbumSheet
        }
        .sheet(isPresented: $showingCoverEditor) {
            coverEditorSheet
        }
        .alert("Rename Album", isPresented: $showingRename) {
            TextField("Album name", text: $renameTitle)
            Button("Rename") { rename() }
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename Podcast", isPresented: renamingEpisodeBinding) {
            TextField("Podcast name", text: $renamingEpisodeTitle)
            Button("Rename") { renameEpisode() }
            Button("Cancel", role: .cancel) { renamingEpisode = nil }
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
        if canManageAlbum, let publish = publishAction {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    performAlbumAction(publish)
                } label: {
                    Image(systemName: publish.systemImage ?? "globe")
                }
                .accessibilityLabel(publish.title)
                .accessibilityIdentifier("album.publish")
                .disabled(!publish.enabled)
            }
        }
        if canManageAlbum, !menuActionItems.isEmpty {
            ToolbarItem(placement: .topBarTrailing) {
            DiscussionActionsMenu(
                items: menuActionItems,
                labelSystemImage: "ellipsis",
                accessibilityLabel: "Album actions",
                isBusy: { _ in false },
                perform: performAlbumAction
            )
            .accessibilityIdentifier("album.more")
            }
        }
    }

    private var canManageAlbum: Bool {
        mode == .owner && (detail?.album.isOwner ?? true)
    }

    private var publishAction: DiscussionUIActionItem? {
        actionItems.first { $0.id == "publish-album" }
    }

    private var menuActionItems: [DiscussionUIActionItem] {
        actionItems.filter { $0.id != "publish-album" }
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
        case ["sheet", "publish"]:
            showingPublish = true
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

    @ViewBuilder
    private var publishAlbumSheet: some View {
        if let detail {
            AlbumPublishSheet(detail: detail) { updated in
                self.detail = updated
                showingPublish = false
            }
        }
    }

    private var actionErrorBinding: Binding<Bool> {
        Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )
    }

    private var renamingEpisodeBinding: Binding<Bool> {
        Binding(
            get: { renamingEpisode != nil },
            set: { if !$0 { renamingEpisode = nil } }
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

    private func beginRenameEpisode(_ episode: Discussion) {
        renamingEpisode = episode
        renamingEpisodeTitle = episode.displayTitle
    }

    private func renameEpisode() {
        guard let episode = renamingEpisode else { return }
        let title = renamingEpisodeTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        renamingEpisode = nil
        Task {
            do {
                let updated = try await APIClient(tokens: auth).renameDiscussion(id: episode.id, title: title)
                if let index = detail?.episodes.firstIndex(where: { $0.id == updated.id }) {
                    detail?.episodes[index] = updated
                }
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
                    episodeRow(episode, number: index + 1)
                }
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
    }

    @ViewBuilder
    private func episodeRow(_ episode: Discussion, number: Int) -> some View {
        if canManageAlbum {
            NavigationLink(value: LibraryDestination.discussion(episode)) {
                AlbumEpisodeRow(episode: episode, number: number)
            }
            .accessibilityIdentifier("album.episode.\(episode.id)")
            .listRowBackground(Color.clear)
            .swipeActions(edge: .trailing) {
                Button(role: .destructive) {
                    removeEpisode(episode)
                } label: {
                    Label("Remove from Album", systemImage: "rectangle.stack.badge.minus")
                }
                Button {
                    beginRenameEpisode(episode)
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                .tint(Theme.accent)
            }
        } else {
            NavigationLink(value: LibraryDestination.discussion(episode)) {
                AlbumEpisodeRow(episode: episode, number: number)
            }
            .accessibilityIdentifier("album.episode.\(episode.id)")
            .listRowBackground(Color.clear)
        }
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
            AlbumView(albumID: id, mode: mode)
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
            if mode == .owner {
                detail = try await APIClient(tokens: auth).album(id: albumID)
            } else {
                detail = try await APIClient(tokens: auth).publicAlbum(id: albumID)
            }
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
        // The toolbar menu is server-rendered; a fetch failure just leaves the
        // menu in its loading state until the next refresh.
        if mode == .owner, let actions = try? await APIClient(tokens: auth).albumUIActions(id: albumID) {
            actionItems = actions.items
        } else {
            actionItems = []
        }
    }
}

private enum AlbumPublishMode: String, CaseIterable, Identifiable {
    case all
    case selected

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all:
            return "All Podcasts"
        case .selected:
            return "Selected"
        }
    }
}

private struct AlbumPublishSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let detail: AlbumDetailResponse
    var onPublished: (AlbumDetailResponse) -> Void

    @State private var mode: AlbumPublishMode = .all
    @State private var selected: Set<String>
    @State private var cover: DiscussionCover
    @State private var isWorking = false
    @State private var errorMessage: String?

    init(detail: AlbumDetailResponse, onPublished: @escaping (AlbumDetailResponse) -> Void) {
        self.detail = detail
        self.onPublished = onPublished
        _selected = State(initialValue: Set(detail.episodes.map(\.id)))
        let initialCover = detail.album.cover?.isPublishable == true ? detail.album.cover! : .defaultGradient
        _cover = State(initialValue: initialCover)
    }

    var body: some View {
        NavigationStack {
            Form {
                CoverEditor(target: .album(id: detail.album.id),
                            title: detail.album.title,
                            cover: $cover,
                            isWorking: $isWorking)

                Section {
                    Picker("Publish", selection: $mode) {
                        ForEach(AlbumPublishMode.allCases) { item in
                            Text(item.title).tag(item)
                        }
                    }
                    .pickerStyle(.segmented)
                    .accessibilityIdentifier("albumPublish.mode")
                }

                if mode == .selected {
                    Section("Podcasts") {
                        ForEach(detail.episodes) { episode in
                            Button {
                                toggleSelection(episode.id)
                            } label: {
                                HStack(spacing: 12) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(episode.displayTitle)
                                            .foregroundStyle(.primary)
                                        Text(episode.status.rawValue.capitalized)
                                            .font(.caption)
                                            .foregroundStyle(Theme.secondaryText)
                                    }
                                    Spacer()
                                    Image(systemName: selected.contains(episode.id) ? "checkmark.circle.fill" : "circle")
                                        .foregroundStyle(selected.contains(episode.id) ? Theme.accent : Theme.secondaryText)
                                }
                            }
                            .buttonStyle(.plain)
                            .accessibilityIdentifier("albumPublish.row.\(episode.id)")
                        }
                    }
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Publish Album")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        publish()
                    } label: {
                        if isWorking {
                            ProgressView()
                                .controlSize(.small)
                        } else {
                            Text("Publish")
                        }
                    }
                    .disabled(isWorking || !cover.isPublishable || publishIDs.isEmpty)
                    .accessibilityIdentifier("albumPublish.submit")
                }
            }
        }
        .presentationDetents([.large])
        .interactiveDismissDisabled(isWorking)
        .accessibilityIdentifier("albumPublish.sheet")
    }

    private var publishIDs: [String] {
        switch mode {
        case .all:
            detail.episodes.map(\.id)
        case .selected:
            detail.episodes.map(\.id).filter { selected.contains($0) }
        }
    }

    private func toggleSelection(_ id: String) {
        if selected.contains(id) {
            selected.remove(id)
        } else {
            selected.insert(id)
        }
    }

    private func publish() {
        isWorking = true
        errorMessage = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                let updated = try await APIClient(tokens: auth).publishAlbum(
                    id: detail.album.id,
                    mode: mode.rawValue,
                    discussionIDs: publishIDs,
                    cover: cover
                )
                onPublished(updated)
                dismiss()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
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
            DiscussionCoverThumbnail(discussion: episode, size: 44)
                .accessibilityIdentifier("album.episode.cover.\(episode.id)")
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
                KFImage.url(url)
                    .placeholder {
                        fallback
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFill()
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
