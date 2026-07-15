import Kingfisher
import SwiftUI
import TipKit
import UIKit

struct FullScreenForegroundPalette {
    let primary: Color
    let secondary: Color
    let accent: Color

    /// Fixed light chrome for landscape, where the controls sit on full-bleed
    /// artwork behind a dark scrim regardless of the sampled cover colors.
    static let overArtwork = FullScreenForegroundPalette(
        primary: .white,
        secondary: .white.opacity(0.68),
        accent: .white
    )

    private init(primary: Color, secondary: Color, accent: Color) {
        self.primary = primary
        self.secondary = secondary
        self.accent = accent
    }

    init(backgroundColors: [Color]) {
        let luminance = Self.averageScrimmedLuminance(for: backgroundColors)
        if luminance < 0.45 {
            primary = .white
            secondary = .white.opacity(0.68)
            accent = .white
        } else {
            primary = .black
            secondary = .black.opacity(0.58)
            accent = .black
        }
    }

    private static func averageScrimmedLuminance(for colors: [Color]) -> Double {
        let source = colors.isEmpty ? [Theme.accent.opacity(0.35), Theme.background] : colors
        let values = source.compactMap(relativeLuminance)
        guard !values.isEmpty else { return 0 }
        let average = values.reduce(0, +) / Double(values.count)
        // The full-screen background adds a top-to-bottom black overlay from
        // 15% to 50%, so use the midpoint to choose a matching foreground.
        return average * 0.675
    }

    private static func relativeLuminance(for color: Color) -> Double? {
        let uiColor = UIColor(color)
        var red: CGFloat = 0
        var green: CGFloat = 0
        var blue: CGFloat = 0
        var alpha: CGFloat = 0
        guard uiColor.getRed(&red, green: &green, blue: &blue, alpha: &alpha) else { return nil }

        func linearize(_ value: CGFloat) -> Double {
            let value = Double(value)
            if value <= 0.03928 { return value / 12.92 }
            return pow((value + 0.055) / 1.055, 2.4)
        }

        return 0.2126 * linearize(red) + 0.7152 * linearize(green) + 0.0722 * linearize(blue)
    }
}

/// Slow Ken Burns motion for landscape/fullscreen artwork, matching the
/// generated-video feel when the source is a still illustration.


actor IllustrationImageLoader {
    static let shared = IllustrationImageLoader()

    private var cache: [URL: Image] = [:]
    private var inflight: [URL: Task<Image?, Never>] = [:]

    func load(_ url: URL) async -> Image? {
        if let cached = cache[url] { return cached }
        if let task = inflight[url] { return await task.value }
        let task = Task<Image?, Never> {
            guard let (data, _) = try? await URLSession.shared.data(from: url),
                  let uiImage = UIImage(data: data) else { return nil }
            return Image(uiImage: uiImage)
        }
        inflight[url] = task
        let image = await task.value
        inflight[url] = nil
        if let image {
            // A dense audiobook plan tops out at ~40 images; a blunt reset
            // guards the pathological case without an LRU.
            if cache.count > 64 { cache.removeAll() }
            cache[url] = image
        }
        return image
    }

    func prefetch(_ url: URL) async {
        _ = await load(url)
    }
}

#if DEBUG
// MARK: - Previews

/// Never returns a token, so the preview's APIClient can't make authenticated
/// calls — everything on screen comes from the seeded model state below.


struct PreviewTokenProvider: TokenProviding {
    func token() async -> String? { nil }
    func refreshedToken() async -> String? { nil }
}

/// Builds an offline `PlayerModel` frozen mid-playback. `start()` is never
/// called, so nothing plays and no sockets open; the seeded `caption` /
/// `lines` / `duration` drive the artwork, VTT caption, and scrubber layout.
@MainActor
private func fullScreenPreviewModel(withArtwork: Bool, colorOnly: Bool = false, colorCoverImageURL: String? = nil) -> PlayerModel {
    let coverJSON: String
    if withArtwork {
        coverJSON = ", \"cover\": {\"type\": \"gradient\", \"gradient_start\": \"#6D5BD0\", \"gradient_end\": \"#2B2350\"}"
    } else if colorOnly {
        let imageField = colorCoverImageURL.map { ", \"image_url\": \"\($0)\"" } ?? ""
        coverJSON = ", \"cover\": {\"type\": \"gradient\", \"gradient_start\": \"#6D5BD0\", \"gradient_end\": \"#6D5BD0\"\(imageField)}"
    } else {
        coverJSON = ""
    }
    let discussion = try! JSONDecoder().decode(Discussion.self, from: Data("""
    {
      "id": "preview-full-screen-player",
      "topic": "第三空间的卑微愿望",
      "title": "第三空间的卑微愿望 · 林悦",
      "status": "ready",
      "language": "zh"\(coverJSON)
    }
    """.utf8))

    let model = PlayerModel(
        discussion: discussion,
        api: APIClient(tokens: PreviewTokenProvider()),
        username: "preview"
    )
    model.duration = 1439
    model.currentTime = 573
    model.caption = "他在狭窄的地下室里看着糖糖的照片，那是他心中唯一的牵挂。"
    model.captionSpeaker = "林悦"
    model.lines = [
        LiveLine(speaker: "林悦", role: "host",
                 text: "远处传来机械的轰鸣声，那是第三空间永不停歇的脉搏。",
                 isUser: false, done: true),
    ]
    if withArtwork {
        // A timed illustration line so the artwork slot exercises the
        // illustration path (image caption stays off-screen; the VTT caption
        // above is what renders under the art). The gradient cover shows
        // until the image loads.
        model.lines.append(
            LiveLine(speaker: "", role: "image",
                     text: "狭窄地下室里的一盏孤灯。",
                     isUser: false, done: true,
                     imageURL: "https://picsum.photos/seed/debate-bot/900",
                     audioOffsetSeconds: 60)
        )
    }
    return model
}

#Preview("Artwork · VTT caption") {
    FullScreenPlayerView(model: fullScreenPreviewModel(withArtwork: true))
}

#Preview("No artwork · live caption") {
    FullScreenPlayerView(model: fullScreenPreviewModel(withArtwork: false))
}

#Preview("Single color cover · with image") {
    FullScreenPlayerView(model: fullScreenPreviewModel(
        withArtwork: false,
        colorOnly: true,
        colorCoverImageURL: "https://picsum.photos/seed/color-cover/900"
    ))
}

#Preview("Single color cover · no image") {
    FullScreenPlayerView(model: fullScreenPreviewModel(withArtwork: false, colorOnly: true))
}
#endif


