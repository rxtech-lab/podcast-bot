import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

extension LibraryView {
    func load(visibility: LibraryVisibilityFilter? = nil,
                      type: LibraryTypeFilter? = nil,
                      showsSearchOverlay: Bool = false) async {
        let filter = visibility ?? visibilityFilter
        let podcastType = type ?? typeFilter
        if showsSearchOverlay {
            isSearchLoading = true
        }
        isLoading = true
        defer {
            isLoading = false
            if showsSearchOverlay && visibilityFilter == filter && typeFilter == podcastType {
                isSearchLoading = false
            }
            hasLoadedInitialPage = true
        }
        do {
            let items = try await APIClient(tokens: auth).discussions(
                limit: pageSize,
                offset: 0,
                query: "",
                visibility: filter.apiVisibility,
                type: podcastType.apiType
            )
            guard visibilityFilter == filter,
                  typeFilter == podcastType else { return }
            let selectedID = selectedDiscussionID
            loadedVisibilityFilter = filter
            loadedTypeFilter = podcastType
            discussions = items
            loadErrorMessage = nil
            // Reconcile the iPad detail selection with the refreshed list so the
            // selected row stays highlighted and the detail reflects the newest copy.
            // Only update when the refreshed page still contains it — a selection
            // from a later page must not be dropped by a first-page refresh.
            // (Explicit deletion is what clears selection.)
            if let selectedID, let refreshed = items.first(where: { $0.id == selectedID }) {
                selection = .discussion(refreshed)
            }
            canLoadMore = items.count == pageSize
        } catch {
            reportLoadError(error, inlineWhenEmpty: true)
        }
    }

    func loadInitialPageIfNeeded() async {
        guard !hasLoadedInitialPage, !isLoading else { return }
        await load()
    }

    func loadMore() async {
        guard canLoadMore, !isLoadingMore, !isLoading else { return }
        let filter = loadedVisibilityFilter
        let podcastType = loadedTypeFilter
        let offset = discussions.count
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let items = try await APIClient(tokens: auth).discussions(
                limit: pageSize,
                offset: offset,
                query: "",
                visibility: filter.apiVisibility,
                type: podcastType.apiType
            )
            guard visibilityFilter == filter,
                  loadedVisibilityFilter == filter,
                  typeFilter == podcastType,
                  loadedTypeFilter == podcastType else { return }
            let existing = Set(discussions.map(\.id))
            discussions.append(contentsOf: items.filter { !existing.contains($0.id) })
            canLoadMore = items.count == pageSize
        } catch {
            reportLoadError(error, inlineWhenEmpty: false)
        }
    }

    func deleteDiscussion(_ target: Discussion) {
        discussions.removeAll { $0.id == target.id }
        path.removeAll { destinationDiscussionID($0) == target.id }
        if let selection, destinationDiscussionID(selection) == target.id { self.selection = nil }
        Task {
            do {
                try await APIClient(tokens: auth).deleteDiscussion(id: target.id)
            } catch {
                reportLoadError(error, inlineWhenEmpty: false)
                await load(visibility: loadedVisibilityFilter, type: loadedTypeFilter)
            }
        }
    }

    func beginRenameDiscussion(_ discussion: Discussion) {
        renamingDiscussion = discussion
        renamingDiscussionTitle = discussion.displayTitle
    }

    func beginRenameAlbum(_ album: AlbumSummaryDTO) {
        renamingAlbum = album
        renamingAlbumTitle = album.title
    }

    func renameSelectedDiscussion() {
        guard let target = renamingDiscussion else { return }
        let title = renamingDiscussionTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        renamingDiscussion = nil
        Task {
            do {
                let updated = try await APIClient(tokens: auth).renameDiscussion(id: target.id, title: title)
                replaceDiscussion(updated)
            } catch {
                reportLoadError(error, inlineWhenEmpty: false)
            }
        }
    }

    func renameSelectedAlbum() {
        guard let target = renamingAlbum else { return }
        let title = renamingAlbumTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        renamingAlbum = nil
        Task {
            do {
                let updated = try await APIClient(tokens: auth).renameAlbum(id: target.id, title: title)
                replaceAlbumSummary(updated)
            } catch {
                reportLoadError(error, inlineWhenEmpty: false)
            }
        }
    }

    func replaceDiscussion(_ updated: Discussion) {
        if let index = discussions.firstIndex(where: { $0.id == updated.id }) {
            discussions[index] = updated
        }
        path = path.map { destination in
            if case .discussion(let d) = destination, d.id == updated.id {
                return .discussion(updated)
            }
            return destination
        }
        if let selection, destinationDiscussionID(selection) == updated.id {
            self.selection = .discussion(updated)
        }
    }

    func replaceAlbumSummary(_ updated: AlbumDTO) {
        for index in discussions.indices {
            guard discussions[index].album?.id == updated.id else { continue }
            discussions[index].album?.title = updated.title
            discussions[index].album?.cover = updated.cover
            discussions[index].album?.episodeCount = updated.episodeCount
        }
    }

    func destinationDiscussionID(_ destination: LibraryDestination) -> String? {
        if case .discussion(let d) = destination { return d.id }
        return nil
    }

    /// The id of the discussion shown in the iPad detail column, for row
    /// highlighting; nil when an album (or nothing) is selected.
    var selectedDiscussionID: String? {
        guard let selection else { return nil }
        return destinationDiscussionID(selection)
    }

    /// Stable identity for the iPad detail column so switching selections
    /// rebuilds the destination view.
    func selectionIdentity(_ destination: LibraryDestination) -> String {
        switch destination {
        case .discussion(let d): return "discussion:\(d.id)"
        case .album(let id): return "album:\(id)"
        }
    }

    func reportLoadError(_ error: Error, inlineWhenEmpty: Bool) {
        guard !APIClient.isCancellation(error) else { return }
        let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
        if inlineWhenEmpty && discussions.isEmpty {
            loadErrorMessage = message
        } else {
            errorMessage = message
        }
    }

    func upsert(_ discussion: Discussion) {
        loadErrorMessage = nil
        discussions.removeAll { $0.id == discussion.id }
        discussions.insert(discussion, at: 0)
    }

    func scheduleSearch(for text: String) {
        let query = normalizedSearchQuery(text)
        searchTask?.cancel()
        guard !query.isEmpty else {
            isSearchLoading = false
            semanticGroups = nil
            loadedSearchQuery = ""
            return
        }
        guard query != loadedSearchQuery else {
            isSearchLoading = false
            return
        }
        isSearchLoading = true
        searchTask = Task {
            try? await Task.sleep(for: .milliseconds(350))
            guard !Task.isCancelled else { return }
            await searchSemantic(query: query)
        }
    }

    /// Global semantic search over indexed podcast content, grouped by
    /// podcast. Falls back to the title-substring search when embeddings are
    /// not configured on the server, so search never goes dark.
    func searchSemantic(query: String) async {
        isSearchLoading = true
        defer {
            if normalizedSearchQuery(searchText) == query {
                isSearchLoading = false
            }
        }
        do {
            let response = try await APIClient(tokens: auth).semanticSearch(query: query)
            guard normalizedSearchQuery(searchText) == query else { return }
            guard response.enabled else {
                // Embeddings are off on the server: fall back to a title
                // substring search, rendered through the same grouped UI (a
                // group with no matches is just the podcast header row).
                let items = try await APIClient(tokens: auth).discussions(
                    limit: pageSize,
                    offset: 0,
                    query: query,
                    visibility: visibilityFilter.apiVisibility,
                    type: typeFilter.apiType
                )
                guard normalizedSearchQuery(searchText) == query else { return }
                loadedSearchQuery = query
                semanticGroups = items.map { SemanticSearchGroup(discussion: $0, matches: []) }
                loadErrorMessage = nil
                return
            }
            loadedSearchQuery = query
            semanticGroups = response.results
            loadErrorMessage = nil
        } catch {
            guard normalizedSearchQuery(searchText) == query else { return }
            reportLoadError(error, inlineWhenEmpty: false)
        }
    }

    func normalizedSearchQuery(_ text: String) -> String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var emptyState: some View {
        VStack(spacing: 16) {
            Image(systemName: "waveform.circle")
                .font(.system(size: 56))
                .foregroundStyle(Theme.accent)
            Text("No \(AppStringLiteral.stationsNameRaw) yet")
                .font(.title3.weight(.semibold))
            Text("Plan an AI \(AppStringLiteral.stationNameRaw) and generate the audio.")
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
            Button {
                showingNew = true
            } label: {
                Label("New \(AppStringLiteral.stationNameRaw)", systemImage: "plus")
                    .padding(.horizontal, 8)
            }
            .buttonStyle(.glassProminent)
            .tint(Theme.accent)
        }
        .padding(40)
    }

    var loadErrorState: some View {
        VStack(spacing: 16) {
            Image(systemName: "wifi.exclamationmark")
                .font(.system(size: 52, weight: .semibold))
                .foregroundStyle(Theme.accent)
            Text("Could not load \(AppStringLiteral.stationsNameRaw)")
                .font(.title3.weight(.semibold))
            Text(loadErrorMessage ?? "Check your connection and try again.")
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
            Button {
                loadErrorMessage = nil
                Task {
                    await load(visibility: visibilityFilter, type: typeFilter)
                    await loadHomeToolbar()
                }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
                    .padding(.horizontal, 8)
            }
            .buttonStyle(.glassProminent)
            .tint(Theme.accent)
            .accessibilityIdentifier("library.refresh")
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    var searchEmptyState: some View {
        ContentUnavailableView(
            "No Results",
            systemImage: "magnifyingglass",
            description: Text("No \(AppStringLiteral.stationsNameRaw) match your search.")
        )
    }

    var searchPromptState: some View {
        ContentUnavailableView(
            "Search \(AppStringLiteral.stationsNameRaw)",
            systemImage: "magnifyingglass",
            description: Text("Find moments across your \(AppStringLiteral.stationsNameRaw).")
        )
    }

    var filterEmptyState: some View {
        ContentUnavailableView(
            loadedTypeFilter == .all ? loadedVisibilityFilter.emptyTitle : loadedTypeFilter.emptyTitle,
            systemImage: loadedTypeFilter == .all ? loadedVisibilityFilter.icon : loadedTypeFilter.icon,
            description: Text(loadedTypeFilter == .all ? loadedVisibilityFilter.emptyMessage : loadedTypeFilter.emptyMessage)
        )
    }

    var searchLoadingOverlay: some View {
        ZStack {
            Color.black.opacity(0.001)
                .ignoresSafeArea()

            HStack(spacing: 12) {
                ProgressView()
                    .tint(Theme.accent)
                Text("Searching...")
                    .font(.subheadline.weight(.semibold))
            }
            .glassCard(cornerRadius: 18)
        }
    }

    /// Carry the active detail across a size-class change so resizing into
    /// Slide Over / Stage Manager (or back) keeps the open discussion instead
    /// of snapping to the list or the placeholder.
    func syncNavigation(toRegular: Bool) {
        if toRegular {
            // Stack -> split: surface the top of the pushed stack as the selection.
            selection = path.last
            path = []
        } else {
            // Split -> stack: rebuild the stack from the current selection.
            path = selection.map { [$0] } ?? []
        }
    }

    /// Open a discussion's detail: drives `selection` on iPad, pushes onto
    /// `path` on iPhone.
    func navigate(to discussion: Discussion) {
        if isRegular {
            selection = .discussion(discussion)
        } else {
            path.append(.discussion(discussion))
        }
    }

    /// Open an album's episode list.
    func navigateToAlbum(id: String) {
        if isRegular {
            selection = .album(id: id)
        } else {
            path.append(.album(id: id))
        }
    }

    /// Swap the currently-shown discussion for its updated value so a planned
    /// discussion transitions in place to a player, in whichever model is active.
    func replaceCurrent(with generated: Discussion) {
        if isRegular {
            selection = .discussion(generated)
        } else if let index = path.lastIndex(where: { destinationDiscussionID($0) == generated.id }) {
            path[index] = .discussion(generated)
        } else {
            path.append(.discussion(generated))
        }
    }

    @ViewBuilder
    func destinationView(_ destination: LibraryDestination) -> some View {
        switch destination {
        case .discussion(let discussion):
            discussionDestination(discussion)
        case .album(let id):
            AlbumView(albumID: id)
        }
    }

    @ViewBuilder
    func discussionDestination(_ discussion: Discussion) -> some View {
        switch discussion.status {
        case .planning, .failed:
            // New discussions plan conversationally; legacy plans are seeded into
            // the same view from their saved script (see PlanConversationView.start).
            PlanConversationView(discussion: discussion) { generated in
                upsert(generated)
                replaceCurrent(with: generated)
            }
        case .generating, .ready:
            PodcastPlayerView(discussion: discussion,
                              onCreatedFollowUp: { created in
                                  upsert(created)
                                  navigate(to: created)
                              },
                              hidesTabBar: true)
        }
    }

    /// Destination views pushed within the search tab's own stack. Mirrors
    /// `destinationView` but drives `searchPath`, so results open in place on
    /// both size classes instead of targeting the Home tab's navigation.
    @ViewBuilder
    func searchDestinationView(_ destination: LibraryDestination) -> some View {
        switch destination {
        case .discussion(let discussion):
            switch discussion.status {
            case .planning, .failed:
                PlanConversationView(discussion: discussion) { generated in
                    upsert(generated)
                    if let index = searchPath.lastIndex(where: { destinationDiscussionID($0) == generated.id }) {
                        searchPath[index] = .discussion(generated)
                    } else {
                        searchPath.append(.discussion(generated))
                    }
                }
            case .generating, .ready:
                PodcastPlayerView(discussion: discussion,
                                  onCreatedFollowUp: { created in
                                      upsert(created)
                                      searchPath.append(.discussion(created))
                                  },
                                  hidesTabBar: true)
            }
        case .album(let id):
            AlbumView(albumID: id)
        }
    }
}
