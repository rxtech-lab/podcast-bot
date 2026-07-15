import SwiftUI

struct PlanningCardAppearModifier: ViewModifier {
    @Environment(\.accessibilityReduceMotion) var reduceMotion
    @State var isVisible = false
    let delay: Double

    func body(content: Content) -> some View {
        content
            .opacity(isVisible ? 1 : 0)
            .scaleEffect(reduceMotion ? 1 : (isVisible ? 1 : 0.97), anchor: .topLeading)
            .offset(y: reduceMotion ? 0 : (isVisible ? 0 : 10))
            .transition(.asymmetric(
                insertion: .opacity.combined(with: .move(edge: .bottom)),
                removal: .opacity
            ))
            .onAppear {
                guard !isVisible else { return }
                if reduceMotion {
                    isVisible = true
                } else {
                    withAnimation(.spring(response: 0.36, dampingFraction: 0.84).delay(delay)) {
                        isVisible = true
                    }
                }
            }
    }
}

extension View {
    func planningCardAppear(delay: Double = 0) -> some View {
        modifier(PlanningCardAppearModifier(delay: delay))
    }
}

/// One row in the planning conversation: a persisted/streaming part, or the
/// transient loading accessory shown while the agent works.
