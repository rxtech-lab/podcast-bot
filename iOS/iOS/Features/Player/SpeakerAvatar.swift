import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
import UIKit
import UniformTypeIdentifiers
import os

struct SpeakerAvatar: View {
    let speaker: String
    var color: Color? = nil
    var size: CGFloat = 32

    var body: some View {
        let color = color ?? SpeakerPalette.color(for: speaker)
        Circle()
            .fill(LinearGradient(
                colors: [color.opacity(0.95), color.opacity(0.55)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            ))
            .frame(width: size, height: size)
            .overlay {
                Text(SpeakerPalette.initials(for: speaker))
                    .font(.system(size: size * 0.4, weight: .bold))
                    .foregroundStyle(.white)
            }
            .overlay {
                Circle().strokeBorder(.white.opacity(0.18), lineWidth: 0.5)
            }
    }
}

/// One transcript message: the current user's own turns sit right in an accent
/// bubble (mockup image 4); everyone else — AI panelists *and* other human
/// participants — render left with an avatar + name header in their own color,
/// so a co-listener's comment reads as a distinct speaker rather than as my own
/// message. `isMine` (not `line.isUser`) drives this: other users also persist
/// with `isUser == true`, so the flag alone can't tell them apart from me.
