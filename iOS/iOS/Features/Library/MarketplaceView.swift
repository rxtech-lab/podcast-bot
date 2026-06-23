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
    @State private var path: [Discussion] = []
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

                Tab("Profile", systemImage: "person.crop.circle", value: MarketTab.profile) {
                    profileContent
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
        case .profile:
            marketProfile?.stations ?? []
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
        case .profile:
            "My \(AppStringLiteral.stationsNameRaw)"
        }
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
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 14)], spacing: 18) {
                ForEach(stations) { station in
                    MarketStationCard(
                        discussion: station,
                        onOpen: { path.append(station) },
                        onToggleLike: { toggleLike(station) }
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
            } else if selectedTab == .search {
                canLoadMoreSearch = true
            }
        }
        guard selectedTab == .profile || reset || currentCanLoadMore else { return }
        isLoading = reset
        defer {
            isLoading = false
            loadedSearchQuery = query
        }
        do {
            let api = APIClient(tokens: auth)
            if selectedTab == .market {
                let items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : marketStations.count, query: query)
                if reset { marketStations = items } else { append(items, to: .market) }
                canLoadMoreMarket = items.count == pageSize
            } else if selectedTab == .liked {
                let items = try await api.likedMarketStations(limit: pageSize, offset: reset ? 0 : likedStations.count, query: query)
                if reset { likedStations = items } else { append(items, to: .liked) }
                canLoadMoreLiked = items.count == pageSize
            } else if selectedTab == .profile {
                marketProfile = try await api.marketProfile()
            } else {
                let items = try await api.marketStations(limit: pageSize, offset: reset ? 0 : searchResults.count, query: query)
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
        if var profile = marketProfile {
            replace(station, in: &profile.stations)
            marketProfile = profile
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
    case profile
}

private enum ProfileTab {
    case stations
    case followed
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

private struct MarketProfileHeader: View {
    let profile: CreatorProfile

    var body: some View {
        HStack(spacing: 16) {
            CreatorAvatar(profile: profile, size: 74)
            VStack(alignment: .leading, spacing: 6) {
                Text(profile.title)
                    .font(.title2.weight(.semibold))
                    .lineLimit(2)
                if !profile.subtitle.isEmpty {
                    Text(profile.subtitle)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard()
    }
}

private struct CreatorProfileRow: View {
    let profile: CreatorProfile

    var body: some View {
        HStack(spacing: 12) {
            CreatorAvatar(profile: profile, size: 46)
            VStack(alignment: .leading, spacing: 3) {
                Text(profile.title)
                    .font(.subheadline.weight(.semibold))
                    .lineLimit(1)
                Text(profile.followerText)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(1)
            }
            Spacer()
            Image(systemName: "chevron.right")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
        }
        .padding(12)
        .background(.thinMaterial, in: .rect(cornerRadius: 8))
    }
}

private struct CreatorAvatar: View {
    let profile: CreatorProfile
    let size: CGFloat

    var body: some View {
        ZStack {
            Circle()
                .fill(Theme.accent.opacity(0.18))
            if let avatar = profile.avatarURL,
               let url = URL(string: avatar) {
                AsyncImage(url: url) { phase in
                    switch phase {
                    case .success(let image):
                        image.resizable().scaledToFill()
                    default:
                        Image(systemName: "person.fill")
                            .font(.system(size: size * 0.42, weight: .semibold))
                            .foregroundStyle(Theme.accent)
                    }
                }
            } else {
                Image(systemName: "person.fill")
                    .font(.system(size: size * 0.42, weight: .semibold))
                    .foregroundStyle(Theme.accent)
            }
        }
        .frame(width: size, height: size)
        .clipShape(Circle())
    }
}

struct CreatorProfileView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    let creatorID: String
    let initialProfile: CreatorProfile?
    let onCreateFromPlan: ((Discussion) -> Void)?

    @State private var profile: CreatorProfile?
    @State private var stations: [Discussion] = []
    @State private var path: [Discussion] = []
    @State private var isLoading = false
    @State private var isTogglingFollow = false
    @State private var errorMessage: String?

    private let pageSize = 20

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                Theme.background.ignoresSafeArea()
                content
            }
            .navigationTitle(profile?.title ?? "Creator")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                }
            }
            .navigationDestination(for: Discussion.self) { discussion in
                PodcastPlayerView(discussion: discussion, onCreatedFromPlan: onCreateFromPlan)
            }
        }
        .task { await load() }
        .alert("Could not load creator", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading && profile == nil {
            ProgressView()
                .tint(Theme.accent)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let profile {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 22) {
                    creatorHeader(profile)
                        .padding(.horizontal, 16)
                        .padding(.top, 14)
                    creatorStationsContent
                }
                .padding(.bottom, 28)
            }
            .refreshable { await load() }
        } else {
            ContentUnavailableView("Creator Not Found",
                                   systemImage: "person.crop.circle.badge.questionmark",
                                   description: Text("This creator profile is not available."))
        }
    }

    @ViewBuilder
    private var creatorStationsContent: some View {
        if isLoading && stations.isEmpty {
            ProgressView()
                .tint(Theme.accent)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 44)
        } else if stations.isEmpty {
            ContentUnavailableView("No Public \(AppStringLiteral.stationsNameRaw)",
                                   systemImage: "music.note.list",
                                   description: Text("Public \(AppStringLiteral.stationsNameRaw) from this creator appear here."))
                .padding(.vertical, 28)
        } else {
            VStack(alignment: .leading, spacing: 12) {
                Text("Public \(AppStringLiteral.stationsNameRaw)")
                    .font(.headline)
                    .padding(.horizontal, 16)
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 150), spacing: 14)], spacing: 18) {
                    ForEach(stations) { station in
                        MarketStationCard(
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

    private func creatorHeader(_ profile: CreatorProfile) -> some View {
        HStack(spacing: 16) {
            CreatorAvatar(profile: profile, size: 78)
            VStack(alignment: .leading, spacing: 6) {
                Text(profile.title)
                    .font(.title2.weight(.semibold))
                    .lineLimit(2)
                Text(profile.followerText)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer(minLength: 0)
            if profile.isSelf != true {
                Button {
                    toggleFollow()
                } label: {
                    Text(profile.isFollowed == true ? "Following" : "Follow")
                        .font(.subheadline.weight(.semibold))
                }
                .buttonStyle(.borderedProminent)
                .disabled(isTogglingFollow)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard()
    }

    @MainActor
    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let api = APIClient(tokens: auth)
            async let loadedProfile = api.creatorProfile(id: creatorID)
            async let loadedStations = api.creatorStations(id: creatorID, limit: pageSize)
            profile = try await loadedProfile
            stations = try await loadedStations
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            profile = initialProfile
            report(error)
        }
    }

    private func toggleFollow() {
        guard let current = profile, current.isSelf != true else { return }
        isTogglingFollow = true
        Task { @MainActor in
            defer { isTogglingFollow = false }
            do {
                let api = APIClient(tokens: auth)
                profile = current.isFollowed == true
                    ? try await api.unfollowCreator(id: current.id)
                    : try await api.followCreator(id: current.id)
            } catch {
                report(error)
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
                if let index = stations.firstIndex(where: { $0.id == updated.id }) {
                    stations[index] = updated
                }
            } catch {
                report(error)
            }
        }
    }

    @MainActor
    private func report(_ error: Error) {
        guard !APIClient.isCancellation(error) else { return }
        errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
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
            if showsTitleOverlay {
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
        }
        .clipShape(.rect(cornerRadius: 8))
        .contentShape(.rect(cornerRadius: 8))
    }

    private var showsTitleOverlay: Bool {
        cover?.hasImage != true
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
