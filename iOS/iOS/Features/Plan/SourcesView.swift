import SwiftUI

/// Lists every source the plan cites and lets the user add a link. Saving a new
/// link asks the engine to research it and fold it into the plan; the updated
/// discussion is propagated via `onUpdated`.
struct SourcesSheet: View {
    @Environment(\.dismiss) private var dismiss

    @State private var discussion: Discussion

    var onUpdateStarted: () -> Void
    var onUpdated: (Discussion) -> Void
    var onUpdateFailed: (String) -> Void

    init(
        discussion: Discussion,
        onUpdateStarted: @escaping () -> Void = {},
        onUpdated: @escaping (Discussion) -> Void = { _ in },
        onUpdateFailed: @escaping (String) -> Void = { _ in }
    ) {
        _discussion = State(initialValue: discussion)
        self.onUpdateStarted = onUpdateStarted
        self.onUpdated = onUpdated
        self.onUpdateFailed = onUpdateFailed
    }

    private var sources: [PlanSourceSnapshot] { discussion.sortedSources }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                list
            }
            .navigationTitle("Sources")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    addSourcesLink
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                }
            }
        }
    }

    private var list: some View {
        List {
            if sources.isEmpty {
                Text("No sources yet. Add a source from the toolbar and the agent will research it and update the plan.")
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
                    .listRowBackground(Color.clear)
            } else {
                Section {
                    ForEach(sources) { source in
                        sourceRow(source)
                    }
                } header: {
                    Text("\(sources.count) source\(sources.count == 1 ? "" : "s")")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(Theme.accent)
                }
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
        .scrollDismissesKeyboard(.interactively)
        .interactiveDismissDisabled()
    }

    @ViewBuilder
    private func sourceRow(_ source: PlanSourceSnapshot) -> some View {
        NavigationLink {
            SourceDetailView(source: source)
        } label: {
            HStack(alignment: .center, spacing: 10) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(source.displayTitle)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.white)
                    if !source.urlString.isEmpty {
                        Text(source.urlString)
                            .font(.caption2)
                            .foregroundStyle(Theme.accent)
                            .lineLimit(1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .buttonStyle(.plain)
        .listRowBackground(Color.white.opacity(0.05))
    }

    private var addSourcesLink: some View {
        NavigationLink {
            AddSourcesView(
                discussionID: discussion.id,
                onSaveStarted: {
                    onUpdateStarted()
                    dismiss()
                },
                onUpdated: { updated in
                    discussion = updated
                    onUpdated(updated)
                },
                onFailed: onUpdateFailed
            )
        } label: {
            Label("Add Sources", systemImage: "plus")
        }
    }
}

private struct DraftWebLink: Identifiable, Equatable {
    let id = UUID()
    var title: String
    var urlString: String

    var displayTitle: String { title.isEmpty ? urlString : title }
}

private struct AddSourcesView: View {
    @Environment(AuthManager.self) private var auth

    let discussionID: String
    var onSaveStarted: () -> Void
    var onUpdated: (Discussion) -> Void
    var onFailed: (String) -> Void

    @State private var inputText = ""
    @State private var links: [DraftWebLink] = []
    @State private var isSearching = false
    @State private var isSaving = false
    @State private var errorMessage: String?

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                linksList
            }
        }
        .navigationTitle("Add Sources")
        .navigationBarTitleDisplayMode(.inline)
        .disabled(isSaving || isSearching)
        .searchable(text: $inputText, prompt: "Add link or search")
        .onSubmit(of: .search, submitInput)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: save) {
                    if isSaving {
                        ProgressView()
                            .controlSize(.small)
                            .tint(Theme.accent)
                    } else {
                        Text("Save")
                    }
                }
                .disabled(!canSave)
            }
            DefaultToolbarItem(kind: .search, placement: .bottomBar)
        }
    }

    private var linksList: some View {
        List {
            if links.isEmpty {
                Text("No links added")
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
                    .listRowBackground(Color.clear)
            } else {
                ForEach(links) { link in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(link.displayTitle)
                            .font(.subheadline.weight(.medium))
                            .foregroundStyle(.white)
                        Text(link.urlString)
                            .font(.caption2)
                            .foregroundStyle(Theme.accent)
                            .lineLimit(1)
                    }
                    .listRowBackground(Color.white.opacity(0.05))
                }
                .onDelete { offsets in
                    links.remove(atOffsets: offsets)
                }
            }
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .listRowBackground(Color.clear)
            }
            if isSearching {
                HStack(spacing: 8) {
                    ProgressView()
                        .controlSize(.small)
                        .tint(Theme.accent)
                    Text("Searching sources")
                        .font(.footnote)
                        .foregroundStyle(Theme.secondaryText)
                }
                .listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        .scrollContentBackground(.hidden)
    }

    private var trimmedInput: String {
        inputText.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var inputLink: String? {
        normalizedLink(inputText)
    }

    private var canSave: Bool {
        !links.isEmpty && !isSearching && !isSaving
    }

    private func submitInput() {
        guard !isSearching, !isSaving, !trimmedInput.isEmpty else { return }
        if let url = inputLink {
            addLink(url)
        } else {
            searchLinks(trimmedInput)
        }
    }

    private func addLink(_ url: String) {
        appendLinks([DraftWebLink(title: "", urlString: url)])
        inputText = ""
    }

    private func searchLinks(_ query: String) {
        guard !query.isEmpty else { return }
        inputText = ""
        isSearching = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                let found = try await api.searchDiscussionSources(id: discussionID, query: query)
                let mapped = found.map { DraftWebLink(title: $0.title, urlString: $0.url) }
                appendLinks(mapped)
                isSearching = false
            } catch {
                isSearching = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func save() {
        guard !links.isEmpty, !isSaving else { return }
        isSaving = true
        errorMessage = nil
        let urls = links.map(\.urlString)
        let api = APIClient(tokens: auth)
        onSaveStarted()
        Task {
            do {
                let updated = try await api.addDiscussionSources(id: discussionID, urls: urls)
                isSaving = false
                onUpdated(updated)
            } catch {
                let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                isSaving = false
                errorMessage = message
                onFailed(message)
            }
        }
    }

    private func appendLinks(_ candidates: [DraftWebLink]) {
        var seen = Set(links.map { $0.urlString })
        for candidate in candidates {
            guard let url = normalizedLink(candidate.urlString), !seen.contains(url) else { continue }
            links.append(DraftWebLink(title: candidate.title, urlString: url))
            seen.insert(url)
        }
    }

    private func normalizedLink(_ raw: String) -> String? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !trimmed.contains(where: \.isWhitespace) else { return nil }

        if let url = URL(string: trimmed), let scheme = url.scheme?.lowercased(),
           scheme == "http" || scheme == "https", url.host != nil
        {
            return trimmed
        }

        guard !trimmed.contains("://") else { return nil }
        let candidate = "https://\(trimmed)"
        guard let url = URL(string: candidate), let host = url.host, host.contains(".") else { return nil }
        return candidate
    }
}

private struct SourceDetailView: View {
    let source: PlanSourceSnapshot

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(source.displayTitle)
                            .font(.title3.weight(.semibold))
                            .foregroundStyle(.white)
                        if let url = source.url {
                            Link(source.urlString, destination: url)
                                .font(.caption)
                                .foregroundStyle(Theme.accent)
                        } else if !source.urlString.isEmpty {
                            Text(source.urlString)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }

                    if source.detailMarkdown.isEmpty {
                        Text("No readable content was returned for this source.")
                            .font(.callout)
                            .foregroundStyle(Theme.secondaryText)
                    } else {
                        MarkdownText(source.detailMarkdown)
                            .font(.body)
                            .foregroundStyle(Theme.secondaryText)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(16)
            }
        }
        .navigationTitle("Source")
        .navigationBarTitleDisplayMode(.inline)
    }
}
