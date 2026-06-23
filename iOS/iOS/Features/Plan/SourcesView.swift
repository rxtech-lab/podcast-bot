import SwiftUI

/// Lists every source the plan cites and lets the user add a link. Saving a new
/// link asks the engine to research it and fold it into the plan; the updated
/// discussion is propagated via `onUpdated`.
struct SourcesSheet: View {
    @Environment(\.dismiss) private var dismiss

    @State private var discussion: Discussion

    var allowsAddingSources: Bool
    var onUpdateStarted: ([String]) -> Void
    var onUpdateProgress: (PlanProgressEvent) -> Void
    var onUpdated: (Discussion) -> Void
    var onUpdateFailed: (String) -> Void

    init(
        discussion: Discussion,
        allowsAddingSources: Bool = true,
        onUpdateStarted: @escaping ([String]) -> Void = { _ in },
        onUpdateProgress: @escaping (PlanProgressEvent) -> Void = { _ in },
        onUpdated: @escaping (Discussion) -> Void = { _ in },
        onUpdateFailed: @escaping (String) -> Void = { _ in }
    ) {
        _discussion = State(initialValue: discussion)
        self.allowsAddingSources = allowsAddingSources
        self.onUpdateStarted = onUpdateStarted
        self.onUpdateProgress = onUpdateProgress
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
                    if allowsAddingSources {
                        addSourcesLink
                    }
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
                Text(emptySourcesText)
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
                    .listRowBackground(Color.clear)
            } else {
                Section {
                    ForEach(sources) { source in
                        sourceRow(source)
                    }
                } header: {
                    Text(sourcesCountText)
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

    private var sourcesCountText: String {
        let count = sources.count
        return count == 1
            ? String(localized: "\(count) source", comment: "Sources section header, singular")
            : String(localized: "\(count) sources", comment: "Sources section header, plural")
    }

    private var emptySourcesText: String {
        if allowsAddingSources {
            return String(localized: "No sources yet. Add a source from the toolbar and the agent will research it and update the plan.",
                          comment: "Empty state when sources can be added")
        }
        return String(localized: "No sources were saved for this plan.",
                      comment: "Empty state when sources are read-only")
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
                        .foregroundStyle(.primary)
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
        .listRowBackground(Theme.rowBackground)
    }

    private var addSourcesLink: some View {
        NavigationLink {
            AddSourcesView(
                discussionID: discussion.id,
                onSaveStarted: { urls in
                    onUpdateStarted(urls)
                    dismiss()
                },
                onProgress: onUpdateProgress,
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
    @Environment(PurchaseManager.self) private var purchases

    let discussionID: String
    var onSaveStarted: ([String]) -> Void
    var onProgress: (PlanProgressEvent) -> Void
    var onUpdated: (Discussion) -> Void
    var onFailed: (String) -> Void

    @State private var inputText = ""
    @State private var links: [DraftWebLink] = []
    @State private var selectedLinkIDs = Set<DraftWebLink.ID>()
    @State private var isSearching = false
    @State private var isSaving = false
    @State private var errorMessage: String?
    @State private var showingPaywall = false

    /// If the error is a points shortfall, open the paywall and stop. Returns
    /// true when handled so the caller skips its normal failure path.
    private func handleInsufficientPoints(_ error: Error) -> Bool {
        guard case let APIError.insufficientPoints(required, balance) = error else { return false }
        isSaving = false
        isSearching = false
        errorMessage = String(localized: "You need \(UsageSummary.formatInt(required)) points but have \(UsageSummary.formatInt(balance)).",
                              comment: "Shown when the user lacks enough points to add sources; values are formatted point amounts")
        Task { await purchases.refreshBalance() }
        showingPaywall = true
        return true
    }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            VStack(spacing: 0) {
                linksList
            }
        }
        .sheet(isPresented: $showingPaywall) { PaywallScreen() }
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
                    linkRow(link)
                    .listRowBackground(Theme.rowBackground)
                }
                .onDelete { offsets in
                    let deletedIDs = offsets.map { links[$0].id }
                    links.remove(atOffsets: offsets)
                    selectedLinkIDs.subtract(deletedIDs)
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

    private func linkRow(_ link: DraftWebLink) -> some View {
        HStack(alignment: .center, spacing: 12) {
            Button {
                toggleSelection(for: link)
            } label: {
                Image(systemName: selectedLinkIDs.contains(link.id) ? "checkmark.circle.fill" : "circle")
                    .font(.title3)
                    .foregroundStyle(selectedLinkIDs.contains(link.id) ? Theme.accent : Theme.secondaryText)
                    .frame(width: 28, height: 28)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(selectedLinkIDs.contains(link.id) ? "Deselect source" : "Select source")
            .accessibilityValue(link.displayTitle)

            VStack(alignment: .leading, spacing: 4) {
                Text(link.displayTitle)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.primary)
                Text(link.urlString)
                    .font(.caption2)
                    .foregroundStyle(Theme.accent)
                    .lineLimit(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
            .onTapGesture {
                toggleSelection(for: link)
            }
        }
    }

    private var trimmedInput: String {
        inputText.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var inputLink: String? {
        normalizedLink(inputText)
    }

    private var selectedLinks: [DraftWebLink] {
        links.filter { selectedLinkIDs.contains($0.id) }
    }

    private var canSave: Bool {
        !selectedLinkIDs.isEmpty && !isSearching && !isSaving
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
        appendLinks([DraftWebLink(title: "", urlString: url)], selected: true)
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
                appendLinks(mapped, selected: false)
                isSearching = false
            } catch {
                if handleInsufficientPoints(error) { return }
                isSearching = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func save() {
        let chosenLinks = selectedLinks
        guard !chosenLinks.isEmpty, !isSaving else { return }
        isSaving = true
        errorMessage = nil
        let urls = chosenLinks.map(\.urlString)
        let api = APIClient(tokens: auth)
        onSaveStarted(urls)
        Task {
            do {
                var didFinish = false
                for try await event in api.addDiscussionSourcesStream(id: discussionID, urls: urls) {
                    switch event {
                    case let .progress(step):
                        onProgress(step)
                    case let .done(updated):
                        didFinish = true
                        isSaving = false
                        onUpdated(updated)
                    case let .failed(message):
                        didFinish = true
                        isSaving = false
                        onFailed(message)
                    }
                }
                if !didFinish {
                    isSaving = false
                    onFailed(String(localized: "The plan update stopped before it finished. Please try again.",
                                    comment: "Shown when the add-sources stream ended without a terminal event"))
                }
            } catch {
                if handleInsufficientPoints(error) { return }
                let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                isSaving = false
                errorMessage = message
                onFailed(message)
            }
        }
    }

    private func toggleSelection(for link: DraftWebLink) {
        if selectedLinkIDs.contains(link.id) {
            selectedLinkIDs.remove(link.id)
        } else {
            selectedLinkIDs.insert(link.id)
        }
    }

    private func appendLinks(_ candidates: [DraftWebLink], selected: Bool) {
        var seen = Set(links.map { $0.urlString })
        for candidate in candidates {
            guard let url = normalizedLink(candidate.urlString), !seen.contains(url) else { continue }
            let link = DraftWebLink(title: candidate.title, urlString: url)
            links.append(link)
            if selected {
                selectedLinkIDs.insert(link.id)
            }
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
                            .foregroundStyle(.primary)
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
