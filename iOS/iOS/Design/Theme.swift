import SwiftUI
import UIKit

/// Centralized adaptive colors + Liquid Glass helpers.
enum Theme {
    static let accent = Color(red: 0.49, green: 0.31, blue: 0.96)   // vivid purple
    static let background = Color(uiColor: .systemBackground)
    static let agentBubble = Color(uiColor: .secondarySystemFill)
    static let rowBackground = Color(uiColor: .secondarySystemBackground)
    static let divider = Color(uiColor: .separator)
    static let secondaryText = Color.secondary
}

extension View {
    /// A Liquid Glass card: padded content over a glass-rendered rounded rect.
    /// Pass `tint` to softly color the glass (e.g. to mark a selected row).
    func glassCard(cornerRadius: CGFloat = 22, tint: Color? = nil) -> some View {
        let glass: Glass = tint.map { .regular.tint($0) } ?? .regular
        return self
            .padding(16)
            .glassEffect(glass, in: .rect(cornerRadius: cornerRadius))
    }

    /// A Liquid Glass capsule chip.
    func glassChip() -> some View {
        self
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .glassEffect(in: .capsule)
    }
}
