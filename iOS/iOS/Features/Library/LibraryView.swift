import Kingfisher
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit

/// Home: the user's server-owned discussions, newest first. Podcasts that
/// belong to an album (audiobook chapter batches, follow-ups, manual groups)
/// collapse into one album row that opens the album's episode list.
struct LibraryView: View {
    @Environment(AuthManager.self) var auth
    @Environment(PurchaseManager.self) var purchases
    @Environment(\.horizontalSizeClass) var hSize
    @State var discussions: [Discussion] = []
    @State var showingNew = false
    @State var showingNewAlbum = false
    @State var showingUploadAudio = false
    @State var showingPointsHistory = false
    @State var showingSettings = false
    @State var showingWhatsNew = false
    @State var showingMarketplace = false
    @State var path: [LibraryDestination] = []
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
    @State var visibilityFilter: LibraryVisibilityFilter = .all
    @State var loadedVisibilityFilter: LibraryVisibilityFilter = .all
    @State var typeFilter: LibraryTypeFilter = .all
    @State var loadedTypeFilter: LibraryTypeFilter = .all
    @State var toolbarItems: [DiscussionUIActionItem] = []
    @State var isSearchLoading = false
    @State var searchTask: Task<Void, Never>?
    @State var renamingDiscussion: Discussion?
    @State var renamingDiscussionTitle = ""
    @State var renamingAlbum: AlbumSummaryDTO?
    @State var renamingAlbumTitle = ""
    let pageSize = 20

    var isRegular: Bool { hSize == .regular }

    var body: some View {
        withLifecycle(withPresentations(
            Group {
                if isRegular { splitView } else { stackView }
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
    func withLifecycle(_ content: some View) -> some View {
        content
            .onChange(of: hSize) { _, newValue in
                syncNavigation(toRegular: newValue == .regular)
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
                .searchable(text: $searchText,
                            placement: .navigationBarDrawer(displayMode: .always),
                            prompt: "Search \(AppStringLiteral.stationsNameRaw)")
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
        } else if discussions.isEmpty && !loadedSearchQuery.isEmpty {
            searchEmptyState
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
                await load(searchQuery: searchText, visibility: visibilityFilter, type: typeFilter)
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
