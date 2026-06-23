import SwiftUI

/// Paged "What's New" carousel. One card per unseen feature, ending in a
/// "Got it" button. Non-dismissable: the user must tap through.
struct WhatsNewSheet: View {
    let features: [WhatsNewFeature]
    /// When false (the launch-flow default) the sheet can't be swiped away. Set
    /// true when shown on demand (e.g. from the library menu) so the user can
    /// dismiss it freely.
    var allowsInteractiveDismiss: Bool = false
    var onContinue: () -> Void

    @State private var index = 0

    private var isLastCard: Bool { index >= features.count - 1 }

    var body: some View {
        VStack(spacing: 24) {
            Text("What's New")
                .font(.title2.bold())
                .padding(.top, 40)

            TabView(selection: $index) {
                ForEach(Array(features.enumerated()), id: \.element.id) { offset, feature in
                    VStack(spacing: 20) {
                        Image(systemName: feature.icon)
                            .font(.system(size: 64))
                            .foregroundStyle(Theme.accent)
                        Text(feature.title)
                            .font(.title2.bold())
                            .multilineTextAlignment(.center)
                        Text(feature.subtitle)
                            .font(.body)
                            .foregroundStyle(Theme.secondaryText)
                            .multilineTextAlignment(.center)
                    }
                    .padding(.horizontal, 32)
                    .tag(offset)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: features.count > 1 ? .always : .never))

            Button {
                if isLastCard {
                    onContinue()
                } else {
                    withAnimation { index += 1 }
                }
            } label: {
                Text(isLastCard ? "Got it" : "Next")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.borderedProminent)
            .tint(Theme.accent)
            .padding(.horizontal, 32)
            .padding(.bottom, 24)
        }
        .interactiveDismissDisabled(!allowsInteractiveDismiss)
        .presentationDragIndicator(allowsInteractiveDismiss ? .visible : .hidden)
    }
}
