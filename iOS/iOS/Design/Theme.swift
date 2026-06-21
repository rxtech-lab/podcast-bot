import SwiftUI

/// Centralized colors + Liquid Glass helpers, matching the dark mockups with a
/// purple accent.
enum Theme {
    static let accent = Color(red: 0.49, green: 0.31, blue: 0.96)   // vivid purple
    static let background = Color.black
    static let agentBubble = Color.white.opacity(0.07)
    static let secondaryText = Color.white.opacity(0.6)
}

extension View {
    /// A Liquid Glass card: padded content over a glass-rendered rounded rect.
    func glassCard(cornerRadius: CGFloat = 22) -> some View {
        self
            .padding(16)
            .glassEffect(in: .rect(cornerRadius: cornerRadius))
    }

    /// A Liquid Glass capsule chip.
    func glassChip() -> some View {
        self
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .glassEffect(in: .capsule)
    }
}
