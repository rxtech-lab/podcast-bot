import RxAuthSwift
import SwiftUI
import TipKit
import UIKit

/// A pushable destination in the library's navigation: an individual podcast
/// (plan or player, by status) or an album's episode list.
enum LibraryDestination: Hashable {
    case discussion(Discussion)
    case album(id: String)
}

/// Home: the user's server-owned discussions, newest first. Podcasts that
/// belong to an album (audiobook chapter batches, follow-ups, manual groups)
/// collapse into one album row that opens the album's episode list.
struct LibraryView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(\.horizontalSizeClass) private var hSize
    @State private var discussions: [Discussion] = []
    @State private var showingNew = false
    @State private var showingNewAlbum = false
    @State private var showingPointsHistory = false
    @State private var showingSettings = false
    @State private var showingWhatsNew = false
    @State private var showingMarketplace = false
    @State private var path: [LibraryDestination] = []
    /// Detail selection for the iPad split-view layout.
    @State private var selection: LibraryDestination?
    @State private var isLoading = false
    @State private var hasLoadedInitialPage = false
    @State private var isLoadingMore = false
    @State private var canLoadMore = true
    @State private var errorMessage: String?
    @State private var searchText = ""
    @State private var loadedSearchQuery = ""
    @State private var visibilityFilter: LibraryVisibilityFilter = .all
    @State private var loadedVisibilityFilter: LibraryVisibilityFilter = .all
    @State private var toolbarItems: [DiscussionUIActionItem] = []
    @State private var isSearchLoading = false
    @State private var searchTask: Task<Void, Never>?
    private let pageSize = 20

    private var isRegular: Bool { hSize == .regular }

    var body: some View {
        withLifecycle(withPresentations(
            Group {
                if isRegular { splitView } else { stackView }
            }
        ))
    }

    /// Sheets, covers, and alerts hung off the root view. Split out of `body`
    /// (with `withLifecycle`) to keep the expression type-checkable.
    private func withPresentations(_ content: some View) -> some View {
        content
            .sheet(isPresented: $showingNew) {
                NewDiscussionView { discussion in
                    showingNew = false
                    upsert(discussion)
                    navigate(to: discussion)
                }
            }
            .sheet(isPresented: $showingNewAlbum) {
                NewAlbumSheet { album in
                    showingNewAlbum = false
                    navigateToAlbum(id: album.id)
                }
            }
            .alert("Could not load \(AppStringLiteral.stationsNameRaw)", isPresented: errorBinding) {
                Button("OK", role: .cancel) { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .sheet(isPresented: $showingPointsHistory) {
                PointsHistoryView()
            }
            .fullScreenCover(isPresented: $showingSettings) {
                LibrarySettingsView(
                    userName: auth.currentUser?.name,
                    userID: auth.currentUser?.id,
                    canManageSubscription: purchases.isConfigured,
                    pointsLabel: purchases.isConfigured ? pointsMenuLabel : nil
                )
            }
            .sheet(isPresented: $showingWhatsNew) {
                WhatsNewSheet(features: WhatsNewFeature.all,
                              allowsInteractiveDismiss: true)
                {
                    showingWhatsNew = false
                }
            }
            .fullScreenCover(isPresented: $showingMarketplace) {
                MarketplaceView { discussion in
                    showingMarketplace = false
                    upsert(discussion)
                    navigate(to: discussion)
                }
            }
    }

    /// Load tasks and change observers hung off the root view.
    private func withLifecycle(_ content: some View) -> some View {
        content
            .onChange(of: hSize) { _, newValue in
                syncNavigation(toRegular: newValue == .regular)
            }
            .task { await load() }
            .task { await loadHomeToolbar() }
            .task { await purchases.refreshBalance() }
            .onChange(of: purchases.isConfigured) { _, _ in
                Task { await loadHomeToolbar() }
            }
            .onChange(of: searchText) { _, newValue in
                scheduleSearch(for: newValue)
            }
            .onChange(of: visibilityFilter) { _, newValue in
                searchTask?.cancel()
                Task {
                    await loadHomeToolbar()
                    await load(visibility: newValue, showsSearchOverlay: hasLoadedInitialPage)
                }
            }
            .onDisappear {
                searchTask?.cancel()
                isSearchLoading = false
            }
    }

    /// iPhone / compact: single-column stack-based navigation.
    private var stackView: some View {
        NavigationStack(path: $path) {
            libraryContainer
                .navigationTitle(AppStringLiteral.stationTitle)
                .toolbar { libraryToolbar }
                .searchable(text: $searchText,
                            placement: .navigationBarDrawer(displayMode: .always),
                            prompt: "Search \(AppStringLiteral.stationsNameRaw)")
                .navigationDestination(for: LibraryDestination.self) { destination in
                    destinationView(destination)
                }
        }
    }

    /// iPad / regular: sidebar list + detail column.
    private var splitView: some View {
        NavigationSplitView {
            libraryContainer
                .navigationTitle(AppStringLiteral.stationTitle)
                .toolbar { libraryToolbar }
                .searchable(text: $searchText,
                            placement: .navigationBarDrawer(displayMode: .always),
                            prompt: "Search \(AppStringLiteral.stationsNameRaw)")
        } detail: {
            NavigationStack {
                if let selection {
                    destinationView(selection)
                        .id(selectionIdentity(selection))
                        .navigationDestination(for: LibraryDestination.self) { destination in
                            destinationView(destination)
                        }
                } else {
                    placeholder
                }
            }
        }
        .navigationSplitViewStyle(.balanced)
    }

    private var libraryContainer: some View {
        libraryContent
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.background.ignoresSafeArea())
            .overlay(alignment: .center) {
                if isSearchLoading && hasLoadedInitialPage {
                    searchLoadingOverlay
                        .transition(.opacity.combined(with: .scale(scale: 0.96)))
                }
            }
            .animation(.easeInOut(duration: 0.18), value: isSearchLoading)
    }

    @ViewBuilder
    private var libraryContent: some View {
        if shouldShowInitialLoader {
            initialLibraryLoadingView
        } else if discussions.isEmpty && !loadedSearchQuery.isEmpty {
            searchEmptyState
        } else if discussions.isEmpty && loadedVisibilityFilter != .all {
            visibilityEmptyState
        } else if discussions.isEmpty {
            emptyState
        } else {
            list
        }
    }

    @ToolbarContentBuilder
    private var libraryToolbar: some ToolbarContent {
        DefaultToolbarItem(kind: .search, placement: .bottomBar)
        ToolbarItemGroup(placement: .topBarLeading) {
            ForEach(leadingToolbarItems) { item in
                homeToolbarItem(item)
            }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            ForEach(trailingToolbarItems) { item in
                homeToolbarItem(item)
            }
        }
    }

    private var leadingToolbarItems: [DiscussionUIActionItem] {
        toolbarItems.filter { $0.placement == "topBarLeading" }
    }

    private var trailingToolbarItems: [DiscussionUIActionItem] {
        toolbarItems.filter { $0.placement != "topBarLeading" }
    }

    @ViewBuilder
    private func homeToolbarItem(_ item: DiscussionUIActionItem) -> some View {
        if item.children.count > 1 {
            Menu {
                ForEach(item.children) { child in
                    homeToolbarMenuLeaf(child)
                }
            } label: {
                homeToolbarIcon(item)
            }
            .accessibilityLabel(item.title)
            .accessibilityIdentifier("library.\(item.id)")
            .disabled(!item.enabled)
            .modifier(HomeToolbarTipModifier(itemID: item.id))
        } else if let child = item.children.first {
            Button(role: buttonRole(for: child)) {
                performHomeToolbarAction(child)
            } label: {
                homeToolbarIcon(child)
            }
            .accessibilityLabel(homeToolbarTitle(child))
            .accessibilityIdentifier("library.\(item.id)")
            .disabled(!child.enabled)
            .modifier(HomeToolbarTipModifier(itemID: item.id))
        } else {
            Button(role: buttonRole(for: item)) {
                performHomeToolbarAction(item)
            } label: {
                homeToolbarIcon(item)
            }
            .accessibilityLabel(homeToolbarTitle(item))
            .accessibilityIdentifier("library.\(item.id)")
            .disabled(!item.enabled)
            .modifier(HomeToolbarTipModifier(itemID: item.id))
        }
    }

    @ViewBuilder
    private func homeToolbarMenuLeaf(_ item: DiscussionUIActionItem) -> some View {
        let actionItem = item.children.first ?? item
        Button(role: buttonRole(for: actionItem)) {
            performHomeToolbarAction(actionItem)
        } label: {
            homeToolbarLabel(actionItem)
        }
        .disabled(!actionItem.enabled)
    }

    @ViewBuilder
    private func homeToolbarIcon(_ item: DiscussionUIActionItem) -> some View {
        Image(systemName: homeToolbarSystemImage(item))
    }

    @ViewBuilder
    private func homeToolbarLabel(_ item: DiscussionUIActionItem) -> some View {
        let title = homeToolbarTitle(item)
        if let systemImage = item.systemImage, !systemImage.isEmpty {
            Label(title, systemImage: systemImage)
        } else {
            Text(title)
        }
    }

    private func homeToolbarTitle(_ item: DiscussionUIActionItem) -> String {
        item.id == "points" ? pointsMenuLabel : item.title
    }

    private func homeToolbarSystemImage(_ item: DiscussionUIActionItem) -> String {
        guard let systemImage = item.systemImage, !systemImage.isEmpty else {
            return "ellipsis"
        }
        return systemImage
    }

    private func buttonRole(for item: DiscussionUIActionItem) -> ButtonRole? {
        item.role == "destructive" ? .destructive : nil
    }

    /// Balance label for the user menu, e.g. "Points (Balance 1,200 Points)".
    private var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Balance Unknown", comment: "User menu label when the points balance is unknown")
        }
        let unit = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Balance (\(UsageSummary.formatInt(balance)) \(unit))",
                      comment: "User menu points label; first value is the formatted balance, second is the localized unit")
    }

    private func loadHomeToolbar() async {
        do {
            let response = try await APIClient(tokens: auth).homeUIActions(
                supportsPoints: purchases.isConfigured,
                visibility: visibilityFilter.rawValue
            )
            toolbarItems = response.toolbars
        } catch {
            toolbarItems = []
        }
    }

    private func performHomeToolbarAction(_ item: DiscussionUIActionItem) {
        guard item.action.type != "none",
              let path = validatedHomeActionPath(item) else { return }
        switch path {
        case ["sheet", "points"]:
            showingPointsHistory = true
        case ["sheet", "settings"]:
            showingSettings = true
        case ["sheet", "whats-new"]:
            showingWhatsNew = true
        case ["sheet", "market"]:
            showingMarketplace = true
        case ["sheet", "new-station"]:
            showingNew = true
        case ["sheet", "new-album"]:
            showingNewAlbum = true
        case ["filter", "all"]:
            visibilityFilter = .all
        case ["filter", "public"]:
            visibilityFilter = .public
        case ["filter", "private"]:
            visibilityFilter = .private
        case ["action", "refresh"]:
            Task {
                await load(searchQuery: searchText, visibility: visibilityFilter)
                await purchases.refreshBalance()
                await loadHomeToolbar()
            }
        case ["action", "sign-out"]:
            Task { await auth.signOut() }
        default:
            break
        }
    }

    private func validatedHomeActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "home" else { return nil }
        return url.pathComponents.filter { $0 != "/" }
    }

    private var placeholder: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ContentUnavailableView(
                "Select a \(AppStringLiteral.stationNameRaw)",
                systemImage: "waveform.circle",
                description: Text("Pick a \(AppStringLiteral.stationNameRaw) from the list, or create a new one.")
            )
        }
    }

    /// One rendered row of the home list: an ungrouped podcast, or a whole
    /// album collapsed into a single group row (positioned by its newest
    /// member on the page).
    private enum LibraryListRow: Identifiable {
        case discussion(Discussion)
        case album(summary: AlbumSummaryDTO, newest: Discussion, count: Int)

        var id: String {
            switch self {
            case .discussion(let d): return "discussion:\(d.id)"
            case .album(let summary, _, _): return "album:\(summary.id)"
            }
        }
    }

    /// Collapses the page's discussions into rows, grouping album members
    /// (matched by the server-attached `album` summary) into one row placed at
    /// the newest member's position.
    private var listRows: [LibraryListRow] {
        var pageCounts: [String: Int] = [:]
        for d in discussions {
            if let albumID = d.album?.id {
                pageCounts[albumID, default: 0] += 1
            }
        }
        var seenAlbums = Set<String>()
        var rows: [LibraryListRow] = []
        for d in discussions {
            if let album = d.album {
                if seenAlbums.insert(album.id).inserted {
                    let count = max(Int(album.episodeCount ?? 0), pageCounts[album.id] ?? 1)
                    rows.append(.album(summary: album, newest: d, count: count))
                }
            } else {
                rows.append(.discussion(d))
            }
        }
        return rows
    }

    private var list: some View {
        let rows = listRows
        return List {
            ForEach(rows) { row in
                listRow(row)
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                    .listRowInsets(.init(top: 6, leading: 16, bottom: 6, trailing: 16))
                    .onAppear {
                        if row.id == rows.last?.id {
                            Task { await loadMore() }
                        }
                    }
            }

            if isLoadingMore {
                HStack {
                    Spacer()
                    ProgressView().tint(Theme.accent)
                    Spacer()
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .scrollDismissesKeyboard(.interactively)
        .refreshable { await load() }
        .background(Color.clear)
    }

    @ViewBuilder
    private func listRow(_ row: LibraryListRow) -> some View {
        switch row {
        case .discussion(let d):
            Button {
                navigate(to: d)
            } label: {
                DiscussionRow(discussion: d, isSelected: isRegular && selectedDiscussionID == d.id)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("discussion.row.\(d.id)")
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                Button(role: .destructive) {
                    deleteDiscussion(d)
                } label: {
                    Label("Delete", systemImage: "trash")
                }
            }
        case .album(let summary, let newest, let count):
            Button {
                navigateToAlbum(id: summary.id)
            } label: {
                AlbumGroupRow(summary: summary,
                              newest: newest,
                              episodeCount: count,
                              isSelected: isRegular && selection == .album(id: summary.id))
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("album.row.\(summary.id)")
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    private var shouldShowInitialLoader: Bool {
        discussions.isEmpty && (isLoading || !hasLoadedInitialPage)
    }

    private var initialLibraryLoadingView: some View {
        VStack(spacing: 12) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.12))
                    .frame(width: 52, height: 52)
                Image(systemName: "waveform.circle.fill")
                    .font(.system(size: 32, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            VStack(spacing: 4) {
                Text("Loading \(AppStringLiteral.stationsNameRaw)...")
                    .font(.headline)
                Text("Syncing your library")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            ProgressView()
                .tint(Theme.accent)
                .controlSize(.small)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(40)
        .multilineTextAlignment(.center)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Loading \(AppStringLiteral.stationsNameRaw)")
    }

    private func load(searchQuery: String? = nil,
                      visibility: LibraryVisibilityFilter? = nil,
                      showsSearchOverlay: Bool = false) async {
        let query = normalizedSearchQuery(searchQuery ?? searchText)
        let filter = visibility ?? visibilityFilter
        if showsSearchOverlay {
            isSearchLoading = true
        }
        isLoading = true
        defer {
            isLoading = false
            if showsSearchOverlay && normalizedSearchQuery(searchText) == query && visibilityFilter == filter {
                isSearchLoading = false
            }
            hasLoadedInitialPage = true
        }
        do {
            let items = try await APIClient(tokens: auth).discussions(
                limit: pageSize,
                offset: 0,
                query: query,
                visibility: filter.apiVisibility
            )
            guard normalizedSearchQuery(searchText) == query, visibilityFilter == filter else { return }
            let selectedID = selectedDiscussionID
            loadedSearchQuery = query
            loadedVisibilityFilter = filter
            discussions = items
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
            reportLoadError(error)
        }
    }

    private func loadMore() async {
        guard canLoadMore, !isLoadingMore, !isLoading else { return }
        let query = loadedSearchQuery
        let filter = loadedVisibilityFilter
        let offset = discussions.count
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let items = try await APIClient(tokens: auth).discussions(
                limit: pageSize,
                offset: offset,
                query: query,
                visibility: filter.apiVisibility
            )
            guard normalizedSearchQuery(searchText) == query,
                  loadedSearchQuery == query,
                  visibilityFilter == filter,
                  loadedVisibilityFilter == filter else { return }
            let existing = Set(discussions.map(\.id))
            discussions.append(contentsOf: items.filter { !existing.contains($0.id) })
            canLoadMore = items.count == pageSize
        } catch {
            reportLoadError(error)
        }
    }

    private func deleteDiscussion(_ target: Discussion) {
        discussions.removeAll { $0.id == target.id }
        path.removeAll { destinationDiscussionID($0) == target.id }
        if let selection, destinationDiscussionID(selection) == target.id { self.selection = nil }
        Task {
            do {
                try await APIClient(tokens: auth).deleteDiscussion(id: target.id)
            } catch {
                reportLoadError(error)
                await load(searchQuery: loadedSearchQuery, visibility: loadedVisibilityFilter)
            }
        }
    }

    private func destinationDiscussionID(_ destination: LibraryDestination) -> String? {
        if case .discussion(let d) = destination { return d.id }
        return nil
    }

    /// The id of the discussion shown in the iPad detail column, for row
    /// highlighting; nil when an album (or nothing) is selected.
    private var selectedDiscussionID: String? {
        guard let selection else { return nil }
        return destinationDiscussionID(selection)
    }

    /// Stable identity for the iPad detail column so switching selections
    /// rebuilds the destination view.
    private func selectionIdentity(_ destination: LibraryDestination) -> String {
        switch destination {
        case .discussion(let d): return "discussion:\(d.id)"
        case .album(let id): return "album:\(id)"
        }
    }

    private func reportLoadError(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }

    private func upsert(_ discussion: Discussion) {
        discussions.removeAll { $0.id == discussion.id }
        discussions.insert(discussion, at: 0)
    }

    private func scheduleSearch(for text: String) {
        let query = normalizedSearchQuery(text)
        searchTask?.cancel()
        guard !query.isEmpty else {
            isSearchLoading = false
            guard !loadedSearchQuery.isEmpty else { return }
            searchTask = Task {
                await load(searchQuery: "", visibility: visibilityFilter)
            }
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
            await load(searchQuery: text, visibility: visibilityFilter, showsSearchOverlay: true)
        }
    }

    private func normalizedSearchQuery(_ text: String) -> String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var emptyState: some View {
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

    private var searchEmptyState: some View {
        ContentUnavailableView(
            "No Results",
            systemImage: "magnifyingglass",
            description: Text("No \(AppStringLiteral.stationsNameRaw) match your search.")
        )
    }

    private var visibilityEmptyState: some View {
        ContentUnavailableView(
            loadedVisibilityFilter.emptyTitle,
            systemImage: loadedVisibilityFilter.icon,
            description: Text(loadedVisibilityFilter.emptyMessage)
        )
    }

    private var searchLoadingOverlay: some View {
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
    private func syncNavigation(toRegular: Bool) {
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
    private func navigate(to discussion: Discussion) {
        if isRegular {
            selection = .discussion(discussion)
        } else {
            path.append(.discussion(discussion))
        }
    }

    /// Open an album's episode list.
    private func navigateToAlbum(id: String) {
        if isRegular {
            selection = .album(id: id)
        } else {
            path.append(.album(id: id))
        }
    }

    /// Swap the currently-shown discussion for its updated value so a planned
    /// discussion transitions in place to a player, in whichever model is active.
    private func replaceCurrent(with generated: Discussion) {
        if isRegular {
            selection = .discussion(generated)
        } else if let index = path.lastIndex(where: { destinationDiscussionID($0) == generated.id }) {
            path[index] = .discussion(generated)
        } else {
            path.append(.discussion(generated))
        }
    }

    @ViewBuilder
    private func destinationView(_ destination: LibraryDestination) -> some View {
        switch destination {
        case .discussion(let discussion):
            discussionDestination(discussion)
        case .album(let id):
            AlbumView(albumID: id)
        }
    }

    @ViewBuilder
    private func discussionDestination(_ discussion: Discussion) -> some View {
        switch discussion.status {
        case .planning, .failed:
            // New discussions plan conversationally; legacy plans are seeded into
            // the same view from their saved script (see PlanConversationView.start).
            PlanConversationView(discussion: discussion) { generated in
                upsert(generated)
                replaceCurrent(with: generated)
            }
        case .generating, .ready:
            PodcastPlayerView(discussion: discussion, onCreatedFollowUp: { created in
                upsert(created)
                navigate(to: created)
            })
        }
    }
}

/// Home-list row for an album group: the album cover, title, and episode
/// count. Opens the album's episode list.
private struct AlbumGroupRow: View {
    let summary: AlbumSummaryDTO
    let newest: Discussion
    let episodeCount: Int
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 14) {
            AlbumCoverThumbnail(cover: summary.cover, size: 40)
            VStack(alignment: .leading, spacing: 4) {
                Text(summary.title.isEmpty ? newest.displayTitle : summary.title)
                    .font(.headline)
                    .lineLimit(2)
                HStack(spacing: 8) {
                    Label("\(episodeCount) episode\(episodeCount == 1 ? "" : "s")",
                          systemImage: "rectangle.stack")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer()
            Image(systemName: "chevron.right").foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(tint: isSelected ? Theme.accent.opacity(0.55) : nil)
    }
}

private struct LibrarySettingsView: View {
    @Environment(\.dismiss) private var dismiss
    let userName: String?
    let userID: String?
    let canManageSubscription: Bool
    let pointsLabel: String?
    @State private var didCopyUserID = false
    /// Preferred chapters per audiobook generation batch. The server hard-caps
    /// a batch at 5; this only controls how many chapters the checklist
    /// preselects.
    @AppStorage("audiobook.defaultBatchChapters") private var audiobookBatchChapters = 3

    private var displayName: String {
        let trimmed = userName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? String(localized: "User", comment: "Fallback account display name") : trimmed
    }

    private var displayUserID: String {
        let trimmed = userID?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? String(localized: "Unknown", comment: "Fallback user id in settings") : trimmed
    }

    private var avatarInitial: String {
        String(displayName.trimmingCharacters(in: .whitespacesAndNewlines).prefix(1)).uppercased()
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    accountHeader
                }

                Section("Account") {
                    userIDRow
                }

                Section {
                    Stepper(value: $audiobookBatchChapters, in: 1...5) {
                        SettingsRowLabel(title: String(localized: "Max chapters per generation: \(audiobookBatchChapters)"),
                                         systemImage: "text.book.closed")
                    }
                    .accessibilityIdentifier("settings.maxChaptersPerGeneration")
                } header: {
                    Text("Audiobooks")
                } footer: {
                    Text("Long audiobooks generate in batches of up to 5 chapters. Remaining chapters can be generated later from the podcast.")
                }

                if canManageSubscription {
                    Section("Subscription") {
                        if let pointsLabel {
                            NavigationLink {
                                PointsHistoryView(embedsInNavigationStack: false, showsCloseButton: false)
                            } label: {
                                SettingsRowLabel(title: pointsLabel, systemImage: "sparkles")
                            }
                        }

                        NavigationLink {
                            CustomerCenterScreen(showsCloseButton: false)
                                .navigationTitle("Manage Subscription")
                                .navigationBarTitleDisplayMode(.inline)
                        } label: {
                            SettingsRowLabel(title: "Manage Subscription", systemImage: "creditcard")
                        }
                    }
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private var accountHeader: some View {
        HStack(spacing: 16) {
            ZStack {
                Circle()
                    .fill(Theme.accent.opacity(0.16))
                Text(avatarInitial)
                    .font(.system(size: 28, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
            .frame(width: 64, height: 64)

            VStack(alignment: .leading, spacing: 4) {
                Text(displayName)
                    .font(.title3.weight(.semibold))
                    .lineLimit(2)
                    .minimumScaleFactor(0.85)
                Text("Signed in")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }

            Spacer(minLength: 0)
        }
        .padding(.vertical, 8)
    }

    private var userIDRow: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text("User ID")
                    .font(.body)
                Text(displayUserID)
                    .font(.caption.monospaced())
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(2)
                    .textSelection(.enabled)
            }

            Spacer(minLength: 8)

            Button {
                UIPasteboard.general.string = displayUserID
                didCopyUserID = true
            } label: {
                Image(systemName: didCopyUserID ? "checkmark" : "doc.on.doc")
                    .font(.body.weight(.semibold))
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(didCopyUserID ? "Copied User ID" : "Copy User ID")
        }
    }
}

private struct SettingsRowLabel: View {
    let title: String
    let systemImage: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: systemImage)
                .font(.body.weight(.semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 26, height: 26)
            Text(title)
                .foregroundStyle(.primary)
            Spacer()
        }
    }
}

private struct HomeToolbarTipModifier: ViewModifier {
    let itemID: String

    @ViewBuilder
    func body(content: Content) -> some View {
        if itemID == "market" {
            content.popoverTip(OpenMarketTip(), arrowEdge: .top)
        } else {
            content
        }
    }
}

private enum LibraryVisibilityFilter: String, CaseIterable, Identifiable {
    case all
    case `public`
    case `private`

    var id: String { rawValue }

    var apiVisibility: DiscussionVisibility? {
        switch self {
        case .all: return nil
        case .public: return .public
        case .private: return .private
        }
    }

    var title: String {
        switch self {
        case .all:
            return String(localized: "All", comment: "Library visibility filter: all stations")
        case .public:
            return String(localized: "Public", comment: "Library visibility filter: public stations")
        case .private:
            return String(localized: "Private", comment: "Library visibility filter: private stations")
        }
    }

    var icon: String {
        switch self {
        case .all: return "tray.full"
        case .public: return "globe"
        case .private: return "lock.fill"
        }
    }

    var emptyTitle: String {
        switch self {
        case .all:
            return String(localized: "No \(AppStringLiteral.stationsNameRaw) yet")
        case .public:
            return String(localized: "No Public \(AppStringLiteral.stationsNameRaw)")
        case .private:
            return String(localized: "No Private \(AppStringLiteral.stationsNameRaw)")
        }
    }

    var emptyMessage: String {
        switch self {
        case .all:
            return String(localized: "Plan an AI \(AppStringLiteral.stationNameRaw) and generate the audio.")
        case .public:
            return String(localized: "Published \(AppStringLiteral.stationsNameRaw) will appear here.")
        case .private:
            return String(localized: "Private \(AppStringLiteral.stationsNameRaw) stay visible only to you.")
        }
    }
}

private struct DiscussionRow: View {
    let discussion: Discussion
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 14) {
            leading
                .frame(width: 40)
            VStack(alignment: .leading, spacing: 4) {
                Text(discussion.displayTitle)
                    .font(.headline)
                    .lineLimit(2)
                HStack(spacing: 8) {
                    Text(statusLabel)
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                    VisibilityBadge(isPublic: discussion.isPublic)
                }
            }
            Spacer()
            Image(systemName: "chevron.right").foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(tint: isSelected ? Theme.accent.opacity(0.55) : nil)
    }

    /// Cover thumbnail when the discussion has cover art, otherwise the
    /// status icon. Keeps the row compact while surfacing covers in the library.
    @ViewBuilder
    private var leading: some View {
        if let cover = discussion.cover, cover.hasImage || cover.hasGradient {
            coverThumbnail(cover)
                .frame(width: 40, height: 40)
                .clipShape(.rect(cornerRadius: 8))
        } else {
            Image(systemName: icon)
                .font(.title2)
                .foregroundStyle(Theme.accent)
        }
    }

    @ViewBuilder
    private func coverThumbnail(_ cover: DiscussionCover) -> some View {
        if let urlString = cover.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !urlString.isEmpty, let url = URL(string: urlString) {
            AsyncImage(url: url) { phase in
                switch phase {
                case .success(let image):
                    image.resizable().scaledToFill()
                default:
                    coverGradient(cover)
                }
            }
        } else {
            coverGradient(cover)
        }
    }

    private func coverGradient(_ cover: DiscussionCover) -> some View {
        LinearGradient(
            colors: [
                Color(hex: cover.gradientStart ?? "#8E5CF7"),
                Color(hex: cover.gradientEnd ?? "#00A3FF"),
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }

    private var icon: String {
        switch discussion.status {
        case .planning: return "pencil.and.list.clipboard"
        case .generating: return "waveform"
        case .ready: return "play.circle.fill"
        case .failed: return "exclamationmark.triangle"
        }
    }

    private var statusLabel: String {
        switch discussion.status {
        case .planning:
            let peopleCount = discussion.sortedPeople.count
            if peopleCount > 0 {
                return String(localized: "Plan - \(peopleCount) people",
                              comment: "Discussion row status: planning, with the panelist count")
            }
            return String(localized: "Plan", comment: "Discussion row status: planning without loaded plan details")
        case .generating:
            return String(localized: "Generating...", comment: "Discussion row status: podcast is generating")
        case .ready:
            return String(localized: "Ready to play", comment: "Discussion row status: podcast is ready")
        case .failed:
            return String(localized: "Failed", comment: "Discussion row status: generation failed")
        }
    }
}

private struct VisibilityBadge: View {
    let isPublic: Bool

    var body: some View {
        Label(isPublic ? "Public" : "Private", systemImage: isPublic ? "globe" : "lock.fill")
            .font(.caption2.weight(.semibold))
            .foregroundStyle(isPublic ? Theme.accent : Theme.secondaryText)
            .lineLimit(1)
            .fixedSize(horizontal: true, vertical: false)
    }
}
