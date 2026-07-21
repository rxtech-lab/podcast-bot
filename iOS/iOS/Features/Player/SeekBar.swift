import Kingfisher
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

struct SeekBar: View {
    @Bindable var model: PlayerModel
    let foregroundPalette: FullScreenForegroundPalette
    /// Lets the landscape chrome pause its auto-hide timer mid-drag.
    var onScrubbingChanged: ((Bool) -> Void)? = nil
    @State private var isScrubbing = false
    @State private var scrubTime = 0.0

    var body: some View {
        VStack(spacing: 6) {
            if model.canSeek {
                Slider(value: Binding(
                    get: { isScrubbing ? scrubTime : model.currentTime },
                    set: { value in
                        scrubTime = value
                        isScrubbing = true
                    }
                ), in: 0...max(model.duration, 0.1), onEditingChanged: { editing in
                    if editing {
                        scrubTime = model.currentTime
                        isScrubbing = true
                    } else {
                        model.seek(to: scrubTime)
                        isScrubbing = false
                    }
                    onScrubbingChanged?(editing)
                })
                .tint(foregroundPalette.accent)
            } else {
                ProgressView(value: progress)
                    .tint(foregroundPalette.accent)
            }
            HStack {
                Text(timeString(displayTime)).font(.caption2).foregroundStyle(foregroundPalette.secondary)
                Spacer()
                Text(timeString(progressDuration)).font(.caption2).foregroundStyle(foregroundPalette.secondary)
            }
        }
    }

    private var progress: Double {
        guard progressDuration > 0 else { return 0 }
        return min(1, progressTime / progressDuration)
    }

    private var progressTime: Double {
        if model.duration > 0 { return model.currentTime }
        return max(model.currentTime, model.elapsedTime)
    }

    private var displayTime: Double { isScrubbing ? scrubTime : progressTime }

    private var progressDuration: Double {
        if model.duration > 0 { return model.duration }
        let estimatedTotal = model.elapsedTime + model.remainingTime
        return estimatedTotal > 0 ? estimatedTotal : 0
    }

    private func timeString(_ s: Double) -> String {
        guard s.isFinite, s >= 0 else { return "0:00" }
        let total = Int(s)
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}

/// Apple-Music-style lyrics: the full caption list, current line highlighted and
/// auto-scrolled in sync with playback. Tap a line to seek to it.


