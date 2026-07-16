import SwiftUI

struct PlanningTypingDots: View {
    @State var isAnimating = false

    var body: some View {
        HStack(spacing: 5) {
            ForEach(0 ..< 3, id: \.self) { index in
                Circle()
                    .fill(Theme.secondaryText)
                    .frame(width: 7, height: 7)
                    .scaleEffect(isAnimating ? 1 : 0.55)
                    .opacity(isAnimating ? 1 : 0.3)
                    .animation(
                        .easeInOut(duration: 0.6)
                            .repeatForever(autoreverses: true)
                            .delay(Double(index) * 0.18),
                        value: isAnimating
                    )
            }
        }
        // No oversized frame: the dots size to their intrinsic 7pt height so the
        // bubble's padding centers them. A taller frame leaves vertical slack the
        // dots ride to the top of, making them spill out of the bubble.
        .onAppear { isAnimating = true }
    }
}
