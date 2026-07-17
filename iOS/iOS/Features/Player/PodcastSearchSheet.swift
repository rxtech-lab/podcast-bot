import SwiftUI

/// In-podcast semantic search: finds moments and source passages inside one
/// finished podcast, showing the matched text + similarity score.
struct PodcastSearchSheet: View {
    @Environment(AuthManager.self) var auth
    @Environment(\.dismiss) private var dismiss
    let discussion: Discussion

    @State private var searchText = ""
    @State private var loadedQuery = ""
    @State private var matches: [SemanticMatch] = []
    @State private var isSearching = false
    @State private var searchUnavailable = false
    @State private var errorMessage: String?
    @State private var searchTask: Task<Void, Never>?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                content
            }
            .navigationTitle(String(localized: "Search Episode", comment: "In-podcast semantic search sheet title"))
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $searchText,
                        placement: .navigationBarDrawer(displayMode: .always),
                        prompt: String(localized: "Search this podcast", comment: "In-podcast search field prompt"))
            .autocorrectionDisabled(true)
            .textInputAutocapitalization(.never)
            .onChange(of: searchText) { _, newValue in
                scheduleSearch(for: newValue)
            }
            .onSubmit(of: .search) {
                searchTask?.cancel()
                Task { await search(query: searchText) }
            }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(String(localized: "Done", comment: "Dismiss the in-podcast search sheet")) { dismiss() }
                }
            }
            .onDisappear { searchTask?.cancel() }
        }
    }

    @ViewBuilder
    private var content: some View {
        if isSearching && matches.isEmpty {
            ProgressView().tint(Theme.accent)
        } else if searchUnavailable {
            ContentUnavailableView(
                String(localized: "Search Unavailable", comment: "In-podcast search state when semantic search is unconfigured or still indexing"),
                systemImage: "magnifyingglass",
                description: Text(String(localized: "This podcast's content is still being indexed. Try again in a moment.",
                                         comment: "In-podcast search unavailable message"))
            )
        } else if let errorMessage {
            ContentUnavailableView(
                String(localized: "Search Failed", comment: "In-podcast search error state title"),
                systemImage: "exclamationmark.triangle",
                description: Text(errorMessage)
            )
        } else if matches.isEmpty && !loadedQuery.isEmpty {
            ContentUnavailableView(
                String(localized: "No Matches", comment: "In-podcast search empty result title"),
                systemImage: "magnifyingglass",
                description: Text(String(localized: "Nothing in this podcast matches your search.",
                                         comment: "In-podcast search empty result message"))
            )
        } else if matches.isEmpty {
            ContentUnavailableView(
                String(localized: "Search This Episode", comment: "In-podcast search initial state title"),
                systemImage: "waveform.and.magnifyingglass",
                description: Text(String(localized: "Find moments and source passages by meaning, not just keywords.",
                                         comment: "In-podcast search initial state message"))
            )
        } else {
            List {
                ForEach(matches) { match in
                    SemanticMatchRow(match: match)
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                        .listRowInsets(.init(top: 4, leading: 16, bottom: 4, trailing: 16))
                }
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .scrollDismissesKeyboard(.interactively)
        }
    }

    private func scheduleSearch(for text: String) {
        let query = text.trimmingCharacters(in: .whitespacesAndNewlines)
        searchTask?.cancel()
        guard !query.isEmpty else {
            matches = []
            loadedQuery = ""
            isSearching = false
            return
        }
        guard query != loadedQuery else { return }
        searchTask = Task {
            try? await Task.sleep(for: .milliseconds(350))
            guard !Task.isCancelled else { return }
            await search(query: query)
        }
    }

    @MainActor
    private func search(query: String) async {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        isSearching = true
        errorMessage = nil
        defer { isSearching = false }
        do {
            let response = try await APIClient(tokens: auth).discussionSemanticSearch(id: discussion.id, query: trimmed)
            guard searchText.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed else { return }
            searchUnavailable = !response.enabled
            matches = response.matches
            loadedQuery = trimmed
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}
