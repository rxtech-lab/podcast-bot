import SwiftUI
import MarkdownUI
import BeautifulMermaid

/// Displays a finished podcast's generated summary document. The Markdown body
/// is fetched only when this view mounts (the podcast detail never carries it),
/// rendered with swift-markdown-ui (non-streaming), and any ```mermaid fenced
/// block is drawn natively with beautiful-mermaid-swift.
///
/// A toolbar picker selects the document type — today only "Summary document";
/// slide-deck / other kinds are reserved for the future.
struct SummaryView: View {
    let discussionID: String
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var docType = "summary"
    @State private var document: SummaryDocument?
    @State private var isLoading = true
    @State private var loadError: String?

    var body: some View {
        NavigationStack {
            content
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

    /// Document-type picker. Only "Summary document" is selectable today; the
    /// slide-deck option is shown disabled to signal future support.
    private var documentTypeMenu: some View {
        Menu {
            Picker("Document type", selection: $docType) {
                Label("Summary document", systemImage: "doc.richtext").tag("summary")
            }
            Button {
            } label: {
                Label("Slides (coming soon)", systemImage: "rectangle.on.rectangle")
            }
            .disabled(true)
        } label: {
            Image(systemName: "ellipsis.circle")
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
