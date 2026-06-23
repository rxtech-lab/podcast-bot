import SwiftUI

/// First-launch welcome carousel. Paged intro slides ending in a "Get started"
/// button. Non-dismissable: the user must tap through.
struct WelcomeSheet: View {
    var onContinue: () -> Void

    @State private var index = 0

    private struct Slide: Identifiable {
        let id = UUID()
        let icon: String
        let title: LocalizedStringKey
        let subtitle: LocalizedStringKey
    }

    private let slides: [Slide] = [
        Slide(icon: "waveform.circle.fill",
              title: "Welcome to \(AppStringLiteral.appTitleRaw)",
              subtitle: "Plan a \(AppStringLiteral.stationNameRaw), generate it, and listen with synced captions."),
        Slide(icon: "person.3.fill",
              title: "Pick your speakers",
              subtitle: "Choose how many people debate, the language, and the topic — we handle the rest."),
        Slide(icon: "play.circle.fill",
              title: "Listen live",
              subtitle: "Follow along with a per-speaker transcript while the episode is generated in real time."),
    ]

    private var isLastSlide: Bool { index >= slides.count - 1 }

    var body: some View {
        VStack(spacing: 24) {
            TabView(selection: $index) {
                ForEach(Array(slides.enumerated()), id: \.element.id) { offset, slide in
                    VStack(spacing: 20) {
                        Image(systemName: slide.icon)
                            .font(.system(size: 72))
                            .foregroundStyle(Theme.accent)
                        Text(slide.title)
                            .font(.title.bold())
                            .multilineTextAlignment(.center)
                        Text(slide.subtitle)
                            .font(.body)
                            .foregroundStyle(Theme.secondaryText)
                            .multilineTextAlignment(.center)
                    }
                    .padding(.horizontal, 32)
                    .tag(offset)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .always))

            Button {
                if isLastSlide {
                    onContinue()
                } else {
                    withAnimation { index += 1 }
                }
            } label: {
                Text(isLastSlide ? "Get started" : "Next")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.borderedProminent)
            .tint(Theme.accent)
            .padding(.horizontal, 32)
            .padding(.bottom, 24)
        }
        .padding(.top, 40)
        .interactiveDismissDisabled(true)
        .presentationDragIndicator(.hidden)
    }
}
