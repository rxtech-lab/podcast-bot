import Kingfisher
import SwiftUI

struct MarketplaceView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let onCreateFromPlan: (Discussion) -> Void

    @State private var selectedTab: MarketTab = .market
    @State private var marketStations: [Discussion] = []
    @State private var likedStations: [Discussion] = []
    @State private var searchResults: [Discussion] = []
    @State private var marketProfile: MarketProfile?
    @State private var path: [MarketDestination] = []
    @State private var presentedCreator: CreatorProfile?
    @State private var selectedProfileTab: ProfileTab = .stations
    @State private var searchText = ""
    @State private var loadedSearchQuery = ""
    @State private var isLoading = false
    @State private var isLoadingMore = false
    @State private var canLoadMoreMarket = true
    @State private var canLoadMoreLiked = true
    @State private var canLoadMoreSearch = false
    @State private var errorMessage: String?
    @State private var searchTask: Task<Void, Never>?

    private let pageSize = 20

    init(onCreateFromPlan: @escaping (Discussion) -> Void = { _ in }) {
        self.onCreateFromPlan = onCreateFromPlan
    }

    var body: some View {
        TabView(selection: $selectedTab) {
            Tab("Market", systemImage: "music.note.list", value: MarketTab.market) {
                marketNavigationStack {
                    #if os(macOS)
                    marketContent(tab: macOSMarketContentTab,
                                  stations: macOSMarketStations,
                                  emptyTitle: macOSMarketEmptyTitle,
                                  emptyMessage: macOSMarketEmptyMessage)
                    #else
                    marketContent(tab: .market,
                                  stations: marketStations,
                                  emptyTitle: "No Public \(AppStringLiteral.stationsNameRaw)",
                                  emptyMessage: "Public \(AppStringLiteral.stationsNameRaw) will appear here.")
                    #endif
                }
            }

            Tab("Liked", systemImage: "heart.fill", value: MarketTab.liked) {
                marketNavigationStack {
                    marketContent(tab: .liked,
                                  stations: likedStations,
                                  emptyTitle: "No Liked \(AppStringLiteral.stationsNameRaw)",
                                  emptyMessage: "Saved \(AppStringLiteral.stationsNameRaw) appear here.")
                }
            }

            Tab("Profile", systemImage: "person.crop.circle", value: MarketTab.profile) {
                marketNavigationStack {
                    profileContent
                }
            }

            #if !os(macOS)
            Tab(value: MarketTab.search, role: .search) {
                marketNavigationStack(title: "Search") {
                    marketContent(tab: .search,
                                  stations: searchResults,
                                  emptyTitle: searchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Search Market" : "No Results",
                                  emptyMessage: searchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Search public \(AppStringLiteral.stationsNameRaw)." : "No public \(AppStringLiteral.stationsNameRaw) match your search.")
                }
                .searchable(text: $searchText,
                            prompt: "Search public \(AppStringLiteral.stationsNameRaw)")
                .autocorrectionDisabled(true)
                .textInputAutocapitalization(.never)
                .onSubmit(of: .search) {
                    searchTask?.cancel()
                    Task { await load(reset: true) }
                }
            }
            #endif
        }
        #if os(macOS)
        .tabViewStyle(.tabBarOnly)
        .searchable(text: $searchText,
                    prompt: "Search public \(AppStringLiteral.stationsNameRaw)")
        .autocorrectionDisabled(true)
        .textInputAutocapitalization(.never)
        .onSubmit(of: .search) {
            searchTask?.cancel()
            Task { await load(reset: true) }
        }
        .frame(minWidth: 780,
               idealWidth: 900,
               maxWidth: 980,
               minHeight: 600,
               idealHeight: 720,
               maxHeight: 820)
        #endif
        .sheet(item: $presentedCreator, onDismiss: {
            if selectedTab == .profile {
                Task { await load(reset: true) }
            }
        }) { creator in
            CreatorProfileView(creatorID: creator.id,
                               initialProfile: creator,
                               onCreateFromPlan: onCreateFromPlan)
        }
        .task { await load(reset: true) }
        .onChange(of: selectedTab) { _, _ in
            Task { await load(reset: currentStations.isEmpty) }
        }
        .onChange(of: searchText) { _, value in
            #if os(macOS)
            if !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
               selectedTab != .market {
                selectedTab = .market
            }
            #endif
            scheduleSearch(value)
        }
        .onDisappear { searchTask?.cancel() }
        .alert("Could not load market", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    private func marketNavigationStack<Content: View>(
        title: String = "Market",
        @ViewBuilder content: () -> Content
    ) -> some View {
        NavigationStack(path: $path) {
            content()
                #if !os(macOS)
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                #endif
                .toolbar {
                    #if os(macOS)
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                            .keyboardShortcut(.cancelAction)
                    }
                    #else
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { dismiss() } label: { Image(systemName: "xmark") }
                            .accessibilityLabel("Close")
                    }
                    #endif
                }
                .background(Theme.background.ignoresSafeArea())
                .navigationDestination(for: MarketDestination.self) { destination in
                    switch destination {
                    case .discussion(let discussion):
                        PodcastPlayerView(discussion: discussion,
                                          onCreatedFromPlan: onCreateFromPlan,
                                          onCreatedFollowUp: onCreateFromPlan,
                                          hidesTabBar: true)
                    case .album(let id):
                        AlbumView(albumID: id, mode: .publicMarket) { episode in
                            path.append(.discussion(episode))
                        }
                    }
                }
        }
    }

    private var currentStations: [Discussion] {
        switch currentContentTab {
        case .market:
            marketStations
        case .liked:
            likedStations
        case .search:
            searchResults
        case .profile:
            marketProfile?.stations ?? []
        }
    }

    private var currentContentTab: MarketTab {
        #if os(macOS)
        if selectedTab == .market {
            return macOSMarketContentTab
        }
        #endif
        return selectedTab
    }

    #if os(macOS)
    private var trimmedSearchText: String {
        searchText.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var macOSMarketContentTab: MarketTab {
        trimmedSearchText.isEmpty ? .market : .search
    }

    private var macOSMarketStations: [Discussion] {
        macOSMarketContentTab == .search ? searchResults : marketStations
    }

    private var macOSMarketEmptyTitle: String {
        trimmedSearchText.isEmpty ? "No Public \(AppStringLiteral.stationsNameRaw)" : "No Results"
    }

    private var macOSMarketEmptyMessage: String {
        trimmedSearchText.isEmpty
            ? "Public \(AppStringLiteral.stationsNameRaw) will appear here."
            : "No public \(AppStringLiteral.stationsNameRaw) match your search."
    }
    #endif

    @ViewBuilder
    private func marketContent(tab: MarketTab, stations: [Discussion], emptyTitle: String, emptyMessage: String) -> some View {
        let items = marketDisplayItems(from: stations)
        let gridItems = gridItems(for: tab, items: items)
        Group {
            if isLoading && stations.isEmpty {
                ProgressView()
                    .tint(Theme.accent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if stations.isEmpty {
                ContentUnavailableView(emptyTitle,
                                       systemImage: "square.grid.2x2",
                                       description: Text(emptyMessage))
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 22) {
                        if tab == .market, let featured = items.first {
                            MarketFeaturedItem(
                                item: featured,
                                onOpen: openMarketItem,
                                onToggleLike: toggleLike
                            )
                            .padding(.horizontal, 16)
                            .padding(.top, 12)
                        }

                        if tab == .market {
                            marketShelf(title: "On Air",
                                        items: marketDisplayItems(from: stations.filter { $0.status == .generating }))
                        }

                        if !gridItems.isEmpty {
                            VStack(alignment: .leading, spacing: 12) {
                                Text(sectionTitle(for: tab))
                                    .font(.headline)
                                    .padding(.horizontal, 16)
                                LazyVGrid(columns: marketGridColumns, spacing: 18) {
                                    ForEach(gridItems) { item in
                                        MarketItemCard(
                                            item: item,
                                            onOpen: openMarketItem,
                                            onToggleLike: toggleLike
                                        )
                                        #if !os(macOS)
                                        .onAppear {
                                            if item.id == gridItems.last?.id {
                                                Task { await loadMore(for: tab) }
                                            }
                                        }
                                        #endif
                                    }
                                }
                                .padding(.horizontal, 16)
                            }
                        }

                        #if os(macOS)
                        if canLoadMore(for: tab) {
                            Color.clear
                                .frame(height: 1)
                                .id(stations.count)
                                .onAppear {
                                    Task { await loadMore(for: tab) }
                                }
                        }
                        #endif

                        if isLoadingMore {
                            HStack {
                                Spacer()
                                ProgressView().tint(Theme.accent)
                                Spacer()
                            }
                            .padding(.vertical, 18)
                        }
                    }
                    .padding(.bottom, 28)
                }
                .refreshable { await load(reset: true) }
                .scrollDismissesKeyboard(.interactively)
            }
        }
    }

    private func sectionTitle(for tab: MarketTab) -> String {
        switch tab {
        case .market:
            "Browse"
        case .liked:
            "Liked"
        case .search:
            "Results"
        case .profile:
            "My \(AppStringLiteral.stationsNameRaw)"
        }
    }

    private var marketGridColumns: [GridItem] {
        #if os(macOS)
        [GridItem(.adaptive(minimum: 180, maximum: 220), spacing: 18, alignment: .top)]
        #else
        [GridItem(.adaptive(minimum: 150), spacing: 14)]
        #endif
    }

    private func gridItems(for tab: MarketTab, items: [MarketDisplayItem]) -> [MarketDisplayItem] {
        #if os(macOS)
        tab == .market ? Array(items.dropFirst()) : items
        #else
        items
        #endif
    }

    @ViewBuilder
    private var profileContent: some View {
        if isLoading && marketProfile == nil {
            ProgressView()
                .tint(Theme.accent)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let profile = marketProfile {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 24) {
                    MarketProfileHeader(profile: profile.profile)
                        .padding(.horizontal, 16)
                        .padding(.top, 14)

                    Picker("Profile section", selection: $selectedProfileTab) {
                        Text("My \(AppStringLiteral.stationsNameRaw)").tag(ProfileTab.stations)
                        Text("Followed").tag(ProfileTab.followed)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .padding(.horizontal, 16)

                    switch selectedProfileTab {
                    case .stations:
                        profileStationsContent(profile.stations)
                    case .followed:
                        profileFollowingContent(profile.following)
                    }
                }
                .padding(.bottom, 28)
            }
            .refreshable { await load(reset: true) }
        } else {
            ContentUnavailableView("Profile Unavailable",
                                   systemImage: "person.crop.circle.badge.exclamationmark",
                                   description: Text("Refresh the market to load your profile."))
        }
    }

    @ViewBuilder
    private func profileStationsContent(_ stations: [Discussion]) -> some View {
        if stations.isEmpty {
            ContentUnavailableView("No Public \(AppStringLiteral.stationsNameRaw)",
                                   systemImage: "music.note.list",
                                   description: Text("Public \(AppStringLiteral.stationsNameRaw) you created appear here."))
                .padding(.vertical, 28)
        } else {
            let items = marketDisplayItems(from: stations)
            LazyVGrid(columns: marketGridColumns, spacing: 18) {
                ForEach(items) { item in
                    MarketItemCard(
                        item: item,
                        onOpen: openMarketItem,
                        onToggleLike: toggleLike
                    )
                }
            }
            .padding(.horizontal, 16)
        }
    }

    @ViewBuilder
    private func profileFollowingContent(_ creators: [CreatorProfile]) -> some View {
        if creators.isEmpty {
            ContentUnavailableView("No Followed Creators",
                                   systemImage: "person.2",
                                   description: Text("Creators you follow appear here."))
                .padding(.vertical, 18)
        } else {
            LazyVStack(spacing: 10) {
                ForEach(creators) { creator in
                    Button { presentedCreator = creator } label: {
                        CreatorProfileRow(profile: creator)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
        }
    }

    private func marketShelf(title: String, items: [MarketDisplayItem]) -> some View {
        Group {
            if !items.isEmpty {
                VStack(alignment: .leading, spacing: 12) {
                    Text(title)
                        .font(.headline)
                        .padding(.horizontal, 16)
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 14) {
                            ForEach(items) { item in
                                MarketShelfItem(
                                    item: item,
                                    onOpen: openMarketItem,
                                    onToggleLike: toggleLike
                                )
                            }
                        }
                        .padding(.horizontal, 16)
                    }
                }
            }
        }
    }

    private func openMarketItem(_ item: MarketDisplayItem) {
        path.append(item.destination)
    }

    @MainActor
    private func load(reset: Bool) async {
        let tab = currentContentTab
        let query = tab == .search ? searchText.trimmingCharacters(in: .whitespacesAndNewlines) : ""
        if tab == .search, query.isEmpty {
            searchResults = []
            canLoadMoreSearch = false
            loadedSearchQuery = ""
            return
        }
        if reset {
            if tab == .market {
                canLoadMoreMarket = true
            } else if tab == .liked {
                canLoadMoreLiked = true
            } else if tab == .search {
                canLoadMoreSearch = true
            }
        }
        guard tab == .profile || reset || currentCanLoadMore else { return }
        isLoading = reset
        defer {
            isLoading = false
            loadedSearchQuery = query
        }
        do {
            let api = APIClient(tokens: auth)
            if tab == .market {
                let items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : marketStations.count, query: query)
                let merged = preservingKnownCovers(in: items)
                if reset { marketStations = merged } else { append(merged, to: .market) }
                canLoadMoreMarket = items.count == pageSize
            } else if tab == .liked {
                let items = try await api.likedMarketStations(limit: pageSize, offset: reset ? 0 : likedStations.count, query: query)
                let merged = preservingKnownCovers(in: items)
                if reset { likedStations = merged } else { append(merged, to: .liked) }
                canLoadMoreLiked = items.count == pageSize
            } else if tab == .profile {
                var profile = try await api.marketProfile()
                profile.stations = preservingKnownCovers(in: profile.stations)
                marketProfile = profile
            } else {
                let items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : searchResults.count, query: query)
                let merged = preservingKnownCovers(in: items)
                if reset { searchResults = merged } else { append(merged, to: .search) }
                canLoadMoreSearch = items.count == pageSize
            }
        } catch {
            report(error)
        }
    }

    @MainActor
    private func loadMore(for tab: MarketTab) async {
        guard currentContentTab == tab else { return }
        guard !isLoadingMore, currentCanLoadMore else { return }
        isLoadingMore = true
        defer { isLoadingMore = false }
        await load(reset: false)
    }

    private var currentCanLoadMore: Bool {
        canLoadMore(for: currentContentTab)
    }

    private func canLoadMore(for tab: MarketTab) -> Bool {
        switch tab {
        case .market:
            canLoadMoreMarket
        case .liked:
            canLoadMoreLiked
        case .search:
            canLoadMoreSearch
        case .profile:
            false
        }
    }

    @MainActor
    private func append(_ items: [Discussion], to tab: MarketTab) {
        if tab == .market {
            let existing = Set(marketStations.map(\.id))
            marketStations.append(contentsOf: items.filter { !existing.contains($0.id) })
        } else if tab == .liked {
            let existing = Set(likedStations.map(\.id))
            likedStations.append(contentsOf: items.filter { !existing.contains($0.id) })
        } else {
            let existing = Set(searchResults.map(\.id))
            searchResults.append(contentsOf: items.filter { !existing.contains($0.id) })
        }
    }

    private func scheduleSearch(_ value: String) {
        #if os(macOS)
        guard selectedTab == .market else { return }
        #else
        guard selectedTab == .search else { return }
        #endif
        searchTask?.cancel()
        searchTask = Task {
            try? await Task.sleep(for: .milliseconds(350))
            guard !Task.isCancelled else { return }
            if value.trimmingCharacters(in: .whitespacesAndNewlines) != loadedSearchQuery {
                await load(reset: true)
            }
        }
    }

    private func toggleLike(_ station: Discussion) {
        Task { @MainActor in
            do {
                let api = APIClient(tokens: auth)
                let updated = (station.isLiked == true)
                    ? try await api.unlikeMarketStation(id: station.id)
                    : try await api.likeMarketStation(id: station.id)
                upsert(updated.preservingRenderableCover(from: station))
            } catch {
                report(error)
            }
        }
    }

    @MainActor
    private func upsert(_ station: Discussion) {
        let merged = preservingKnownCover(for: station)
        replace(merged, in: &marketStations)
        replace(merged, in: &searchResults)
        if merged.isLiked == true {
            replace(merged, in: &likedStations)
            if !likedStations.contains(where: { $0.id == station.id }) {
                likedStations.insert(merged, at: 0)
            }
        } else {
            likedStations.removeAll { $0.id == merged.id }
        }
        if var profile = marketProfile {
            replace(merged, in: &profile.stations)
            marketProfile = profile
        }
    }

    private func replace(_ station: Discussion, in list: inout [Discussion]) {
        if let index = list.firstIndex(where: { $0.id == station.id }) {
            list[index] = station
        }
    }

    private func preservingKnownCovers(in items: [Discussion]) -> [Discussion] {
        items.map(preservingKnownCover(for:))
    }

    private func preservingKnownCover(for station: Discussion) -> Discussion {
        for existing in knownStations where existing.id == station.id {
            return station.preservingRenderableCover(from: existing)
        }
        return station
    }

    private var knownStations: [Discussion] {
        marketStations + likedStations + searchResults + (marketProfile?.stations ?? [])
    }

    @MainActor
    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }
}
