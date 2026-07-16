import Kingfisher
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
    var mode: AlbumViewMode = .owner
    var onOpenEpisode: ((Discussion) -> Void)?

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
            episodeOpenControl(episode, number: number)
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
            episodeOpenControl(episode, number: number)
                .accessibilityIdentifier("album.episode.\(episode.id)")
                .listRowBackground(Color.clear)
        }
    }

    @ViewBuilder
    private func episodeOpenControl(_ episode: Discussion, number: Int) -> some View {
        if let onOpenEpisode {
            Button {
                onOpenEpisode(episode)
            } label: {
                AlbumEpisodeRow(episode: episode, number: number)
            }
            .buttonStyle(.plain)
        } else {
            NavigationLink(value: LibraryDestination.discussion(episode)) {
                AlbumEpisodeRow(episode: episode, number: number)
            }
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
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 12)
    }

    /// Local destination resolution for the album's own stack (sheet mode).
    @ViewBuilder
    private func albumDestination(_ destination: LibraryDestination) -> some View {
        switch destination {
        case .discussion(let episode):
            episodeDestination(episode)
        case .album(let id):
            AlbumView(albumID: id, mode: mode, onOpenEpisode: onOpenEpisode)
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
            }, hidesTabBar: mode == .publicMarket)
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
