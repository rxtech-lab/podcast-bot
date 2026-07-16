import SwiftUI

/// A minimal sound-reactive pulse: a solid accent circle surrounded by two
/// plain translucent circles that expand as the input level rises. In the
/// middle, four rounded bars bounce independently with the voice (assistant
/// style); while paused they are replaced by the pause glyph.
struct CircularWaveformView: View {
    /// Normalized 0...1 microphone level.
    var level: CGFloat
    /// True while actively recording (drives the motion and the glyph).
    var isActive: Bool

    /// Smoothed level so the pulse eases instead of jittering.
    @State private var smoothed: CGFloat = 0

    var body: some View {
        ZStack {
            Circle()
                .fill(Theme.accent.opacity(0.12))
                .scaleEffect(0.62 + smoothed * 0.38)
            Circle()
                .fill(Theme.accent.opacity(0.2))
                .scaleEffect(0.58 + smoothed * 0.22)
            Circle()
                .fill(Theme.accent)
                .scaleEffect(0.54 + smoothed * 0.08)
            if isActive {
                SoundBars(level: smoothed)
                    .transition(.opacity.combined(with: .scale(scale: 0.6)))
            } else {
                Image(systemName: "pause.fill")
                    .font(.system(size: 30, weight: .semibold))
                    .foregroundStyle(.white)
                    .transition(.opacity.combined(with: .scale(scale: 0.6)))
            }
        }
        .animation(.easeOut(duration: 0.2), value: isActive)
        .onChange(of: level) { _, newValue in
            let target = isActive ? newValue : 0
            withAnimation(.easeOut(duration: 0.12)) {
                smoothed = smoothed * 0.6 + target * 0.4
            }
        }
        .onChange(of: isActive) { _, active in
            if !active {
                withAnimation(.easeOut(duration: 0.4)) { smoothed = 0 }
            }
        }
    }
}

/// Four rounded bars bouncing out of phase; the voice level sets both how
/// tall and how fast the bounces get, and in silence they rest as small dots
/// swaying slowly.
private struct SoundBars: View {
    /// Smoothed 0...1 level driving the bounce.
    var level: CGFloat

    @State private var clock = BarClock()

    var body: some View {
        TimelineView(.animation) { timeline in
            let phases = clock.advance(to: timeline.date.timeIntervalSinceReferenceDate,
                                       level: level)
            HStack(spacing: 7) {
                ForEach(0 ..< 4) { i in
                    Capsule()
                        .fill(.white)
                        .frame(width: 9, height: height(phase: phases[i]))
                }
            }
        }
        .frame(height: 48)
    }

    private func height(phase: Double) -> CGFloat {
        let bounce = abs(sin(phase))
        // A whisper of motion remains in silence so the bars feel alive.
        let amplitude = 4 + 34 * level
        return 10 + amplitude * CGFloat(0.25 + 0.75 * bounce)
    }
}

/// Integrates each bar's bounce phase frame by frame, so the bounce speed can
/// follow the voice level without the bars ever jumping: quiet input barely
/// advances the phase, loud input makes it race.
private final class BarClock {
    private var phases: [Double] = [0, 2.1, 4.2, 1.1]
    private var lastTime: TimeInterval?
    private let speeds: [Double] = [11.0, 13.6, 12.2, 10.4]

    func advance(to t: TimeInterval, level: CGFloat) -> [Double] {
        // Clamp dt so a hiccuped frame doesn't teleport the bars.
        let dt = min(max(t - (lastTime ?? t), 0), 0.1)
        lastTime = t
        let rate = 0.1 + 0.9 * Double(level)
        for i in phases.indices {
            phases[i] += dt * speeds[i] * rate
        }
        return phases
    }
}

#Preview {
    CircularWaveformView(level: 0.6, isActive: true)
        .frame(width: 300, height: 300)
        .background(Theme.background)
}
