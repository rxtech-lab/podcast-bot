import Kingfisher
import SwiftUI
import TipKit
import UIKit

struct LyricLine: View {
    let group: LyricCueGroup
    let speaker: String
    let isActive: Bool
    let foregroundPalette: FullScreenForegroundPalette

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !speaker.isEmpty {
                Text(speaker.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(isActive ? foregroundPalette.accent : foregroundPalette.secondary)
            }
            Text(group.text)
                .font(.title3.weight(.semibold))
                .foregroundStyle(isActive ? foregroundPalette.primary : foregroundPalette.secondary)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
        }
        .opacity(isActive ? 1 : 0.55)
        .scaleEffect(isActive ? 1.0 : 0.98, anchor: .leading)
        .animation(.spring(duration: 0.3), value: isActive)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}


