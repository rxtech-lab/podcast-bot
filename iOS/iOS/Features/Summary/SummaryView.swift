import AuthenticationServices
import BeautifulMermaid
import Kingfisher
import MarkdownUI
import QuickLook
import SwiftUI
import TipKit
import os

private let summaryViewLog = Logger(subsystem: "com.debatebot.ios", category: "SummaryView")

/// Displays a finished podcast's generated summary document. The Markdown body
/// is fetched only when this view mounts (the podcast detail never carries it),
/// rendered with swift-markdown-ui (non-streaming), and any ```mermaid fenced
/// block is drawn natively with beautiful-mermaid-swift.
///
/// A toolbar picker selects the document type. The Markdown summary is always
/// available here; a generated slide deck appears when the server has stored the
/// `ppt` summary document.
struct SummaryView: View {
    let discussionID: String
    /// Used only to name the exported PDF / Markdown file; defaults to "Summary".
    var title: String = "Summary"
    /// Whether the mindmap opened from the embedded summary link is editable
    /// (i.e. the viewer owns the discussion).
    var mindmapEditable: Bool = false
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var docType = "summary"
    @State private var document: SummaryDocument?
    @State private var isLoading = true
    @State private var loadError: String?
    @State private var isPPTDeckAvailable = false
    @State private var pptPreviewFile: ExportedSummaryFile?

    /// The temp file (PDF or Markdown) to hand to the system share sheet.
    @State private var exportFile: ExportedSummaryFile?
    @State private var isPreparingPDF = false
    @State private var isPreparingPPTX = false
    @State private var isPreparingSlidesPDF = false
    @State private var exportError: String?
    @State private var showingNotionExport = false
    @State private var showingMindmap = false
    @State private var actionItems: [DiscussionUIActionItem] = []

    private var isSummaryDocumentSelected: Bool { docType == "summary" }
    private var isPPTDocumentSelected: Bool { docType == "ppt" }

    private var canExport: Bool {
        guard let document else { return false }
        return !document.markdown.isEmpty
    }

    private var canExportMarkdownDocument: Bool {
        isSummaryDocumentSelected && canExport
    }

    private var canExportSlideDeck: Bool {
        if isSummaryDocumentSelected {
            return canExport
        }
        return isPPTDocumentSelected && isPPTDeckAvailable
    }

    private var isPreparingExport: Bool {
        isPreparingPDF || isPreparingPPTX || isPreparingSlidesPDF
    }

    private var preparingExportTitle: LocalizedStringKey {
        if isPreparingPPTX { return "Preparing PPTX…" }
        if isPreparingSlidesPDF { return "Preparing slides PDF…" }
        return "Preparing PDF…"
    }

    private var pptxExportTitle: LocalizedStringKey {
        if isPreparingPPTX { return "Preparing PPTX…" }
        return isPPTDeckAvailable ? "Download PPTX" : "Generate PPTX"
    }

    private var slidesPDFExportTitle: LocalizedStringKey {
        if isPreparingSlidesPDF { return "Preparing slides PDF…" }
        return isPPTDeckAvailable ? "Download slides PDF" : "Generate slides PDF"
    }

    private var pptxExportIcon: String {
        if isPreparingPPTX { return "hourglass" }
        return isPPTDeckAvailable ? "rectangle.on.rectangle" : "wand.and.stars"
    }

    private var slidesPDFExportIcon: String {
        if isPreparingSlidesPDF { return "hourglass" }
        return isPPTDeckAvailable ? "rectangle.stack" : "wand.and.stars"
    }

    var body: some View {
        NavigationStack {
            content
                .overlay {
                    if isPreparingExport {
                        exportPreparingOverlay
                    }
                }
                .navigationTitle("Summary")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Done") { dismiss() }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        documentTypeMenu
                    }
                }
                .sheet(item: $exportFile) { file in
                    FileShareSheet(url: file.url)
                }
                .sheet(isPresented: $showingNotionExport) {
                    NotionExportSheet(api: api, discussionID: discussionID, docType: docType)
                }
                .sheet(isPresented: $showingMindmap) {
                    MindmapView(discussionID: discussionID,
                                title: title,
                                isEditable: mindmapEditable,
                                api: api)
                }
                .alert("Couldn’t export", isPresented: Binding(
                    get: { exportError != nil },
                    set: { if !$0 { exportError = nil } }
                )) {
                    Button("OK", role: .cancel) { exportError = nil }
                } message: {
                    Text(exportError ?? "")
                }
                .task(id: docType) {
                    await load()
                    await loadSummaryActions()
                }
        }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if isPPTDocumentSelected {
            pptPreviewContent
        } else if let document, !document.markdown.isEmpty {
            markdownContent(markdown: document.markdown)
        } else {
            emptyState
        }
    }

    private func markdownContent(markdown: String) -> some View {
        ScrollView {
            Markdown(markdown)
                .markdownImageProvider(KingfisherMarkdownImageProvider())
                .markdownBlockStyle(\.codeBlock) { configuration in
                    if configuration.language?.lowercased() == "mermaid" {
                        MermaidBlock(code: configuration.content)
                    } else {
                        configuration.label
                    }
                }
                .padding()
        }
        // The server embeds a debatepod:// mindmap deep link in the summary
        // body; intercept it to present the mindmap sheet in-app instead of
        // bouncing the custom scheme through the system.
        .environment(\.openURL, OpenURLAction { url in
            handleSummaryMarkdownLink(url)
        })
    }

    private func handleSummaryMarkdownLink(_ url: URL) -> OpenURLAction.Result {
        guard url.scheme == "debatepod", url.host == "discussion" else { return .systemAction }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == discussionID else { return .systemAction }
        if Array(components.dropFirst()) == ["sheet", "mindmap"] {
            showingMindmap = true
            return .handled
        }
        return .systemAction
    }

    private var pptPreviewContent: some View {
        Group {
            if let url = pptPreviewFile?.url {
                SummaryPPTXPreview(url: url)
            } else {
                emptyState
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.richtext")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text(loadError ?? "The summary isn’t available yet.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Button("Retry") { Task { await load() } }
                .buttonStyle(.bordered)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    /// Backend-rendered document and export actions. The slide deck is generated
    /// lazily from the loaded summary when it does not exist yet.
    @ViewBuilder
    private var documentTypeMenu: some View {
        let menu = DiscussionActionsMenu(
            items: actionItems,
            labelSystemImage: "ellipsis.circle",
            accessibilityLabel: "Summary actions",
            isBusy: isSummaryActionBusy,
            perform: performSummaryAction
        )

        if canExportMarkdownDocument {
            menu.popoverTip(SummaryPDFDownloadTip(), arrowEdge: .top)
        } else {
            menu
        }
    }

    private func loadSummaryActions() async {
        do {
            let response = try await api.discussionUIActions(id: discussionID,
                                                             surface: "summary-actions",
                                                             docType: docType)
            actionItems = response.items
        } catch {
            actionItems = []
        }
    }

    private func performSummaryAction(_ item: DiscussionUIActionItem) {
        guard let path = validatedSummaryActionPath(item) else { return }
        switch path {
        case ["summary", "select", "summary"]:
            docType = "summary"
        case ["summary", "select", "ppt"]:
            docType = "ppt"
        case ["summary", "export", "pptx"]:
            Task { await downloadPPTX() }
        case ["summary", "export", "slides-pdf"]:
            Task { await downloadSlidesPDF() }
        case ["summary", "export", "pdf"]:
            Task { await downloadPDF() }
        case ["summary", "export", "markdown"]:
            downloadMarkdown()
        case ["summary", "sheet", "notion"]:
            showingNotionExport = true
        default:
            break
        }
    }

    private func validatedSummaryActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "discussion" else { return nil }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == discussionID else { return nil }
        return Array(components.dropFirst())
    }

    private func isSummaryActionBusy(_ item: DiscussionUIActionItem) -> Bool {
        switch item.id {
        case "download-pptx":
            return isPreparingPPTX
        case "download-slides-pdf":
            return isPreparingSlidesPDF
        case "download-pdf":
            return isPreparingPDF
        default:
            return false
        }
    }

    /// Fetches the server-rendered PDF and hands it to the share sheet. PDF
    /// rendering runs on Cloudflare and can take a few seconds; `isPreparingPDF`
    /// drives the menu label + the loading overlay meanwhile.
    private func downloadPDF() async {
        guard canExportMarkdownDocument, !isPreparingExport else { return }
        isPreparingPDF = true
        defer { isPreparingPDF = false }
        do {
            exportFile = try ExportedSummaryFile(
                url: await api.downloadSummaryPDF(id: discussionID, docType: docType, title: title)
            )
        } catch {
            exportError = "Couldn’t export the PDF. Please try again."
        }
    }

    /// Fetches the server-rendered PPTX deck and shares it.
    private func downloadPPTX() async {
        guard canExportSlideDeck, !isPreparingExport else { return }
        if isPPTDocumentSelected, let pptPreviewFile {
            exportFile = pptPreviewFile
            return
        }
        isPreparingPPTX = true
        defer { isPreparingPPTX = false }
        do {
            let file = try ExportedSummaryFile(url: await api.downloadSummaryPPTX(id: discussionID, title: title))
            exportFile = file
            pptPreviewFile = file
            isPPTDeckAvailable = true
            await loadSummaryActions()
        } catch {
            exportError = "Couldn’t export the PPTX. Please try again."
        }
    }

    /// Fetches the server-rendered slide-deck PDF and shares it.
    private func downloadSlidesPDF() async {
        guard canExportSlideDeck, !isPreparingExport else { return }
        isPreparingSlidesPDF = true
        defer { isPreparingSlidesPDF = false }
        do {
            exportFile = try ExportedSummaryFile(
                url: await api.downloadSummarySlidesPDF(id: discussionID, title: title)
            )
            isPPTDeckAvailable = true
            await loadSummaryActions()
        } catch {
            exportError = "Couldn’t export the slides PDF. Please try again."
        }
    }

    /// Writes the already-loaded Markdown body to a temp file and shares it.
    private func downloadMarkdown() {
        guard canExportMarkdownDocument, let markdown = document?.markdown, !markdown.isEmpty else { return }
        do {
            exportFile = try ExportedSummaryFile(url: api.writeSummaryMarkdown(markdown, title: title))
        } catch {
            exportError = "Couldn’t export the Markdown. Please try again."
        }
    }

    /// Dimmed HUD shown while the server renders an export.
    private var exportPreparingOverlay: some View {
        ZStack {
            Color.black.opacity(0.25).ignoresSafeArea()
            VStack(spacing: 12) {
                ProgressView()
                Text(preparingExportTitle)
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            .padding(24)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
        }
    }

    private func load() async {
        isLoading = true
        loadError = nil
        if isPPTDocumentSelected {
            await loadPPTXPreview()
            return
        }
        pptPreviewFile = nil
        do {
            let loaded = try await api.summary(id: discussionID, docType: docType)
            logRawMarkdownForDebug(loaded.markdown, docType: docType, source: "SummaryView.load")
            document = loaded
            isLoading = false
            if isSummaryDocumentSelected, !loaded.markdown.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                await refreshPPTDeckAvailability()
            } else if isPPTDocumentSelected {
                isPPTDeckAvailable = loaded.status == .ready
                    && !loaded.markdown.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            } else {
                isPPTDeckAvailable = false
            }
            return
        } catch {
            document = nil
            isPPTDeckAvailable = false
            loadError = "Couldn’t load the summary. Please try again."
            if isPPTDocumentSelected {
                docType = "summary"
            }
        }
        isLoading = false
    }

    private func loadPPTXPreview() async {
        document = nil
        do {
            let file = try ExportedSummaryFile(url: await api.downloadSummaryPPTX(id: discussionID, title: title))
            pptPreviewFile = file
            isPPTDeckAvailable = true
        } catch {
            pptPreviewFile = nil
            isPPTDeckAvailable = false
            loadError = "Couldn’t load the PPTX. Please try again."
            docType = "summary"
        }
        isLoading = false
    }

    private func refreshPPTDeckAvailability() async {
        do {
            let deck = try await api.summary(id: discussionID, docType: "ppt")
            isPPTDeckAvailable = deck.status == .ready
                && !deck.markdown.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        } catch APIError.http(404, _) {
            isPPTDeckAvailable = false
        } catch {
            isPPTDeckAvailable = false
        }
    }

    private func logRawMarkdownForDebug(_ markdown: String, docType: String, source: String) {
        let chunkSize = 2_000
        let totalParts = max(1, (markdown.count + chunkSize - 1) / chunkSize)
        summaryViewLog.info("Raw markdown begin source=\(source, privacy: .public) discussion=\(discussionID, privacy: .public) doc_type=\(docType, privacy: .public) chars=\(markdown.count, privacy: .public) parts=\(totalParts, privacy: .public)")

        guard !markdown.isEmpty else {
            summaryViewLog.info("Raw markdown chunk source=\(source, privacy: .public) discussion=\(discussionID, privacy: .public) doc_type=\(docType, privacy: .public) part=1/1 markdown=''")
            summaryViewLog.info("Raw markdown end source=\(source, privacy: .public) discussion=\(discussionID, privacy: .public) doc_type=\(docType, privacy: .public)")
            return
        }

        var part = 1
        var index = markdown.startIndex
        while index < markdown.endIndex {
            let next = markdown.index(index, offsetBy: chunkSize, limitedBy: markdown.endIndex) ?? markdown.endIndex
            let chunk = String(markdown[index..<next])
            summaryViewLog.info("Raw markdown chunk source=\(source, privacy: .public) discussion=\(discussionID, privacy: .public) doc_type=\(docType, privacy: .public) part=\(part, privacy: .public)/\(totalParts, privacy: .public) markdown=\(chunk, privacy: .public)")
            index = next
            part += 1
        }

        summaryViewLog.info("Raw markdown end source=\(source, privacy: .public) discussion=\(discussionID, privacy: .public) doc_type=\(docType, privacy: .public)")
    }
}

private struct KingfisherMarkdownImageProvider: ImageProvider {
    func makeImage(url: URL?) -> some View {
        Group {
            if let url {
                KFImage.url(url)
                    .placeholder {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                            .frame(height: 160)
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .fade(duration: 0.15)
                    .resizable()
                    .scaledToFit()
                    .id(url.absoluteString)
            } else {
                Color.clear
                    .frame(width: 0, height: 0)
            }
        }
    }
}

/// A summary export (PDF or Markdown) sitting in a temp file, ready to hand to
/// the system share sheet.
private struct ExportedSummaryFile: Identifiable {
    let id = UUID()
    let url: URL
}

private struct SummaryPPTXPreview: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: QLPreviewController, context: Context) {
        context.coordinator.url = url
        controller.reloadData()
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(url: url)
    }

    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL

        init(url: URL) {
            self.url = url
        }

        func numberOfPreviewItems(in controller: QLPreviewController) -> Int {
            1
        }

        func previewController(_ controller: QLPreviewController,
                               previewItemAt index: Int) -> QLPreviewItem {
            url as NSURL
        }
    }
}

/// Renders one ```mermaid fenced block natively. Falls back to showing the raw
/// mermaid source as a code block if the diagram fails to parse/render.
private struct MermaidBlock: View {
    let code: String

    @State private var parseError: Error?
    @State private var bounds: CGRect = .zero

    var body: some View {
        Group {
            if parseError != nil {
                rawCode
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    MermaidDiagramView(source: code,
                                       parseError: $parseError,
                                       diagramBounds: $bounds)
                        .frame(width: max(bounds.width, 1),
                               height: bounds.height > 0 ? bounds.height : 240)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var rawCode: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(code)
                .font(.system(.callout, design: .monospaced))
                .padding(12)
        }
        .background(Color.secondary.opacity(0.12))
        .clipShape(RoundedRectangle(cornerRadius: 8))
    }
}

/// A self-contained flow for exporting the current podcast summary to Notion.
/// It ensures the workspace is connected (running the OAuth flow if not), lets
/// the user pick a parent page, creates the summary as a sub-page, then offers
/// to open the new page. Reuses the existing `APIClient` Notion endpoints; no
/// AuthManager dependency because the injected `api` already carries the token.
struct NotionExportSheet: View {
    let api: APIClient
    let discussionID: String
    var docType: String = "summary"

    @Environment(\.dismiss) private var dismiss

    @State private var phase: Phase = .checking
    @State private var query = ""
    @State private var pages: [NotionPageDTO] = []
    @State private var selectedPageID: String?
    @State private var isExporting = false
    @State private var isConnecting = false
    @State private var errorMessage: String?
    @State private var createdURL: URL?
    @State private var authSession: ASWebAuthenticationSession?
    @State private var presentationProvider = NotionExportWebAuthPresentationContextProvider()

    private enum Phase: Equatable {
        case checking
        case needsConnect
        case picking
        case done
    }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Export to Notion")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button(phase == .done ? "Done" : "Cancel") { dismiss() }
                    }
                    if phase == .picking {
                        ToolbarItemGroup(placement: .topBarTrailing) {
                            Button {
                                allowMorePages()
                            } label: {
                                Label("Allow Access to More Pages", systemImage: "folder.badge.plus")
                            }
                            .disabled(isConnecting || isExporting)
                        }
                        ToolbarItem(placement: .confirmationAction) {
                            Button(isExporting ? "Exporting…" : "Export") {
                                Task { await export() }
                            }
                            .disabled(isExporting)
                        }
                    }
                }
                .task { await loadStatus() }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch phase {
        case .checking:
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .needsConnect:
            connectState
        case .picking:
            pickerList
        case .done:
            doneState
        }
    }

    private var connectState: some View {
        VStack(spacing: 16) {
            Image(systemName: "link.circle")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
            Text("Connect your Notion workspace to export this summary.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }
            Button("Connect Notion") { connect() }
                .buttonStyle(.borderedProminent)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var pickerList: some View {
        List {
            if let errorMessage {
                Text(errorMessage)
                    .font(.footnote)
                    .foregroundStyle(.red)
            }
            Section {
                ForEach(pages) { page in
                    Button { toggle(page) } label: { pageRow(page) }
                }
            } header: {
                Text("Choose a parent page")
            } footer: {
                Text("Select a page to export inside it, or leave it empty to export at the root.")
            }
        }
        .searchable(text: $query, prompt: "Search pages")
        .task(id: query) { await search() }
    }

    @ViewBuilder
    private func pageRow(_ page: NotionPageDTO) -> some View {
        let selected = selectedPageID == page.id
        HStack(spacing: 12) {
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(selected ? Color.accentColor : Color.secondary)
            VStack(alignment: .leading, spacing: 3) {
                Text(page.title.isEmpty ? "Untitled" : page.title)
                    .font(.body.weight(.medium))
                    .foregroundStyle(.primary)
                if let url = page.url, !url.isEmpty {
                    Text(url)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer()
        }
    }

    private func toggle(_ page: NotionPageDTO) {
        selectedPageID = (selectedPageID == page.id) ? nil : page.id
    }

    private var doneState: some View {
        VStack(spacing: 16) {
            Image(systemName: "checkmark.circle.fill")
                .font(.largeTitle)
                .foregroundStyle(.green)
            Text("Summary exported to Notion.")
                .font(.headline)
            if let createdURL {
                Link(destination: createdURL) {
                    Label("Open in Notion", systemImage: "arrow.up.right.square")
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func loadStatus() async {
        do {
            let status = try await api.notionStatus()
            if status.connected {
                phase = .picking
            } else {
                phase = .needsConnect
            }
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            phase = .needsConnect
        }
    }

    private func search() async {
        let currentQuery = query
        errorMessage = nil
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        do {
            let result = try await api.searchNotionPages(query: currentQuery)
            guard !Task.isCancelled, currentQuery == query else { return }
            pages = result
        } catch {
            guard !Task.isCancelled, currentQuery == query else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func connect() {
        errorMessage = nil
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        authSession = nil
                        guard error == nil else { return }
                        await loadStatus()
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                authSession = session
                session.start()
            } catch {
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func export() async {
        await export(parentPageID: selectedPageID)
    }

    private func export(parentPageID: String?) async {
        guard !isExporting else { return }
        isExporting = true
        errorMessage = nil
        defer { isExporting = false }
        do {
            let resp = try await api.exportSummaryToNotion(id: discussionID,
                                                           parentPageID: parentPageID,
                                                           docType: docType)
            createdURL = URL(string: resp.url)
            phase = .done
        } catch {
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func allowMorePages() {
        guard !isConnecting else { return }
        isConnecting = true
        errorMessage = nil
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        authSession = nil
                        isConnecting = false
                        guard error == nil else { return }
                        if phase == .picking {
                            await search()
                        } else {
                            await loadStatus()
                        }
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                authSession = session
                session.start()
            } catch {
                isConnecting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

@MainActor
private final class NotionExportWebAuthPresentationContextProvider: NSObject, ASWebAuthenticationPresentationContextProviding {
    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        let scene = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .first { $0.activationState == .foregroundActive }
        return scene?.keyWindow ?? ASPresentationAnchor()
    }
}
