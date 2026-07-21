import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

/// Home: the user's server-owned discussions, newest first. Podcasts that
/// belong to an album (audiobook chapter batches, follow-ups, manual groups)
/// collapse into one album row that opens the album's episode list.
struct LibraryView: View {
    @Environment(AuthManager.self) var auth
    @Environment(PurchaseManager.self) var purchases
    #if !os(macOS)
    @Environment(\.horizontalSizeClass) var hSize
    #endif
    @State var discussions: [Discussion] = []
    @State var showingNew = false
    @State var showingNewAlbum = false
    @State var showingUploadAudio = false
    @State var showingRecordAudio = false
    @State var showingRecordings = false
    @State var showingPointsHistory = false
    @State var showingSettings = false
    @State var showingWhatsNew = false
    @State var showingMarketplace = false
    @State var selectedTab: HomeTab = .home
    @State var showingGlobalChat = false
    @State var showingGlobalDocuments = false
    @State var path: [LibraryDestination] = []
    /// Navigation stack for the search tab; independent of the library
    /// tab's `path`/`selection` on both size classes.
    @State var searchPath: [LibraryDestination] = []
    /// Detail selection for the iPad split-view layout.
    @State var selection: LibraryDestination?
    @State var isLoading = false
    @State var hasLoadedInitialPage = false
    @State var isLoadingMore = false
    @State var canLoadMore = true
    @State var errorMessage: String?
    @State var loadErrorMessage: String?
    @State var searchText = ""
    @State var loadedSearchQuery = ""
    /// Grouped semantic content matches for the active search; nil while no
    /// semantic search has completed (title-substring fallback / no query).
    @State var semanticGroups: [SemanticSearchGroup]?
    @State var visibilityFilter: LibraryVisibilityFilter = .all
    @State var loadedVisibilityFilter: LibraryVisibilityFilter = .all
    @State var typeFilter: LibraryTypeFilter = .all
    @State var loadedTypeFilter: LibraryTypeFilter = .all
    @State var toolbarItems: [DiscussionUIActionItem] = []
    /// Nil means Chat is globally unavailable. A present but disabled action
    /// means the feature exists but the current subscription does not grant it.
    @State var homeChatAction: DiscussionUIActionItem?
    @State var showingChatUpgradePrompt = false
    @State var showingChatPaywall = false
    @State var isSearchLoading = false
    @State var searchTask: Task<Void, Never>?
    @State var renamingDiscussion: Discussion?
    @State var renamingDiscussionTitle = ""
    @State var renamingAlbum: AlbumSummaryDTO?
    @State var renamingAlbumTitle = ""
    let pageSize = 20

    var isRegular: Bool {
        #if os(macOS)
        true
        #else
        hSize == .regular
        #endif
    }

    var body: some View {
        withLifecycle(withPresentations(
            TabView(selection: $selectedTab) {
                Tab("Home", systemImage: "waveform.circle.fill", value: HomeTab.home) {
                    Group {
                        if isRegular { splitView } else { stackView }
                    }
                }

                if homeChatAction != nil {
                    Tab("Chat", systemImage: "bubble.left.and.text.bubble.right", value: HomeTab.chat) {
                        chatTab
                    }
                }

                Tab(value: HomeTab.search, role: .search) {
                    searchTab
                }
            }
            .onChange(of: selectedTab) { _, newValue in
                if newValue == .chat {
                    if homeChatAction?.enabled == true {
                        showingGlobalChat = true
                    } else {
                        showingGlobalChat = false
                        selectedTab = .home
                        showingChatUpgradePrompt = true
                    }
                }
            }
        ))
    }

    /// Sheets, covers, and alerts hung off the root view. Split out of `body`
    /// (with `withLifecycle`) to keep the expression type-checkable.
    func withPresentations(_ content: some View) -> some View {
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
            .sheet(isPresented: $showingUploadAudio) {
                UploadAudioSheet { discussion in
                    showingUploadAudio = false
                    upsert(discussion)
                    navigate(to: discussion)
                }
            }
            .sheet(isPresented: $showingRecordAudio) {
                RecordAudioSheet { discussion in
                    showingRecordAudio = false
                    upsert(discussion)
                    navigate(to: discussion)
                }
            }
            .sheet(isPresented: $showingRecordings) {
                MyRecordingsView { discussion in
                    showingRecordings = false
                    upsert(discussion)
                    navigate(to: discussion)
                }
            }
            .alert("Could not load \(AppStringLiteral.stationsNameRaw)", isPresented: errorBinding) {
                Button("OK", role: .cancel) { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .alert("Rename Podcast", isPresented: renamingDiscussionBinding) {
                TextField("Podcast name", text: $renamingDiscussionTitle)
                Button("Rename") { renameSelectedDiscussion() }
                Button("Cancel", role: .cancel) { renamingDiscussion = nil }
            }
            .alert("Rename Album", isPresented: renamingAlbumBinding) {
                TextField("Album name", text: $renamingAlbumTitle)
                Button("Rename") { renameSelectedAlbum() }
                Button("Cancel", role: .cancel) { renamingAlbum = nil }
            }
            .sheet(isPresented: $showingPointsHistory) {
                PointsHistoryView()
            }
            .sheet(isPresented: $showingGlobalDocuments) {
                AgentDocumentLibraryView(discussionID: nil,
                                         api: APIClient(tokens: auth))
            }
            .alert("Upgrade Required", isPresented: $showingChatUpgradePrompt) {
                Button("View Plans") { showingChatPaywall = true }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("You need to upgrade your subscription to access Chat.")
            }
            .sheet(isPresented: $showingChatPaywall) {
                PaywallScreen {
                    showingChatPaywall = false
                    Task { await loadHomeToolbar() }
                }
                    .appSheetPresentation()
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

    /// Global chat over every podcast in the library. Selecting Chat pushes the
    /// conversation in this tab's navigation stack; the pushed view hides the
    /// tab bar, so popping it restores the bar with the navigation animation.
    var chatTab: some View {
        NavigationStack {
            Color.clear
                .navigationTitle("Home")
                .navigationDestination(isPresented: $showingGlobalChat) {
                    QAConversationView(scope: .global, allowsClearingMessages: true) { discussionID in
                        showingGlobalChat = false
                        Task {
                            if let discussion = try? await APIClient(tokens: auth).discussion(id: discussionID) {
                                upsert(discussion)
                                navigate(to: discussion)
                            }
                        }
                    }
                    #if !os(macOS)
                    .toolbar(.hidden, for: .tabBar)
                    #endif
                }
                .onChange(of: showingGlobalChat) { _, isPresented in
                    if !isPresented, selectedTab == .chat {
                        selectedTab = .home
                    }
                }
        }
    }

    /// Semantic content search over the whole library, living behind the tab
    /// bar's search button. Results push onto the tab's own stack so they
    /// open in place on both iPhone and iPad.
    var searchTab: some View {
        NavigationStack(path: $searchPath) {
            searchTabContent
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.background.ignoresSafeArea())
                .overlay(alignment: .center) {
                    if isSearchLoading {
                        searchLoadingOverlay
                            .transition(.opacity.combined(with: .scale(scale: 0.96)))
                    }
                }
                .animation(.easeInOut(duration: 0.18), value: isSearchLoading)
                .navigationTitle("Search")
                .navigationBarTitleDisplayMode(.inline)
                .navigationDestination(for: LibraryDestination.self) { destination in
                    searchDestinationView(destination)
                }
        }
        .searchable(text: $searchText,
                    prompt: "Search \(AppStringLiteral.stationsNameRaw)")
    }

    @ViewBuilder
    var searchTabContent: some View {
        if normalizedSearchQuery(searchText).isEmpty {
            searchPromptState
        } else if let groups = semanticGroups, !loadedSearchQuery.isEmpty {
            if groups.isEmpty {
                searchEmptyState
            } else {
                SemanticSearchResultsView(groups: groups) { discussion in
                    searchPath.append(.discussion(discussion))
                }
            }
        } else {
            // Query typed, debounce/fetch in flight — the overlay spinner covers this.
            searchPromptState
        }
    }

    /// Load tasks and change observers hung off the root view.
    func withLifecycle(_ content: some View) -> some View {
        content
            .onChange(of: isRegular) { _, newValue in
                syncNavigation(toRegular: newValue)
            }
            .task { await loadInitialPageIfNeeded() }
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
                    await load(visibility: newValue, type: typeFilter, showsSearchOverlay: hasLoadedInitialPage)
                }
            }
            .onChange(of: typeFilter) { _, newValue in
                searchTask?.cancel()
                Task {
                    await loadHomeToolbar()
                    await load(visibility: visibilityFilter, type: newValue, showsSearchOverlay: hasLoadedInitialPage)
                }
            }
            .onDisappear {
                searchTask?.cancel()
                isSearchLoading = false
            }
    }

    /// iPhone / compact: single-column stack-based navigation.
    var stackView: some View {
        NavigationStack(path: $path) {
            libraryContainer
                .navigationTitle(AppStringLiteral.stationTitle)
                .toolbar { libraryToolbar }
                .navigationDestination(for: LibraryDestination.self) { destination in
                    destinationView(destination)
                }
        }
    }

    /// iPad / regular: sidebar list + detail column.
    var splitView: some View {
        NavigationSplitView {
            libraryContainer
                .navigationTitle(AppStringLiteral.stationTitle)
                .toolbar { libraryToolbar }
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

    var libraryContainer: some View {
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
    var libraryContent: some View {
        if shouldShowInitialLoader {
            initialLibraryLoadingView
        } else if shouldShowLoadError {
            loadErrorState
        } else if discussions.isEmpty && (loadedVisibilityFilter != .all || loadedTypeFilter != .all) {
            filterEmptyState
        } else if discussions.isEmpty {
            emptyState
        } else {
            list
        }
    }

    @ToolbarContentBuilder
    var libraryToolbar: some ToolbarContent {
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

    var leadingToolbarItems: [DiscussionUIActionItem] {
        toolbarItems.filter { $0.placement == "topBarLeading" }
    }

    var trailingToolbarItems: [DiscussionUIActionItem] {
        toolbarItems.filter { $0.placement != "topBarLeading" }
    }

    @ViewBuilder
    func homeToolbarItem(_ item: DiscussionUIActionItem) -> some View {
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
    func homeToolbarMenuLeaf(_ item: DiscussionUIActionItem) -> some View {
        let actionItem = item.children.first ?? item
        if actionItem.isDivider {
            Divider()
        } else {
            Button(role: buttonRole(for: actionItem)) {
                performHomeToolbarAction(actionItem)
            } label: {
                homeToolbarLabel(actionItem)
            }
            .disabled(!actionItem.enabled)
            .accessibilityIdentifier("library.\(actionItem.id)")
        }
    }

    @ViewBuilder
    func homeToolbarIcon(_ item: DiscussionUIActionItem) -> some View {
        Image(systemName: homeToolbarSystemImage(item))
    }

    @ViewBuilder
    func homeToolbarLabel(_ item: DiscussionUIActionItem) -> some View {
        let title = homeToolbarTitle(item)
        if let systemImage = item.systemImage, !systemImage.isEmpty {
            Label(title, systemImage: systemImage)
        } else {
            Text(title)
        }
    }

    func homeToolbarTitle(_ item: DiscussionUIActionItem) -> String {
        item.id == "points" ? pointsMenuLabel : item.title
    }

    func homeToolbarSystemImage(_ item: DiscussionUIActionItem) -> String {
        guard let systemImage = item.systemImage, !systemImage.isEmpty else {
            return "ellipsis"
        }
        return systemImage
    }

    func buttonRole(for item: DiscussionUIActionItem) -> ButtonRole? {
        item.role == "destructive" ? .destructive : nil
    }

    /// Balance label for the user menu, e.g. "Points (Balance 1,200 Points)".
    var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Balance Unknown", comment: "User menu label when the points balance is unknown")
        }
        let unit = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Balance (\(UsageSummary.formatInt(balance)) \(unit))",
                      comment: "User menu points label; first value is the formatted balance, second is the localized unit")
    }

    func loadHomeToolbar() async {
        do {
            let response = try await APIClient(tokens: auth).homeUIActions(
                supportsPoints: purchases.isConfigured,
                visibility: visibilityFilter.rawValue,
                type: typeFilter.rawValue
            )
            toolbarItems = response.toolbars
            homeChatAction = response.items.first(where: { $0.id == "chat" })
            if homeChatAction == nil, selectedTab == .chat {
                showingGlobalChat = false
                selectedTab = .home
            }
        } catch {
            toolbarItems = []
        }
    }

    func performHomeToolbarAction(_ item: DiscussionUIActionItem) {
        guard item.action.type != "none",
              let path = validatedHomeActionPath(item) else { return }
        switch path {
        case ["sheet", "points"]:
            showingPointsHistory = true
        case ["sheet", "documents"]:
            showingGlobalDocuments = true
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
        case ["sheet", "upload-audio"]:
            showingUploadAudio = true
        case ["sheet", "record-audio"]:
            showingRecordAudio = true
        case ["sheet", "recordings"]:
            showingRecordings = true
        case ["filter", "all"]:
            visibilityFilter = .all
        case ["filter", "public"]:
            visibilityFilter = .public
        case ["filter", "private"]:
            visibilityFilter = .private
        case ["type", "all"]:
            typeFilter = .all
        case ["type", "discussion"]:
            typeFilter = .discussion
        case ["type", "audio-book"]:
            typeFilter = .audioBook
        case ["action", "refresh"]:
            Task {
                await load(visibility: visibilityFilter, type: typeFilter)
                await purchases.refreshBalance()
                await loadHomeToolbar()
            }
        case ["action", "sign-out"]:
            Task { await auth.signOut() }
        default:
            break
        }
    }

    func validatedHomeActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "home" else { return nil }
        return url.pathComponents.filter { $0 != "/" }
    }

    var placeholder: some View {
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

    /// Collapses the page's discussions into rows, grouping album members
    /// (matched by the server-attached `album` summary) into one row placed at
    /// the newest member's position.
    var listRows: [LibraryListRow] {
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

    var list: some View {
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
    func listRow(_ row: LibraryListRow) -> some View {
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
                Button {
                    beginRenameDiscussion(d)
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                .tint(Theme.accent)
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
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                Button {
                    beginRenameAlbum(summary)
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                .tint(Theme.accent)
            }
        }
    }

    var errorBinding: Binding<Bool> {
        Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )
    }

    var renamingDiscussionBinding: Binding<Bool> {
        Binding(
            get: { renamingDiscussion != nil },
            set: { if !$0 { renamingDiscussion = nil } }
        )
    }

    var renamingAlbumBinding: Binding<Bool> {
        Binding(
            get: { renamingAlbum != nil },
            set: { if !$0 { renamingAlbum = nil } }
        )
    }

    var shouldShowInitialLoader: Bool {
        discussions.isEmpty && (isLoading || !hasLoadedInitialPage)
    }

    var shouldShowLoadError: Bool {
        discussions.isEmpty && loadErrorMessage != nil
    }

    var initialLibraryLoadingView: some View {
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

}
