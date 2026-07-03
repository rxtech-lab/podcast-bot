import SwiftUI
import TipKit
import UIKit

/// Full-screen "now playing" experience presented over `PodcastPlayerView`.
/// Top: minimize. Center: the podcast cover art when one exists, with a button
/// that flips to an Apple-Music-style synced caption list (or the single live
/// caption while streaming). Bottom: scrubber + skip ±15s + play/pause.
struct FullScreenPlayerView: View {
    @Bindable var model: PlayerModel
    @Environment(\.dismiss) private var dismiss

    /// When the podcast has a cover, the center starts on the artwork and the
    /// listener taps the transcript button to flip to the captions. Without a
    /// cover there's nothing to flip from, so the transcript shows immediately.
    @State private var showingTranscript = false
    @State private var showingCoverEditor = false
    @State private var showingShareSheet = false

    /// Drives the "magic move" of the cover between the large centered artwork
    /// and the small header thumbnail shown in transcription mode.
    @Namespace private var coverNamespace
    private let coverID = "coverArt"

    private var cover: DiscussionCover? { model.discussion.cover }

    /// A cover worth displaying — either a fetchable image or a gradient.
    private var hasCover: Bool {
        cover?.hasImage == true || cover?.hasGradient == true
    }

    /// Whether the center currently shows the transcript rather than the art.
    private var transcriptVisible: Bool { showingTranscript || !hasCover }

    /// The cover rides along in the header (small) while the transcript shows,
    /// and fills the center (large) otherwise. Mutually exclusive, so the same
    /// matched-geometry id is only ever present once.
    private var showsHeaderCover: Bool { hasCover && showingTranscript }
    private var showsCenterCover: Bool { hasCover && !showingTranscript }
    private var foregroundPalette: FullScreenForegroundPalette {
        FullScreenForegroundPalette(backgroundColors: model.coverColors)
    }

    var body: some View {
        ZStack {
            background
            VStack(spacing: 0) {
                header
                ZStack {
                    if transcriptVisible {
                        transcript
                            .transition(.opacity)
                    } else {
                        coverArt
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                controls
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 24)
        }
        .sheet(isPresented: Binding(
            get: { model.showsDownloadDialog },
            set: { if !$0 { model.showsDownloadDialog = false } }
        )) {
            DownloadProgressSheet(model: model)
        }
        .sheet(item: Binding(
            get: { model.downloadedPodcastFile },
            set: { model.downloadedPodcastFile = $0 }
        )) { file in
            FileShareSheet(url: file.url)
        }
        .sheet(isPresented: $showingCoverEditor) {
            CoverEditorSheet(discussion: Binding(
                get: { model.discussion },
                set: { model.discussion = $0 }
            ))
        }
        .sheet(isPresented: $showingShareSheet) {
            ShareSheet(discussionID: model.discussion.id, api: model.api)
        }
        .task(id: coverColorKey) {
            await model.loadCoverColors()
        }
        .preventsIdleSleep()
    }

    /// Changes whenever the cover's source changes, so the background palette is
    /// recomputed after an edit or a background-generated cover lands.
    private var coverColorKey: String {
        "\(cover?.imageURL ?? "")|\(cover?.gradientStart ?? "")|\(cover?.gradientEnd ?? "")"
    }

    @ViewBuilder
    private var transcript: some View {
        if model.supportsLyrics {
            LyricsListView(model: model, foregroundPalette: foregroundPalette)
        } else {
            liveCaption
        }
    }

    /// Large rounded artwork that gently shrinks when paused, mirroring the
    /// system Now Playing screen. Falls back to the cover's gradient. The
    /// matched-geometry id lets it "magic move" to the header thumbnail when the
    /// listener flips to the transcript.
    private var coverArt: some View {
        GeometryReader { geo in
            let side = min(geo.size.width, geo.size.height) - 8
            // matchedGeometryEffect must wrap the *flexible* image and the frame
            // must come after it, otherwise the inner frame pins the size and
            // only the position animates (no grow/shrink) — see swiftui-lab /
            // Chris Eidhof on the modifier-order pitfall.
            coverImage
                .matchedGeometryEffect(id: coverID, in: coverNamespace)
                .frame(width: side, height: side)
                .clipShape(.rect(cornerRadius: 20))
                .shadow(color: .black.opacity(0.35), radius: 24, y: 14)
                .scaleEffect(model.isPlaying ? 1.0 : 0.86)
                .animation(.spring(duration: 0.5), value: model.isPlaying)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    /// Small header artwork shown in transcription mode; shares the cover's
    /// matched-geometry id so it grows from / shrinks to the large artwork.
    private var coverThumbnail: some View {
        coverImage
            .matchedGeometryEffect(id: coverID, in: coverNamespace)
            .frame(width: 48, height: 48)
            .clipShape(.rect(cornerRadius: 10))
            .shadow(color: .black.opacity(0.25), radius: 6, y: 3)
    }

    @ViewBuilder
    private var coverImage: some View {
        if let url = coverImageURL {
            AsyncImage(url: url) { phase in
                switch phase {
                case .success(let image):
                    image.resizable().scaledToFill()
                case .empty:
                    ZStack { coverGradient; ProgressView().tint(.white) }
                default:
                    coverGradient
                }
            }
        } else {
            coverGradient
        }
    }

    private var coverGradient: some View {
        LinearGradient(
            colors: [
                Color(hex: cover?.gradientStart ?? "#8E5CF7"),
                Color(hex: cover?.gradientEnd ?? "#00A3FF"),
            ],
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
    }

    private var coverImageURL: URL? {
        guard let urlString = cover?.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
              !urlString.isEmpty else { return nil }
        return URL(string: urlString)
    }

    /// Background tinted from the cover's two main colors (gradient hexes or
    /// colors sampled from the artwork), with a top-to-bottom dark scrim so the
    /// transcript and controls stay legible over bright covers. Falls back to
    /// the accent gradient when no palette is available yet.
    private var background: some View {
        let palette = model.coverColors.count >= 2
            ? model.coverColors
            : [Theme.accent.opacity(0.35), Theme.background]
        return LinearGradient(colors: palette, startPoint: .topLeading, endPoint: .bottomTrailing)
            .overlay(
                LinearGradient(
                    colors: [.black.opacity(0.15), .black.opacity(0.5)],
                    startPoint: .top,
                    endPoint: .bottom
                )
            )
            .ignoresSafeArea()
            .animation(.easeInOut(duration: 0.6), value: model.coverColors)
    }

    /// Adaptive top bar. In transcription mode it mirrors the system player:
    /// the cover thumbnail and a left-aligned title lead the row. Otherwise the
    /// title is centered and the large artwork lives in the body.
    private var header: some View {
        HStack(spacing: 12) {
            Button {
                dismiss()
            } label: {
                Image(systemName: "chevron.down")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(foregroundPalette.primary)
                    .frame(width: 40, height: 40)
                    .glassEffect(in: .circle)
            }
            .accessibilityLabel("Minimize")

            if showsHeaderCover {
                coverThumbnail
                titleBlock(alignment: .leading)
                Spacer(minLength: 0)
            } else {
                Spacer(minLength: 0)
                titleBlock(alignment: .center)
                Spacer(minLength: 0)
            }

            actionsMenu
        }
        .padding(.top, 8)
    }

    private func titleBlock(alignment: HorizontalAlignment) -> some View {
        VStack(alignment: alignment, spacing: 2) {
            Text(model.discussion.displayTitle.isEmpty ? AppStringLiteral.stationNameRaw : model.discussion.displayTitle)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(foregroundPalette.primary)
                .lineLimit(1)
            if !headerSubtitle.isEmpty {
                Text(headerSubtitle)
                    .font(.caption2)
                    .foregroundStyle(foregroundPalette.secondary)
                    .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: alignment == .leading ? .leading : .center)
    }

    private var headerSubtitle: String {
        if !model.currentAudioBookChapterTitle.isEmpty { return model.currentAudioBookChapterTitle }
        if !model.phaseLabel.isEmpty { return model.phaseLabel }
        if !model.statusText.isEmpty { return model.statusText }
        return ""
    }

    @ViewBuilder
    private var actionsMenu: some View {
        if model.showsPodcastActions {
            PodcastActionsMenu(
                model: model,
                showsPoints: false,
                pointsMenuLabel: "Points",
                onShowPoints: {},
                onPublish: {},
                onEditCover: { showingCoverEditor = true },
                onMakePrivate: {},
                onShare: { showingShareSheet = true },
                onCreateFollowUp: nil,
                isCreatingFromPlan: false,
                onCreateFromPlan: nil
            )
                .font(.title3)
                .foregroundStyle(foregroundPalette.primary)
                .frame(width: 40, height: 40)
                .glassEffect(in: .circle)
        } else {
            Color.clear.frame(width: 40, height: 40)
        }
    }

    private var liveCaption: some View {
        VStack(spacing: 16) {
            if !model.captionSpeaker.isEmpty {
                Text(model.captionSpeaker.uppercased())
                    .font(.headline.weight(.bold))
                    .foregroundStyle(foregroundPalette.accent)
            }
            Text(model.caption.isEmpty ? "…" : model.caption)
                .font(.title2.weight(.semibold))
                .multilineTextAlignment(.center)
                .foregroundStyle(foregroundPalette.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 8)
    }

    private var controls: some View {
        VStack(spacing: 20) {
            SeekBar(model: model, foregroundPalette: foregroundPalette)
            HStack(spacing: 40) {
                Button { model.skipBackward() } label: {
                    Image(systemName: "gobackward.15").font(.title)
                }
                .disabled(!model.canSeek)

                Button(action: model.togglePlay) {
                    Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                        .font(.system(size: 32, weight: .bold))
                        .foregroundStyle(foregroundPalette.primary)
                        .frame(width: 76, height: 76)
                        .glassEffect(in: .circle)
                }

                Button { model.skipForward() } label: {
                    Image(systemName: "goforward.15").font(.title)
                }
                .disabled(!model.canSeek)
            }
            .foregroundStyle(foregroundPalette.primary)

            if hasCover {
                transcriptToggle
            }
        }
    }

    /// Flips the center between the artwork and the transcript, echoing the
    /// system player's lyrics button. Hidden when there's no cover to flip from.
    private var transcriptToggle: some View {
        HStack {
            Button {
                withAnimation(.spring(duration: 0.45)) {
                    showingTranscript.toggle()
                }
            } label: {
                Image(systemName: "quote.bubble.fill")
                    .font(.title3)
                    .foregroundStyle(showingTranscript ? foregroundPalette.accent : foregroundPalette.primary)
                    .frame(width: 44, height: 44)
                    .glassEffect(in: .circle)
            }
            .accessibilityLabel(showingTranscript ? "Show cover" : "Show transcript")
            .popoverTip(FullScreenCaptionTip(), arrowEdge: .bottom)
            Spacer()
        }
    }
}

@MainActor
private final class IdleSleepPrevention {
    static let shared = IdleSleepPrevention()

    private var activeTokens: Set<UUID> = []
    private var previousIdleTimerState: Bool?

    func begin(token: UUID) {
        guard !activeTokens.contains(token) else { return }
        if activeTokens.isEmpty {
            previousIdleTimerState = UIApplication.shared.isIdleTimerDisabled
            UIApplication.shared.isIdleTimerDisabled = true
        }
        activeTokens.insert(token)
    }

    func end(token: UUID) {
        guard activeTokens.remove(token) != nil else { return }
        if activeTokens.isEmpty {
            UIApplication.shared.isIdleTimerDisabled = previousIdleTimerState ?? false
            previousIdleTimerState = nil
        }
    }
}

private struct IdleSleepPreventionModifier: ViewModifier {
    @State private var token = UUID()
    @State private var isActive = false

    func body(content: Content) -> some View {
        content
            .onAppear(perform: begin)
            .onDisappear(perform: end)
    }

    private func begin() {
        guard !isActive else { return }
        isActive = true
        IdleSleepPrevention.shared.begin(token: token)
    }

    private func end() {
        guard isActive else { return }
        isActive = false
        IdleSleepPrevention.shared.end(token: token)
    }
}

extension View {
    func preventsIdleSleep() -> some View {
        modifier(IdleSleepPreventionModifier())
    }
}

/// Scrubber + elapsed/remaining labels. Mirrors the mini-bar slider logic but
/// fills the full width; falls back to a progress bar while streaming.
private struct SeekBar: View {
    @Bindable var model: PlayerModel
    let foregroundPalette: FullScreenForegroundPalette
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
                })
                .tint(Theme.accent)
            } else {
                ProgressView(value: progress)
                    .tint(Theme.accent)
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
private struct LyricsListView: View {
    @Bindable var model: PlayerModel
    let foregroundPalette: FullScreenForegroundPalette
    private let tapSeekOffset = 0.05

    var body: some View {
        ScrollViewReader { proxy in
            let groups = model.lyricCueGroups
            ScrollView(showsIndicators: false) {
                LazyVStack(alignment: .leading, spacing: 18) {
                    ForEach(groups) { group in
                        LyricLine(
                            group: group,
                            speaker: model.speaker(for: group),
                            isActive: group.id == model.activeLyricGroupID,
                            foregroundPalette: foregroundPalette
                        )
                        .id(group.id)
                        .onTapGesture { model.seek(to: group.start + tapSeekOffset) }
                    }
                }
                .padding(.vertical, 40)
            }
            .onChange(of: model.activeLyricGroupID) { _, id in
                guard let id else { return }
                withAnimation(.spring(duration: 0.4)) {
                    proxy.scrollTo(id, anchor: .center)
                }
            }
            .onAppear {
                if let id = model.activeLyricGroupID {
                    proxy.scrollTo(id, anchor: .center)
                }
            }
        }
    }
}

private struct LyricLine: View {
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

private struct FullScreenForegroundPalette {
    let primary: Color
    let secondary: Color
    let accent: Color

    init(backgroundColors: [Color]) {
        let luminance = Self.averageScrimmedLuminance(for: backgroundColors)
        if luminance < 0.45 {
            primary = .white
            secondary = .white.opacity(0.68)
        } else {
            primary = .black
            secondary = .black.opacity(0.58)
        }
        accent = Theme.accent
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
