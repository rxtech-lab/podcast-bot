import SwiftUI

/// Paged "What's New" carousel. Short updates show one feature per page, while
/// updates with more than four features use compact multi-feature pages.
/// Non-dismissable in the launch flow: the user must tap through.
struct WhatsNewSheet: View {
    let features: [WhatsNewFeature]
    /// When false (the launch-flow default) the sheet can't be swiped away. Set
    /// true when shown on demand (e.g. from the library menu) so the user can
    /// dismiss it freely.
    var allowsInteractiveDismiss: Bool = false
    var onContinue: () -> Void

    @State private var index = 0

    private var pages: [[WhatsNewFeature]] {
        WhatsNewFeature.presentationPages(for: features)
    }

    private var isLastPage: Bool { index >= pages.count - 1 }

    var body: some View {
        VStack(spacing: 24) {
            Text("What's New")
                .font(.title2.bold())
                .padding(.top, 40)

            TabView(selection: $index) {
                ForEach(Array(pages.enumerated()), id: \.offset) { offset, page in
                    featurePage(page)
                        .tag(offset)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: pages.count > 1 ? .always : .never))

            Button {
                if isLastPage {
                    onContinue()
                } else {
                    withAnimation { index += 1 }
                }
            } label: {
                Text(isLastPage ? "Got it" : "Next")
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

    @ViewBuilder
    private func featurePage(_ features: [WhatsNewFeature]) -> some View {
        if features.count == 1, let feature = features.first {
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
        } else {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 24) {
                    ForEach(features) { feature in
                        HStack(alignment: .top, spacing: 16) {
                            Image(systemName: feature.icon)
                                .font(.system(size: 30))
                                .foregroundStyle(Theme.accent)
                                .frame(width: 40)

                            VStack(alignment: .leading, spacing: 4) {
                                Text(feature.title)
                                    .font(.headline)
                                Text(feature.subtitle)
                                    .font(.subheadline)
                                    .foregroundStyle(Theme.secondaryText)
                            }
                        }
                    }
                }
                .padding(.horizontal, 32)
                .padding(.vertical, 12)
            }
        }
    }
}

#if DEBUG
#Preview("What's New · Multiple Features") {
    WhatsNewSheet(
        features: Array(WhatsNewFeature.all.prefix(5)),
        allowsInteractiveDismiss: true
    ) {}
}
#endif
