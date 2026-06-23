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
    @State private var path: [Discussion] = []
    /// Detail selection for the iPad split-view layout.
    @State private var selection: Discussion?
    @State private var isLoading = false
    @State private var hasLoadedInitialPage = false
    @State private var isLoadingMore = false
    @State private var canLoadMore = true
    @State private var errorMessage: String?
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
        .task { await load() }
        .task { await purchases.refreshBalance() }
    }

    /// iPhone / compact: single-column stack-based navigation.
    private var stackView: some View {
        NavigationStack(path: $path) {
            libraryContainer
                .navigationTitle("Discussions")
                .toolbar { libraryToolbar }
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
        ZStack {
            Theme.background.ignoresSafeArea()
            if shouldShowInitialLoader {
                ProgressView().tint(Theme.accent)
            } else if discussions.isEmpty {
                emptyState
            } else {
                list
            }
        }
    }

    @ToolbarContentBuilder
    private var libraryToolbar: some ToolbarContent {
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
                Button("Refresh") { Task { await load(); await purchases.refreshBalance() } }
                Button("Sign Out", role: .destructive) { Task { await auth.signOut() } }
            } label: { Image(systemName: "person.crop.circle") }
        }
    }

    /// Balance label for the user menu, e.g. "Points (Balance 1,200 Points)".
    private var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else { return "Points" }
        let greaterThanOne = balance > 1
        return "Points (\(UsageSummary.formatInt(balance)) Point\(greaterThanOne ? "s" : ""))"
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

    private func load() async {
        isLoading = true
        defer {
            isLoading = false
            hasLoadedInitialPage = true
        }
        do {
            let items = try await APIClient(tokens: auth).discussions(limit: pageSize, offset: 0)
            let selectedID = selection?.id
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
        isLoadingMore = true
        defer { isLoadingMore = false }
        do {
            let items = try await APIClient(tokens: auth).discussions(limit: pageSize, offset: discussions.count)
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
                    await load()
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
        case .planning: return "Plan - \(discussion.sortedPeople.count) people"
        case .generating: return "Generating..."
        case .ready: return "Ready to play"
        case .failed: return "Failed"
        }
    }
}
