import SwiftUI

struct MarketplaceView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let onCreateFromPlan: (Discussion) -> Void

    @State private var selectedTab: MarketTab = .market
    @State private var marketStations: [Discussion] = []
    @State private var likedStations: [Discussion] = []
    @State private var searchResults: [Discussion] = []
    @State private var path: [Discussion] = []
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
        NavigationStack(path: $path) {
            TabView(selection: $selectedTab) {
                Tab("Market", systemImage: "music.note.list", value: MarketTab.market) {
                    marketContent(tab: .market,
                                  stations: marketStations,
                                  emptyTitle: "No Public \(AppStringLiteral.stationsNameRaw)",
                                  emptyMessage: "Public \(AppStringLiteral.stationsNameRaw) will appear here.")
                }

                Tab("Liked", systemImage: "heart.fill", value: MarketTab.liked) {
                    marketContent(tab: .liked,
                                  stations: likedStations,
                                  emptyTitle: "No Liked \(AppStringLiteral.stationsNameRaw)",
                                  emptyMessage: "Saved \(AppStringLiteral.stationsNameRaw) appear here.")
                }

                Tab(value: MarketTab.search, role: .search) {
                    marketContent(tab: .search,
                                  stations: searchResults,
                                  emptyTitle: searchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Search Market" : "No Results",
                                  emptyMessage: searchText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "Search public \(AppStringLiteral.stationsNameRaw)." : "No public \(AppStringLiteral.stationsNameRaw) match your search.")
                        .searchable(text: $searchText,
                                    prompt: "Search public \(AppStringLiteral.stationsNameRaw)")
                        .autocorrectionDisabled(true)
                        .textInputAutocapitalization(.never)
                        .onSubmit(of: .search) {
                            searchTask?.cancel()
                            Task { await load(reset: true) }
                        }
                }
            }
            .navigationTitle("Market")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                }
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationDestination(for: Discussion.self) { discussion in
                PodcastPlayerView(discussion: discussion, onCreatedFromPlan: onCreateFromPlan)
            }
        }
        .task { await load(reset: true) }
        .onChange(of: selectedTab) { _, _ in
            Task { await load(reset: currentStations.isEmpty) }
        }
        .onChange(of: searchText) { _, value in scheduleSearch(value) }
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

    private var currentStations: [Discussion] {
        switch selectedTab {
        case .market:
            marketStations
        case .liked:
            likedStations
        case .search:
            searchResults
        }
    }

    private func marketContent(tab: MarketTab, stations: [Discussion], emptyTitle: String, emptyMessage: String) -> some View {
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
                        if tab == .market, let featured = stations.first {
                            MarketFeaturedStation(
                                discussion: featured,
                                onOpen: { path.append(featured) },
                                onToggleLike: { toggleLike(featured) }
                            )
                            .padding(.horizontal, 16)
                            .padding(.top, 12)
                        }

                        if tab == .market {
                            marketShelf(title: "On Air",
                                        stations: stations.filter { $0.status == .generating })
                        }

                        VStack(alignment: .leading, spacing: 12) {
                            Text(sectionTitle(for: tab))
                                .font(.headline)
                                .padding(.horizontal, 16)
                            LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 14)], spacing: 18) {
                                ForEach(stations) { station in
                                    MarketStationCard(
                                        discussion: station,
                                        onOpen: { path.append(station) },
                                        onToggleLike: { toggleLike(station) }
                                    )
                                    .onAppear {
                                        if station.id == stations.last?.id {
                                            Task { await loadMore(for: tab) }
                                        }
                                    }
                                }
                            }
                            .padding(.horizontal, 16)
                        }

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
        }
    }

    private func marketShelf(title: String, stations: [Discussion]) -> some View {
        Group {
            if !stations.isEmpty {
                VStack(alignment: .leading, spacing: 12) {
                    Text(title)
                        .font(.headline)
                        .padding(.horizontal, 16)
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 14) {
                            ForEach(stations) { station in
                                MarketStationShelfItem(
                                    discussion: station,
                                    onOpen: { path.append(station) },
                                    onToggleLike: { toggleLike(station) }
                                )
                            }
                        }
                        .padding(.horizontal, 16)
                    }
                }
            }
        }
    }

    @MainActor
    private func load(reset: Bool) async {
        let query = selectedTab == .search ? searchText.trimmingCharacters(in: .whitespacesAndNewlines) : ""
        if selectedTab == .search, query.isEmpty {
            searchResults = []
            canLoadMoreSearch = false
            loadedSearchQuery = ""
            return
        }
        if reset {
            if selectedTab == .market {
                canLoadMoreMarket = true
            } else if selectedTab == .liked {
                canLoadMoreLiked = true
            } else {
                canLoadMoreSearch = true
            }
        }
        guard reset || currentCanLoadMore else { return }
        isLoading = reset
        defer {
            isLoading = false
            loadedSearchQuery = query
        }
        do {
            let api = APIClient(tokens: auth)
            let items: [Discussion]
            if selectedTab == .market {
                items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : marketStations.count, query: query)
                if reset { marketStations = items } else { append(items, to: .market) }
                canLoadMoreMarket = items.count == pageSize
            } else if selectedTab == .liked {
                items = try await api.likedMarketStations(limit: pageSize, offset: reset ? 0 : likedStations.count, query: query)
                if reset { likedStations = items } else { append(items, to: .liked) }
                canLoadMoreLiked = items.count == pageSize
            } else {
                items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : searchResults.count, query: query)
                if reset { searchResults = items } else { append(items, to: .search) }
                canLoadMoreSearch = items.count == pageSize
            }
        } catch {
            report(error)
        }
    }

    @MainActor
    private func loadMore(for tab: MarketTab) async {
        guard selectedTab == tab else { return }
        guard !isLoadingMore, currentCanLoadMore else { return }
        isLoadingMore = true
        defer { isLoadingMore = false }
        await load(reset: false)
    }

    private var currentCanLoadMore: Bool {
        switch selectedTab {
        case .market:
            canLoadMoreMarket
        case .liked:
            canLoadMoreLiked
        case .search:
            canLoadMoreSearch
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
        guard selectedTab == .search else { return }
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
                upsert(updated)
            } catch {
                report(error)
            }
        }
    }

    @MainActor
    private func upsert(_ station: Discussion) {
        replace(station, in: &marketStations)
        replace(station, in: &searchResults)
        if station.isLiked == true {
            replace(station, in: &likedStations)
            if !likedStations.contains(where: { $0.id == station.id }) {
                likedStations.insert(station, at: 0)
            }
        } else {
            likedStations.removeAll { $0.id == station.id }
        }
    }

    private func replace(_ station: Discussion, in list: inout [Discussion]) {
        if let index = list.firstIndex(where: { $0.id == station.id }) {
            list[index] = station
        }
    }

    @MainActor
    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
    }
}

private enum MarketTab {
    case market
    case liked
    case search
}

private struct MarketFeaturedStation: View {
    let discussion: Discussion
    let onOpen: () -> Void
    let onToggleLike: () -> Void

    var body: some View {
        Button(action: onOpen) {
            HStack(spacing: 16) {
                StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                    .frame(width: 118, height: 118)
                VStack(alignment: .leading, spacing: 8) {
                    Text(discussion.displayTitle)
                        .font(.title3.weight(.semibold))
                        .lineLimit(2)
                    Text(discussion.topic)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(2)
                    MarketStatusLabel(discussion: discussion)
                    HStack {
                        Label("\(discussion.likeCount ?? 0)", systemImage: "heart")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(Theme.secondaryText)
                        Spacer()
                        Button(action: onToggleLike) {
                            Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                        }
                        .buttonStyle(.borderless)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .glassCard()
        }
        .buttonStyle(.plain)
    }
}

private struct MarketStationCard: View {
    let discussion: Discussion
    let onOpen: () -> Void
    let onToggleLike: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button(action: onOpen) {
                StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                    .aspectRatio(1, contentMode: .fit)
            }
            .buttonStyle(.plain)
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(discussion.displayTitle)
                        .font(.subheadline.weight(.semibold))
                        .lineLimit(2)
                    MarketStatusLabel(discussion: discussion)
                }
                Spacer(minLength: 8)
                Button(action: onToggleLike) {
                    Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                }
                .buttonStyle(.borderless)
            }
        }
    }
}

private struct MarketStationShelfItem: View {
    let discussion: Discussion
    let onOpen: () -> Void
    let onToggleLike: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button(action: onOpen) {
                StationCoverArt(cover: discussion.cover, title: discussion.displayTitle)
                    .frame(width: 136, height: 136)
            }
            .buttonStyle(.plain)
            HStack {
                Text(discussion.displayTitle)
                    .font(.caption.weight(.semibold))
                    .lineLimit(2)
                Spacer()
                Button(action: onToggleLike) {
                    Image(systemName: discussion.isLiked == true ? "heart.fill" : "heart")
                        .font(.caption)
                }
                .buttonStyle(.borderless)
            }
            .frame(width: 136)
        }
    }
}

private struct MarketStatusLabel: View {
    let discussion: Discussion

    var body: some View {
        Label(title, systemImage: icon)
            .font(.caption.weight(.semibold))
            .foregroundStyle(discussion.status == .generating ? .green : Theme.secondaryText)
    }

    private var title: String {
        switch discussion.status {
        case .generating: return "Live"
        case .ready: return "Ready"
        case .planning: return "Planning"
        case .failed: return "Failed"
        }
    }

    private var icon: String {
        discussion.status == .generating ? "dot.radiowaves.left.and.right" : "play.circle"
    }
}

struct StationCoverArt: View {
    let cover: DiscussionCover?
    let title: String

    var body: some View {
        ZStack {
            if let urlString = cover?.imageURL,
               let url = URL(string: urlString) {
                AsyncImage(url: url) { phase in
                    switch phase {
                    case .success(let image):
                        image
                            .resizable()
                            .scaledToFill()
                    default:
                        gradient
                    }
                }
            } else {
                gradient
            }
            VStack {
                Spacer()
                Text(title)
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.white)
                    .lineLimit(3)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(.black.opacity(0.22))
            }
        }
        .clipShape(.rect(cornerRadius: 8))
        .contentShape(.rect(cornerRadius: 8))
    }

    private var gradient: some View {
        LinearGradient(
            colors: [
                Color(hex: cover?.gradientStart ?? "#8E5CF7"),
                Color(hex: cover?.gradientEnd ?? "#00A3FF"),
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }
}

extension Color {
    init(hex: String) {
        let clean = hex.trimmingCharacters(in: CharacterSet(charactersIn: "#")).uppercased()
        var value: UInt64 = 0
        Scanner(string: clean).scanHexInt64(&value)
        let red = Double((value >> 16) & 0xFF) / 255.0
        let green = Double((value >> 8) & 0xFF) / 255.0
        let blue = Double(value & 0xFF) / 255.0
        self.init(red: red, green: green, blue: blue)
    }
}
