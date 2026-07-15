import AuthenticationServices
import BeautifulMermaid
import Kingfisher
import MarkdownUI
import QuickLook
import SwiftUI
import TipKit
import os

struct MermaidBlock: View {
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


