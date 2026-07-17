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
    var language: String? = nil
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var docType: String
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

    init(discussionID: String,
         title: String = "Summary",
         mindmapEditable: Bool = false,
         language: String? = nil,
         initialDocType: String = "summary",
         api: APIClient) {
        self.discussionID = discussionID
        self.title = title
        self.mindmapEditable = mindmapEditable
        self.language = language
        self.api = api
        _docType = State(initialValue: initialDocType == "ppt" ? "ppt" : "summary")
    }

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
                    NotionExportSheet(api: api, discussionID: discussionID,
                                      docType: docType, language: language)
                }
                .sheet(isPresented: $showingMindmap) {
                    MindmapView(discussionID: discussionID,
                                title: title,
                                isEditable: mindmapEditable && language == nil,
                                language: language,
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
                url: await api.downloadSummaryPDF(id: discussionID, docType: docType,
                                                  title: title, language: language)
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
            let loaded = try await api.summary(id: discussionID, docType: docType, language: language)
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
