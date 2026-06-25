import BeautifulMermaid
import MarkdownUI
import SwiftUI

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

    /// The temp file (PDF or Markdown) to hand to the system export sheet.
    @State private var exportFile: ExportedSummaryFile?
    @State private var isPreparingPDF = false
    @State private var exportError: String?

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
                    SummaryDocumentExporter(url: file.url)
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
    private var documentTypeMenu: some View {
        Menu {
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
        } label: {
            Image(systemName: "ellipsis.circle")
        }
    }

    /// Fetches the server-rendered PDF and hands it to the export sheet. PDF
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

    /// Writes the already-loaded Markdown body to a temp file and exports it.
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
/// the system export sheet.
private struct ExportedSummaryFile: Identifiable {
    let id = UUID()
    let url: URL
}

/// Presents the iOS "save to Files / share" picker for an exported summary file.
private struct SummaryDocumentExporter: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forExporting: [url], asCopy: true)
        picker.shouldShowFileExtensions = true
        return picker
    }

    func updateUIViewController(_ uiViewController: UIDocumentPickerViewController, context: Context) {}
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
