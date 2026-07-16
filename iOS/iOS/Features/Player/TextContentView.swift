import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

struct TextContentView: View {
    let discussionID: String
    let title: String
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var markdown: String = ""
    @State private var isLoading = true
    @State private var errorText: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if let errorText {
                    ContentUnavailableView(
                        "Text unavailable",
                        systemImage: "book.closed",
                        description: Text(errorText)
                    )
                } else {
                    ScrollView {
                        Markdown(markdown)
                            .markdownImageProvider(TextContentMarkdownImageProvider())
                            .padding()
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await loadText() }
        }
    }

    private func loadText() async {
        isLoading = true
        errorText = nil
        do {
            let doc = try await api.summary(id: discussionID, docType: "text")
            logRawMarkdownForDebug(doc.markdown)
            markdown = doc.markdown
        } catch {
            errorText = error.localizedDescription
        }
        isLoading = false
    }

    private func logRawMarkdownForDebug(_ markdown: String) {
        let chunkSize = 2_000
        let totalParts = max(1, (markdown.count + chunkSize - 1) / chunkSize)
        textContentLog.info("Raw markdown begin source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text chars=\(markdown.count, privacy: .public) parts=\(totalParts, privacy: .public)")

        guard !markdown.isEmpty else {
            textContentLog.info("Raw markdown chunk source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text part=1/1 markdown=''")
            textContentLog.info("Raw markdown end source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text")
            return
        }

        var part = 1
        var index = markdown.startIndex
        while index < markdown.endIndex {
            let next = markdown.index(index, offsetBy: chunkSize, limitedBy: markdown.endIndex) ?? markdown.endIndex
            let chunk = String(markdown[index..<next])
            textContentLog.info("Raw markdown chunk source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text part=\(part, privacy: .public)/\(totalParts, privacy: .public) markdown=\(chunk, privacy: .public)")
            index = next
            part += 1
        }

        textContentLog.info("Raw markdown end source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text")
    }
}
