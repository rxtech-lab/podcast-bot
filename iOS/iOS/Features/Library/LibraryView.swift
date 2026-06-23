import SwiftUI

/// Home: the user's server-owned discussions, newest first.
struct LibraryView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(\.horizontalSizeClass) private var hSize
    @State private var discussions: [Discussion] = []
    @State private var showingNew = false
    @State private var showingCustomerCenter = false
    @State private var showingPointsHistory = false
    @State private var showingWhatsNew = false
    @State private var path: [Discussion] = []
    /// Detail selection for the iPad split-view layout.
    @State private var selection: Discussion?
    @State private var isLoading = false
    @State private var hasLoadedInitialPage = false
    @State private var isLoadingMore = false
    @State private var canLoadMore = true
    @State private var errorMessage: String?
    @State private var searchText = ""
    @State private var loadedSearchQuery = ""
    @State private var isSearchLoading = false
    @State private var searchTask: Task<Void, Never>?
    /// Plan requests for freshly-created discussions, keyed by id, so the plan
    /// page knows to auto-stream the plan once the user navigates to it.
    @State private var pendingPlans: [String: PlanRequest] = [:]

    private let pageSize = 20

    private var isRegular: Bool { hSize == .regular }

    var body: some View {
        Group {
            if isRegular { splitView } else { stackView }
        }
        .onChange(of: hSize) { _, newValue in
            syncNavigation(toRegular: newValue == .regular)
        }
        .sheet(isPresented: $showingNew) {
            NewDiscussionView { discussion, request in
                showingNew = false
                pendingPlans[discussion.id] = request
                upsert(discussion)
                navigate(to: discussion)
            }
        }
        .alert("Could not load discussions", isPresented: errorBinding) {
            Button("OK", role: .cancel) { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .sheet(isPresented: $showingCustomerCenter) {
            CustomerCenterScreen()
        }
        .sheet(isPresented: $showingPointsHistory) {
            PointsHistoryView()
        }
        .sheet(isPresented: $showingWhatsNew) {
            WhatsNewSheet(features: WhatsNewFeature.all,
                          allowsInteractiveDismiss: true) {
                showingWhatsNew = false
            }
        }
        .task { await load() }
        .task { await purchases.refreshBalance() }
        .onChange(of: searchText) { _, newValue in
            scheduleSearch(for: newValue)
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
                .navigationTitle("Discussions")
                .toolbar { libraryToolbar }
                .searchable(text: $searchText,
                            placement: .navigationBarDrawer(displayMode: .always),
                            prompt: "Search discussions")
                .navigationDestination(for: Discussion.self) { discussion in
                    destination(for: discussion)
                }
        }
    }

    /// iPad / regular: sidebar list + detail column.
    private var splitView: some View {
        NavigationSplitView {
            libraryContainer
                .navigationTitle("Discussions")
                .toolbar { libraryToolbar }
                .searchable(text: $searchText,
                            placement: .navigationBarDrawer(displayMode: .always),
                            prompt: "Search discussions")
        } detail: {
            NavigationStack {
                if let selection {
                    destination(for: selection)
                        .id(selection.id)
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
        } else if discussions.isEmpty {
            emptyState
        } else {
            list
        }
    }

    @ToolbarContentBuilder
    private var libraryToolbar: some ToolbarContent {
        DefaultToolbarItem(kind: .search, placement: .bottomBar)
        ToolbarItem(placement: .topBarTrailing) {
            Button { showingNew = true } label: { Image(systemName: "plus") }
        }
        ToolbarItem(placement: .topBarLeading) {
            Menu {
                if purchases.isConfigured {
                    Button(pointsMenuLabel) { showingPointsHistory = true }
                    Button("Manage Subscription") { showingCustomerCenter = true }
                    Divider()
                }
                Button("What's New") { showingWhatsNew = true }
                Button("Refresh") {
                    Task {
                        await load(searchQuery: searchText)
                        await purchases.refreshBalance()
                    }
                }
                Button("Sign Out", role: .destructive) { Task { await auth.signOut() } }
            } label: { Image(systemName: "person.crop.circle") }
        }
    }

    /// Balance label for the user menu, e.g. "Points (Balance 1,200 Points)".
    private var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Points", comment: "User menu label when the points balance is unknown")
        }
        let unit = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Points (\(UsageSummary.formatInt(balance)) \(unit))",
                      comment: "User menu points label; first value is the formatted balance, second is the localized unit")
    }

    private var placeholder: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ContentUnavailableView(
                "Select a discussion",
                systemImage: "waveform.circle",
                description: Text("Pick a discussion from the list, or create a new one.")
            )
        }
    }

    private var list: some View {
        List {
            ForEach(discussions) { d in
                Button {
                    navigate(to: d)
                } label: {
                    DiscussionRow(discussion: d, isSelected: isRegular && selection?.id == d.id)
                }
                .buttonStyle(.plain)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .listRowInsets(.init(top: 6, leading: 16, bottom: 6, trailing: 16))
                .onAppear {
                    if d.id == discussions.last?.id {
                        Task { await loadMore() }
                    }
                }
            }
            .onDelete(perform: deleteDiscussions)

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
                Text("Loading discussions...")
                    .font(.headline)
                Text("Syncing your library")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            ProgressView()
                .tint(Theme.accent)
                .controlSize(.small)
        }
        .multilineTextAlignment(.center)
        .glassCard(cornerRadius: 20)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Loading discussions")
    }

    private func load(searchQuery: String? = nil, showsSearchOverlay: Bool = false) async {
        let query = normalizedSearchQuery(searchQuery ?? searchText)
        if showsSearchOverlay {
            isSearchLoading = true
        }
        isLoading = true
        defer {
            isLoading = false
            if showsSearchOverlay && normalizedSearchQuery(searchText) == query {
                isSearchLoading = false
            }
            hasLoadedInitialPage = true
        }
        do {
            let items = try await APIClient(tokens: auth).discussions(limit: pageSize, offset: 0, query: query)
            guard normalizedSearchQuery(searchText) == query else { return }
            let selectedID = selection?.id
            loadedSearchQuery = query
            discussions = items
            // Reconcile the iPad detail selection with the refreshed list so the
            // selected row stays highlighted and the detail reflects the newest copy.
            // Only update when the refreshed page still contains it — a selection
            // from a later page must not be dropped by a first-page refresh.
            // (Explicit deletion is what clears selection.)
            if let selectedID, let refreshed = items.first(where: { $0.id == selectedID }) {
                selection = refreshed
            }
            canLoadMore = items.count == pageSize
        } catch {
            reportLoadError(error)
        }
    }

    private func loadMore() async {
        guard canLoadMore, !isLoadingMore, !isLoading else { return }
        let query = loadedSearchQuery
        let offset = discussions.count
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let items = try await APIClient(tokens: auth).discussions(
                limit: pageSize,
                offset: offset,
                query: query
            )
            guard normalizedSearchQuery(searchText) == query, loadedSearchQuery == query else { return }
            let existing = Set(discussions.map(\.id))
            discussions.append(contentsOf: items.filter { !existing.contains($0.id) })
            canLoadMore = items.count == pageSize
        } catch {
            reportLoadError(error)
        }
    }

    private func deleteDiscussions(at offsets: IndexSet) {
        let targets = offsets.map { discussions[$0] }
        let targetIDs = Set(targets.map(\.id))
        discussions.removeAll { targetIDs.contains($0.id) }
        path.removeAll { targetIDs.contains($0.id) }
        if let sel = selection, targetIDs.contains(sel.id) { selection = nil }
        Task {
            let api = APIClient(tokens: auth)
            for target in targets {
                do {
                    try await api.deleteDiscussion(id: target.id)
                } catch {
                    reportLoadError(error)
                    await load(searchQuery: loadedSearchQuery)
                    return
                }
            }
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
                await load(searchQuery: "")
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
            await load(searchQuery: text, showsSearchOverlay: true)
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
            Text("No discussions yet")
                .font(.title3.weight(.semibold))
            Text("Plan an AI panel discussion and generate it as a podcast.")
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
                .multilineTextAlignment(.center)
            Button {
                showingNew = true
            } label: {
                Label("New Discussion", systemImage: "plus")
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
            description: Text("No discussions match your search.")
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
            selection = discussion
        } else {
            path.append(discussion)
        }
    }

    /// Swap the currently-shown discussion for its updated value so a planned
    /// discussion transitions in place to a player, in whichever model is active.
    private func replaceCurrent(with generated: Discussion) {
        if isRegular {
            selection = generated
        } else if let index = path.lastIndex(where: { $0.id == generated.id }) {
            path[index] = generated
        } else {
            path.append(generated)
        }
    }

    @ViewBuilder
    private func destination(for discussion: Discussion) -> some View {
        switch discussion.status {
        case .planning, .failed:
            PlanDetailView(discussion: discussion, initialPlan: pendingPlans[discussion.id]) { generated in
                upsert(generated)
                replaceCurrent(with: generated)
            }
        case .generating, .ready:
            PodcastPlayerView(discussion: discussion)
        }
    }
}

private struct DiscussionRow: View {
    let discussion: Discussion
    var isSelected: Bool = false

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: icon)
                .font(.title2)
                .foregroundStyle(Theme.accent)
                .frame(width: 40)
            VStack(alignment: .leading, spacing: 4) {
                Text(discussion.displayTitle)
                    .font(.headline)
                    .lineLimit(2)
                Text(statusLabel)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
            Image(systemName: "chevron.right").foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .glassCard(tint: isSelected ? Theme.accent.opacity(0.55) : nil)
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
            return String(localized: "Plan - \(discussion.sortedPeople.count) people",
                          comment: "Discussion row status: planning, with the panelist count")
        case .generating:
            return String(localized: "Generating...", comment: "Discussion row status: podcast is generating")
        case .ready:
            return String(localized: "Ready to play", comment: "Discussion row status: podcast is ready")
        case .failed:
            return String(localized: "Failed", comment: "Discussion row status: generation failed")
        }
    }
}
