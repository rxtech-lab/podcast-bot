import SwiftUI
import SwiftData

/// Home: the user's discussions (synced via iCloud), newest first, with a button
/// to plan a new one. Routing by status: planning → plan editor, ready/generating
/// → podcast player.
struct LibraryView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.modelContext) private var context
    @Query(sort: \Discussion.updatedAt, order: .reverse) private var discussions: [Discussion]
    @State private var showingNew = false
    @State private var path: [Discussion] = []
    @State private var deletionError: String?

    var body: some View {
        NavigationStack(path: $path) {
            ZStack {
                Theme.background.ignoresSafeArea()
                if discussions.isEmpty {
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
                    path.append(discussion)
                }
            }
            .alert("Could not delete discussion", isPresented: deletionErrorBinding) {
                Button("OK", role: .cancel) { deletionError = nil }
            } message: {
                Text(deletionError ?? "")
            }
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
            }
            .onDelete(perform: deleteDiscussions)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(Color.clear)
    }

    private var deletionErrorBinding: Binding<Bool> {
        Binding(
            get: { deletionError != nil },
            set: { if !$0 { deletionError = nil } }
        )
    }

    private func deleteDiscussions(at offsets: IndexSet) {
        let targets = offsets.map { discussions[$0] }
        let targetIDs = Set(targets.map(\.id))

        path.removeAll { targetIDs.contains($0.id) }
        targets.forEach(context.delete)

        do {
            try context.save()
        } catch {
            context.rollback()
            deletionError = error.localizedDescription
        }
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
            PlanDetailView(discussion: discussion)
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
                Text(discussion.title.isEmpty ? discussion.topic : discussion.title)
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
        case .planning: return "Plan • \(discussion.sortedPeople.count) people"
        case .generating: return "Generating…"
        case .ready: return "Ready to play"
        case .failed: return "Failed"
        }
    }
}
