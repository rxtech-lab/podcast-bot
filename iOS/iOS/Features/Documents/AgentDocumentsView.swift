import Kingfisher
import MarkdownUI
import SwiftUI

struct AgentDocumentLibraryView: View {
    let discussionID: String?
    var title: String = String(localized: "Documents")
    let api: APIClient
    var onOpenPodcast: ((String) -> Void)?

    @Environment(\.dismiss) private var dismiss
    @State private var documents: [AgentDocumentDTO] = []
    @State private var isLoading = true
    @State private var isLoadingMore = false
    @State private var canLoadMore = false
    @State private var loadError: String?
    @State private var paginationError: String?
    @State private var searchText = ""
    @State private var loadedSearchQuery = ""
    @State private var searchTask: Task<Void, Never>?
    @State private var deleteTarget: AgentDocumentDTO?
    @State private var deletingDocumentID: String?
    @State private var deleteError: String?
    private let pageSize = 20

    private var showsAllDocuments: Bool { discussionID == nil }

    private var sections: [AgentDocumentSection] {
        guard showsAllDocuments else { return [] }
        var order: [String] = []
        var documentsBySection: [String: [AgentDocumentDTO]] = [:]
        var titles: [String: String] = [:]
        for document in documents {
            let key = document.discussionID ?? AgentDocumentSection.globalID
            if documentsBySection[key] == nil {
                order.append(key)
                documentsBySection[key] = []
                let podcastTitle = document.podcastTitle?.trimmingCharacters(in: .whitespacesAndNewlines)
                if document.discussionID == nil {
                    titles[key] = String(localized: "Global Documents")
                } else if let podcastTitle, !podcastTitle.isEmpty {
                    titles[key] = podcastTitle
                } else {
                    titles[key] = String(localized: "Podcast")
                }
            }
            documentsBySection[key, default: []].append(document)
        }
        return order.map {
            AgentDocumentSection(id: $0,
                                 title: titles[$0] ?? String(localized: "Documents"),
                                 documents: documentsBySection[$0] ?? [])
        }
    }

    var body: some View {
        NavigationStack {
            searchableContent
        }
        .confirmationDialog(
            String(localized: "Delete this document?", comment: "Confirm deleting an agent-authored document"),
            isPresented: deleteConfirmationBinding,
            titleVisibility: .visible,
            presenting: deleteTarget
        ) { document in
            Button(String(localized: "Delete", comment: "Delete document confirmation button"),
                   role: .destructive) {
                deleteTarget = nil
                Task { await delete(document) }
            }
            Button(String(localized: "Cancel", comment: "Cancel document deletion"), role: .cancel) {}
        }
        .alert(
            String(localized: "Couldn’t delete document", comment: "Document deletion error title"),
            isPresented: deleteErrorBinding
        ) {
            Button("OK", role: .cancel) { deleteError = nil }
        } message: {
            Text(deleteError ?? "")
        }
        .onDisappear { searchTask?.cancel() }
    }

    @ViewBuilder
    private var searchableContent: some View {
        if showsAllDocuments {
            navigationContent
                .searchable(
                    text: $searchText,
                    placement: .navigationBarDrawer(displayMode: .always),
                    prompt: Text("Search documents")
                )
                .onChange(of: searchText) { _, newValue in
                    scheduleSearch(for: newValue)
                }
        } else {
            navigationContent
        }
    }

    private var navigationContent: some View {
        content
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .navigationDestination(for: AgentDocumentDTO.self) { document in
                AgentDocumentView(documentID: document.id,
                                  initialTitle: document.title,
                                  api: api,
                                  onOpenPodcast: onOpenPodcast)
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await load() }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let loadError {
            ContentUnavailableView(String(localized: "Couldn’t load documents"),
                                   systemImage: "doc.badge.ellipsis",
                                   description: Text(loadError))
        } else if documents.isEmpty {
            if showsAllDocuments && !loadedSearchQuery.isEmpty {
                ContentUnavailableView.search(text: loadedSearchQuery)
            } else {
                ContentUnavailableView(String(localized: "No Documents"),
                                       systemImage: "doc.text",
                                       description: Text("Ask the chat agent to write a document and it will appear here."))
            }
        } else {
            List {
                if showsAllDocuments {
                    ForEach(sections) { section in
                        Section(section.title) {
                            ForEach(section.documents) { document in
                                documentRow(document, showsPodcastTitle: false)
                            }
                        }
                    }
                } else {
                    ForEach(documents) { document in
                        documentRow(document, showsPodcastTitle: true)
                    }
                }
                if let paginationError {
                    Button {
                        Task { await loadMore() }
                    } label: {
                        Label(paginationError, systemImage: "arrow.clockwise")
                            .font(.caption)
                    }
                } else if canLoadMore {
                    HStack {
                        Spacer()
                        ProgressView()
                        Spacer()
                    }
                    .listRowSeparator(.hidden)
                    .onAppear { Task { await loadMore() } }
                }
            }
            .refreshable {
                searchTask?.cancel()
                await load(query: normalizedSearchQuery(searchText))
            }
        }
    }

    private func documentRow(_ document: AgentDocumentDTO, showsPodcastTitle: Bool) -> some View {
        NavigationLink(value: document) {
            VStack(alignment: .leading, spacing: 4) {
                Text(document.title)
                    .font(.body.weight(.medium))
                if showsPodcastTitle,
                   let podcastTitle = document.podcastTitle,
                   !podcastTitle.isEmpty {
                    Label(podcastTitle, systemImage: "waveform")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }
                if deletingDocumentID == document.id {
                    ProgressView()
                }
            }
        }
        .accessibilityIdentifier("documents.row.\(document.id)")
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button(role: .destructive) {
                deleteTarget = document
            } label: {
                Label(String(localized: "Delete", comment: "Delete document swipe action"),
                      systemImage: "trash")
            }
            .disabled(deletingDocumentID != nil)
            .accessibilityIdentifier("documents.delete.\(document.id)")
        }
    }

    private func load(query: String? = nil) async {
        let requestedQuery = query ?? normalizedSearchQuery(searchText)
        isLoading = documents.isEmpty
        loadError = nil
        paginationError = nil
        defer { isLoading = false }
        do {
            if showsAllDocuments {
                let response = try await api.allAgentDocuments(limit: pageSize, query: requestedQuery)
                guard normalizedSearchQuery(searchText) == requestedQuery else { return }
                loadedSearchQuery = requestedQuery
                documents = response.documents
                canLoadMore = response.hasMore == true
            } else {
                documents = try await api.agentDocuments(discussionID: discussionID)
                loadedSearchQuery = ""
                canLoadMore = false
            }
        } catch {
            guard !showsAllDocuments || normalizedSearchQuery(searchText) == requestedQuery else { return }
            loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func loadMore(limit: Int? = nil) async {
        guard showsAllDocuments, canLoadMore, !isLoading, !isLoadingMore else { return }
        let requestedQuery = loadedSearchQuery
        let requestedOffset = documents.count
        isLoadingMore = true
        paginationError = nil
        defer { isLoadingMore = false }
        do {
            let response = try await api.allAgentDocuments(
                limit: limit ?? pageSize,
                offset: requestedOffset,
                query: requestedQuery
            )
            guard normalizedSearchQuery(searchText) == requestedQuery,
                  loadedSearchQuery == requestedQuery,
                  documents.count == requestedOffset else { return }
            let existing = Set(documents.map(\.id))
            documents.append(contentsOf: response.documents.filter { !existing.contains($0.id) })
            canLoadMore = response.hasMore == true
        } catch {
            guard normalizedSearchQuery(searchText) == requestedQuery else { return }
            paginationError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func scheduleSearch(for text: String) {
        searchTask?.cancel()
        canLoadMore = false
        paginationError = nil
        let query = normalizedSearchQuery(text)
        searchTask = Task {
            try? await Task.sleep(for: .milliseconds(350))
            guard !Task.isCancelled else { return }
            await load(query: query)
        }
    }

    private func normalizedSearchQuery(_ text: String) -> String {
        text.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var deleteConfirmationBinding: Binding<Bool> {
        Binding(
            get: { deleteTarget != nil },
            set: { if !$0 { deleteTarget = nil } }
        )
    }

    private var deleteErrorBinding: Binding<Bool> {
        Binding(
            get: { deleteError != nil },
            set: { if !$0 { deleteError = nil } }
        )
    }

    private func delete(_ document: AgentDocumentDTO) async {
        guard deletingDocumentID == nil else { return }
        deletingDocumentID = document.id
        defer { deletingDocumentID = nil }
        do {
            try await api.deleteAgentDocument(id: document.id)
            let shouldBackfill = canLoadMore
            documents.removeAll { $0.id == document.id }
            if shouldBackfill {
                await loadMore(limit: 1)
            }
        } catch {
            deleteError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

private struct AgentDocumentSection: Identifiable {
    static let globalID = "__global__"

    let id: String
    let title: String
    let documents: [AgentDocumentDTO]
}

struct AgentDocumentView: View {
    let documentID: String
    var initialTitle: String = String(localized: "Document")
    let api: APIClient
    var onOpenPodcast: ((String) -> Void)?

    @State private var document: AgentDocumentDTO?
    @State private var actionItems: [DiscussionUIActionItem] = []
    @State private var isLoading = true
    @State private var loadError: String?
    @State private var exportFile: ExportedSummaryFile?
    @State private var isPreparingPDF = false
    @State private var exportError: String?
    @State private var showingNotionExport = false

    var body: some View {
        content
            .navigationTitle(document?.title ?? initialTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    DiscussionActionsMenu(
                        items: actionItems,
                        labelSystemImage: "ellipsis.circle",
                        accessibilityLabel: "Document actions",
                        isBusy: { $0.id == "download-pdf" && isPreparingPDF },
                        perform: performDocumentAction
                    )
                }
            }
            .sheet(item: $exportFile) { file in
                FileShareSheet(url: file.url)
            }
            .sheet(isPresented: $showingNotionExport) {
                NotionExportSheet(api: api, documentID: documentID)
            }
            .alert("Couldn’t export", isPresented: Binding(
                get: { exportError != nil },
                set: { if !$0 { exportError = nil } }
            )) {
                Button("OK", role: .cancel) { exportError = nil }
            } message: {
                Text(exportError ?? "")
            }
            .overlay {
                if isPreparingPDF {
                    ZStack {
                        Color.black.opacity(0.2).ignoresSafeArea()
                        ProgressView("Preparing PDF…")
                            .padding(20)
                            .background(.regularMaterial, in: .rect(cornerRadius: 14))
                    }
                }
            }
            .task {
                async let documentLoad: Void = load()
                async let actionLoad: Void = loadActions()
                _ = await (documentLoad, actionLoad)
            }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let markdown = document?.markdown, !markdown.isEmpty {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let discussionID = document?.discussionID,
                       let podcastTitle = document?.podcastTitle,
                       !podcastTitle.isEmpty,
                       let onOpenPodcast {
                        Button { onOpenPodcast(discussionID) } label: {
                            Label(podcastTitle, systemImage: "waveform.circle.fill")
                                .font(.subheadline.weight(.semibold))
                        }
                        .buttonStyle(.bordered)
                    }
                    Markdown(markdown)
                        .markdownImageProvider(KingfisherMarkdownImageProvider())
                        .markdownBlockStyle(\.codeBlock) { configuration in
                            if configuration.language?.lowercased() == "mermaid" {
                                MermaidBlock(code: configuration.content)
                            } else {
                                configuration.label
                            }
                        }
                }
                .padding()
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            ContentUnavailableView("Document unavailable",
                                   systemImage: "doc.badge.ellipsis",
                                   description: Text(loadError ?? "Try again later."))
        }
    }

    private func load() async {
        isLoading = true
        loadError = nil
        defer { isLoading = false }
        do {
            document = try await api.agentDocument(id: documentID)
        } catch {
            loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func loadActions() async {
        do {
            actionItems = try await api.agentDocumentUIActions(id: documentID).items
        } catch {
            actionItems = []
        }
    }

    private func performDocumentAction(_ item: DiscussionUIActionItem) {
        guard let path = validatedDocumentActionPath(item) else { return }
        switch path {
        case ["export", "pdf"]:
            Task { await downloadPDF() }
        case ["export", "markdown"]:
            downloadMarkdown()
        case ["sheet", "notion"]:
            showingNotionExport = true
        default:
            break
        }
    }

    private func validatedDocumentActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "document" else { return nil }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == documentID else { return nil }
        return Array(components.dropFirst())
    }

    private func downloadPDF() async {
        guard let document, !isPreparingPDF else { return }
        isPreparingPDF = true
        defer { isPreparingPDF = false }
        do {
            exportFile = try ExportedSummaryFile(
                url: await api.downloadAgentDocumentPDF(id: document.id, title: document.title)
            )
        } catch {
            exportError = String(localized: "Couldn’t export")
        }
    }

    private func downloadMarkdown() {
        guard let document, let markdown = document.markdown, !markdown.isEmpty else { return }
        do {
            exportFile = try ExportedSummaryFile(url: api.writeSummaryMarkdown(markdown, title: document.title))
        } catch {
            exportError = String(localized: "Couldn’t export")
        }
    }
}

struct QAAgentDocumentCardView: View {
    let document: QAAgentDocumentCard
    var onTap: () -> Void = {}

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 12) {
                Image(systemName: "doc.text.fill")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 40, height: 40)
                    .background(Theme.accent.opacity(0.12), in: .rect(cornerRadius: 10))
                VStack(alignment: .leading, spacing: 3) {
                    Text(document.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(2)
                    Text(document.podcastTitle?.isEmpty == false ? document.podcastTitle! : String(localized: "Document"))
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(1)
                }
                Spacer(minLength: 6)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Theme.secondaryText.opacity(0.7))
            }
            .padding(12)
            .frame(maxWidth: 300, alignment: .leading)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .strokeBorder(Theme.accent.opacity(0.18), lineWidth: 1)
            )
        }
        .buttonStyle(PlanningToolCardButtonStyle())
        .accessibilityIdentifier("qa.card.document.\(document.id)")
    }
}
