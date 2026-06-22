import SwiftUI

/// Home: the user's server-owned discussions, newest first.
struct LibraryView: View {
    @Environment(AuthManager.self) private var auth
    @State private var discussions: [Discussion] = []
    @State private var showingNew = false
    @State private var path: [Discussion] = []
    @State private var isLoading = false
    @State private var hasLoadedInitialPage = false
    @State private var isLoadingMore = false
    @State private var canLoadMore = true
    @State private var errorMessage: String?

    private let pageSize = 20

    var body: some View {
        NavigationStack(path: $path) {
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
            .navigationTitle("Discussions")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showingNew = true } label: { Image(systemName: "plus") }
                }
                ToolbarItem(placement: .topBarLeading) {
                    Menu {
                        Button("Refresh") { Task { await load() } }
                        Button("Sign Out", role: .destructive) { Task { await auth.signOut() } }
                    } label: { Image(systemName: "person.crop.circle") }
                }
            }
            .navigationDestination(for: Discussion.self) { discussion in
                destination(for: discussion)
            }
            .sheet(isPresented: $showingNew) {
                NewDiscussionView { discussion in
                    showingNew = false
                    upsert(discussion)
                    path.append(discussion)
                }
            }
            .alert("Could not load discussions", isPresented: errorBinding) {
                Button("OK", role: .cancel) { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .task { await load() }
            .refreshable { await load() }
        }
    }

    private var list: some View {
        List {
            ForEach(discussions) { d in
                Button {
                    path.append(d)
                } label: {
                    DiscussionRow(discussion: d)
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
            discussions = items
            canLoadMore = items.count == pageSize
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
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
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func deleteDiscussions(at offsets: IndexSet) {
        let targets = offsets.map { discussions[$0] }
        let targetIDs = Set(targets.map(\.id))
        discussions.removeAll { targetIDs.contains($0.id) }
        path.removeAll { targetIDs.contains($0.id) }
        Task {
            let api = APIClient(tokens: auth)
            for target in targets {
                do {
                    try await api.deleteDiscussion(id: target.id)
                } catch {
                    errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
                    await load()
                    return
                }
            }
        }
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

    @ViewBuilder
    private func destination(for discussion: Discussion) -> some View {
        switch discussion.status {
        case .planning, .failed:
            PlanDetailView(discussion: discussion) { generated in
                upsert(generated)
                if let index = path.lastIndex(where: { $0.id == generated.id }) {
                    path[index] = generated
                } else {
                    path.append(generated)
                }
            }
        case .generating, .ready:
            PodcastPlayerView(discussion: discussion)
        }
    }
}

private struct DiscussionRow: View {
    let discussion: Discussion

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
        .glassCard()
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
