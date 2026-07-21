import SwiftUI
#if os(macOS)
import MarkdownUI
#else
import SwiftStreamingMarkdown
#endif

/// All markdown rendering goes through here so the markdown library call
/// site is isolated to one file.
///
/// On iOS/visionOS, `MarkdownText` renders with SwiftStreamingMarkdown's
/// `MarkdownView`. In-flight transcript bubbles just feed it the growing
/// snapshot, so it re-renders live as tokens arrive.
///
/// On macOS, SwiftStreamingMarkdown is unavailable (it links UIKit-only
/// dependencies), so rendering falls back to MarkdownUI's `Markdown`.
struct MarkdownText: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        #if os(macOS)
        Markdown(text)
        #else
        MarkdownView(text: text)
        #endif
    }
}
