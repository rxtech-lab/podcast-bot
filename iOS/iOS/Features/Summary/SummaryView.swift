import AuthenticationServices
import BeautifulMermaid
import MarkdownUI
import SwiftUI
import TipKit

/// Displays a finished podcast's generated summary document. The Markdown body
/// is fetched only when this view mounts (the podcast detail never carries it),
/// rendered with swift-markdown-ui (non-streaming), and any ```mermaid fenced
/// block is drawn natively with beautiful-mermaid-swift.
///
/// A toolbar picker selects the document type — today only "Summary document";
/// slide-deck / other kinds are reserved for the future.
struct SummaryView: View {
    let discussionID: String
    /// Used only to name the exported PDF / Markdown file; defaults to "Summary".
    var title: String = "Summary"
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var docType = "summary"
    @State private var document: SummaryDocument?
    @State private var isLoading = true
    @State private var loadError: String?

    /// The temp file (PDF or Markdown) to hand to the system share sheet.
    @State private var exportFile: ExportedSummaryFile?
    @State private var isPreparingPDF = false
    @State private var exportError: String?
    @State private var showingNotionExport = false

    private var canExport: Bool {
        guard let document else { return false }
        return !document.markdown.isEmpty
    }

    var body: some View {
        NavigationStack {
            content
                .overlay {
                    if isPreparingPDF {
                        pdfPreparingOverlay
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
                .alert("Couldn’t export", isPresented: Binding(
                    get: { exportError != nil },
                    set: { if !$0 { exportError = nil } }
                )) {
                    Button("OK", role: .cancel) { exportError = nil }
                } message: {
                    Text(exportError ?? "")
                }
                .task(id: docType) { await load() }
        }
    }

    @ViewBuilder
    private var content: some View {
        if isLoading {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if let document, !document.markdown.isEmpty {
            ScrollView {
                Markdown(document.markdown)
                    .markdownBlockStyle(\.codeBlock) { configuration in
                        if configuration.language?.lowercased() == "mermaid" {
                            MermaidBlock(code: configuration.content)
                        } else {
                            configuration.label
                        }
                    }
                    .padding()
            }
        } else {
            emptyState
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

    /// Document-type picker plus the download actions. Only "Summary document"
    /// is selectable today; the slide-deck option is shown disabled to signal
    /// future support. Downloads are enabled once the Markdown body has loaded.
    @ViewBuilder
    private var documentTypeMenu: some View {
        let menu = Menu {
            Picker("Document type", selection: $docType) {
                Label("Summary document", systemImage: "doc.richtext").tag("summary")
            }
            Button {} label: {
                Label("Slides (coming soon)", systemImage: "rectangle.on.rectangle")
            }
            .disabled(true)

            Button {
                Task { await downloadPDF() }
            } label: {
                Label(isPreparingPDF ? "Preparing PDF…" : "Download PDF",
                      systemImage: isPreparingPDF ? "hourglass" : "arrow.down.doc")
            }
            .disabled(!canExport || isPreparingPDF)

            Button {
                downloadMarkdown()
            } label: {
                Label("Download Markdown", systemImage: "arrow.down.doc.fill")
            }
            .disabled(!canExport)

            Button {
                showingNotionExport = true
            } label: {
                Label("Export to Notion", systemImage: "square.and.arrow.up.on.square")
            }
            .disabled(!canExport)
        } label: {
            Image(systemName: "ellipsis.circle")
        }

        if canExport {
            menu.popoverTip(SummaryPDFDownloadTip(), arrowEdge: .top)
        } else {
            menu
        }
    }

    /// Fetches the server-rendered PDF and hands it to the share sheet. PDF
    /// rendering runs on Cloudflare and can take a few seconds; `isPreparingPDF`
    /// drives the menu label + the loading overlay meanwhile.
    private func downloadPDF() async {
        guard canExport, !isPreparingPDF else { return }
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

    /// Writes the already-loaded Markdown body to a temp file and shares it.
    private func downloadMarkdown() {
        guard let markdown = document?.markdown, !markdown.isEmpty else { return }
        do {
            exportFile = try ExportedSummaryFile(url: api.writeSummaryMarkdown(markdown, title: title))
        } catch {
            exportError = "Couldn’t export the Markdown. Please try again."
        }
    }

    /// Dimmed HUD shown while the server renders the PDF.
    private var pdfPreparingOverlay: some View {
        ZStack {
            Color.black.opacity(0.25).ignoresSafeArea()
            VStack(spacing: 12) {
                ProgressView()
                Text("Preparing PDF…")
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
        do {
            document = try await api.summary(id: discussionID, docType: docType)
        } catch {
            document = nil
            loadError = "Couldn’t load the summary. Please try again."
        }
        isLoading = false
    }
}

/// A summary export (PDF or Markdown) sitting in a temp file, ready to hand to
/// the system share sheet.
private struct ExportedSummaryFile: Identifiable {
    let id = UUID()
    let url: URL
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
