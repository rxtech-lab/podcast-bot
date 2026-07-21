import Kingfisher
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct IdleSleepPreventionModifier: ViewModifier {
    @State private var token = UUID()
    @State private var isActive = false

    func body(content: Content) -> some View {
        content
            .onAppear(perform: begin)
            .onDisappear(perform: end)
    }

    private func begin() {
        guard !isActive else { return }
        isActive = true
        IdleSleepPrevention.shared.begin(token: token)
    }

    private func end() {
        guard isActive else { return }
        isActive = false
        IdleSleepPrevention.shared.end(token: token)
    }
}


extension View {
    func preventsIdleSleep() -> some View {
        modifier(IdleSleepPreventionModifier())
    }
}

/// Scrubber + elapsed/remaining labels. Mirrors the mini-bar slider logic but
/// fills the full width; falls back to a progress bar while streaming.


