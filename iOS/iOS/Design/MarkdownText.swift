import SwiftUI
import SwiftStreamingMarkdown

/// All markdown rendering goes through here so the SwiftStreamingMarkdown call
/// site is isolated to one file.
///
/// `MarkdownText` renders a string with SwiftStreamingMarkdown's `MarkdownView`.
/// In-flight transcript bubbles just feed it the growing snapshot, so it
/// re-renders live as tokens arrive. To get the library's token-level animation,
/// swap the body for `StreamedMarkdownView` once its source API is confirmed.
///
/// NOTE: If the public view is named differently than `MarkdownView(text:)`,
/// adjust it here only — every screen renders markdown through this view.
struct MarkdownText: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        MarkdownView(text: text)
    }
}
