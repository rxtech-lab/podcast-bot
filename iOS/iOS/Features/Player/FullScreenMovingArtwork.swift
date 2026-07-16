import Kingfisher
import SwiftUI
import TipKit
import UIKit

struct FullScreenMovingArtwork<Content: View>: View {
    let isPlaying: Bool
    @ViewBuilder var content: () -> Content

    @State private var phaseStartDate: Date?
    @State private var pausedPhase = 0.0

    private let duration: TimeInterval = 22
    private let minScale = 1.1
    private let maxScale = 1.16

    var body: some View {
        TimelineView(.animation) { timeline in
            GeometryReader { geo in
                let progress = mirroredProgress(for: phase(at: timeline.date))
                content()
                    .frame(width: geo.size.width, height: geo.size.height)
                    .scaleEffect(minScale + (maxScale - minScale) * progress)
                    .offset(
                        x: lerp(geo.size.width * 0.018, -geo.size.width * 0.018, progress),
                        y: lerp(-geo.size.height * 0.012, geo.size.height * 0.012, progress)
                    )
                    .frame(width: geo.size.width, height: geo.size.height)
                    .clipped()
            }
        }
        .onAppear(perform: resetMotion)
        .onChange(of: isPlaying) { wasPlaying, playing in
            updatePlaybackState(wasPlaying: wasPlaying, isPlaying: playing)
        }
    }

    private func phase(at date: Date) -> Double {
        guard isPlaying, let phaseStartDate else { return pausedPhase }
        return pausedPhase + date.timeIntervalSince(phaseStartDate) / duration
    }

    private func resetMotion() {
        pausedPhase = 0
        phaseStartDate = isPlaying ? Date() : nil
    }

    private func updatePlaybackState(wasPlaying: Bool, isPlaying: Bool) {
        if isPlaying {
            phaseStartDate = Date()
        } else if wasPlaying {
            pausedPhase = normalizedRunningPhase(at: Date())
            phaseStartDate = nil
        }
    }

    private func normalizedRunningPhase(at date: Date) -> Double {
        guard let phaseStartDate else { return pausedPhase }
        return (pausedPhase + date.timeIntervalSince(phaseStartDate) / duration)
            .truncatingRemainder(dividingBy: 2)
    }

    private func mirroredProgress(for phase: Double) -> Double {
        let normalized = phase.truncatingRemainder(dividingBy: 2)
        return normalized <= 1 ? normalized : 2 - normalized
    }

    private func lerp(_ start: CGFloat, _ end: CGFloat, _ progress: Double) -> CGFloat {
        start + (end - start) * CGFloat(progress)
    }
}

/// Displays a timed audiobook illustration with hard cuts: the incoming image
/// is downloaded off-screen and swapped in only once decoded, inside a
/// transaction with animations disabled — the artwork never crossfades and
/// never flashes a placeholder between switches. The previous illustration
/// (or the cover placeholder on first load) stays up until the next one is
/// ready.


