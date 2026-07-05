import Kingfisher
import SwiftUI
import TipKit
import UIKit

/// Full-screen "now playing" experience presented over `PodcastPlayerView`.
/// The cover art (or timed illustration) bleeds edge-to-edge from the top of
/// the screen with the synced caption overlaid on it, and a button flips it to
/// an Apple-Music-style synced caption list (or the single live caption while
/// streaming). Bottom: scrubber + skip ±15s + play/pause.
struct FullScreenPlayerView: View {
    @Bindable var model: PlayerModel
    @Environment(\.dismiss) private var dismiss
    @Environment(\.verticalSizeClass) private var verticalSizeClass

    /// When the podcast has a cover, the center starts on the artwork and the
    /// listener taps the transcript button to flip to the captions. Without a
    /// cover there's nothing to flip from, so the transcript shows immediately.
    @State private var showingTranscript = false
    @State private var showingCoverEditor = false
    @State private var showingShareSheet = false

    /// Landscape chrome visibility: like a video player, the header and
    /// transport controls fade out after a few idle seconds of playback and
    /// come back on tap. The synced caption stays up like a subtitle track.
    @State private var controlsVisible = true
    @State private var isScrubbing = false
    /// Bumped by any control interaction so the auto-hide timer restarts.
    @State private var interactionCount = 0

    private var isLandscape: Bool { verticalSizeClass == .compact }

    private static let autoHideDelay: Duration = .seconds(4)
    private static let controlsFade = Animation.easeInOut(duration: 0.25)

    /// Drives the "magic move" of the cover between the large centered artwork
    /// and the small header thumbnail shown in transcription mode.
    @Namespace private var coverNamespace
    private let coverID = "coverArt"

    private var cover: DiscussionCover? { model.discussion.cover }

    /// A cover worth displaying — either a fetchable image or a gradient.
    private var hasCover: Bool {
        cover?.hasImage == true || cover?.hasGradient == true
    }

    /// Timed audiobook illustrations replace the cover artwork while they
    /// apply: the latest arrived image during a live stream, or the image at
    /// the playback position for a finished audiobook. Nil falls back to the
    /// cover.
    private var currentIllustrationCue: PlayerModel.IllustrationCue? {
        if model.isLivePlayback { return model.latestIllustrationCue }
        return model.illustrationCue(at: model.currentTime)
    }

    private var currentIllustrationURL: URL? {
        currentIllustrationCue?.url
    }

    /// The synced VTT audio caption shown under the artwork, matching the mini
    /// player. Illustration cues carry their own image caption, but listeners
    /// follow the narration, so the audio line is surfaced instead.
    private var artworkCaption: String {
        model.caption.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The next timeline entry, prefetched so the hard cut lands instantly.
    private var nextIllustrationURL: URL? {
        guard !model.isLivePlayback else { return nil }
        return model.illustrationTimeline.first(where: { $0.start > model.currentTime })?.url
    }

    private var hasIllustrations: Bool {
        model.isLivePlayback ? model.latestIllustrationURL != nil : !model.illustrationTimeline.isEmpty
    }

    /// Anything worth showing in the artwork slot — a cover or timed
    /// illustrations (an audiobook without a cover still gets its artwork).
    private var hasArtwork: Bool { hasCover || hasIllustrations }

    /// Whether the center currently shows the transcript rather than the art.
    private var transcriptVisible: Bool { showingTranscript || !hasArtwork }

    /// The cover rides along in the header (small) while the transcript shows,
    /// and fills the center (large) otherwise. Mutually exclusive, so the same
    /// matched-geometry id is only ever present once.
    private var showsHeaderCover: Bool { hasArtwork && showingTranscript }
    private var showsCenterCover: Bool { hasArtwork && !showingTranscript }
    private var foregroundPalette: FullScreenForegroundPalette {
        FullScreenForegroundPalette(backgroundColors: model.coverColors)
    }

    var body: some View {
        ZStack {
            background
            if isLandscape {
                landscapeLayout
            } else {
                portraitLayout
            }
        }
        .onChange(of: isLandscape) { _, _ in revealControls() }
        .onChange(of: model.isPlaying) { _, _ in revealControls() }
        .onChange(of: showingTranscript) { _, _ in revealControls() }
        .task(id: autoHideKey) { await autoHideControlsAfterIdle() }
        .statusBarHidden(isLandscape && !controlsVisible)
        .persistentSystemOverlays(isLandscape && !controlsVisible ? .hidden : .automatic)
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
        .task(id: artworkColorKey) {
            await model.adoptArtworkColors(from: currentIllustrationURL)
        }
        .preventsIdleSleep()
    }

    /// Changes whenever the artwork on screen changes — a new timed
    /// illustration, or the cover's source after an edit — so the background
    /// palette always follows the image currently displayed.
    private var artworkColorKey: String {
        if let url = currentIllustrationURL { return url.absoluteString }
        return "\(cover?.imageURL ?? "")|\(cover?.gradientStart ?? "")|\(cover?.gradientEnd ?? "")"
    }

    // MARK: - Portrait

    private var portraitLayout: some View {
        VStack(spacing: 0) {
            // The hero fills everything above the controls, so its blurred
            // bottom edge always lands just above the seek bar.
            ZStack(alignment: .top) {
                if showsCenterCover {
                    coverHero
                }
                VStack(spacing: 0) {
                    header
                    ZStack {
                        if transcriptVisible {
                            transcript
                                .transition(.opacity)
                        }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                .padding(.horizontal, 20)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            controls
                .padding(.horizontal, 20)
        }
        .padding(.bottom, 24)
    }

    // MARK: - Landscape (video-player style)

    /// Landscape mirrors a video player: the artwork bleeds across the whole
    /// screen, the caption sits above the controls like a subtitle track, and
    /// the chrome (header, scrubber, transport) overlays the art and fades out
    /// after a few seconds of playback. Tapping anywhere brings it back.
    private var landscapeLayout: some View {
        ZStack {
            landscapeArtwork
            landscapeScrim
            VStack(spacing: 0) {
                if controlsVisible {
                    header
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
                if transcriptVisible {
                    transcript
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .transition(.opacity)
                } else {
                    Spacer(minLength: 0)
                }
                if !transcriptVisible, !artworkCaption.isEmpty {
                    heroCaption
                        .padding(.horizontal, 24)
                        .padding(.bottom, controlsVisible ? 14 : 24)
                }
                if controlsVisible {
                    landscapeControls
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 12)
            .animation(.easeInOut(duration: 0.2), value: artworkCaption)
        }
        .contentShape(Rectangle())
        .onTapGesture {
            // Lyric rows own their taps; the toggle only applies in art mode.
            guard !transcriptVisible else { return }
            withAnimation(Self.controlsFade) { controlsVisible.toggle() }
            interactionCount += 1
        }
    }

    private var landscapeArtwork: some View {
        GeometryReader { geo in
            FullScreenMovingArtwork(isPlaying: model.isPlaying) {
                coverImage
            }
            .frame(width: geo.size.width, height: geo.size.height)
            .clipped()
            .opacity(transcriptVisible ? 0.2 : 1)
        }
        .ignoresSafeArea()
    }

    /// Legibility scrim over the full-bleed art: a light band up top for the
    /// header and a heavier one at the bottom for the caption and controls,
    /// both easing off while the chrome is hidden.
    private var landscapeScrim: some View {
        LinearGradient(
            stops: [
                .init(color: .black.opacity(controlsVisible ? 0.45 : 0), location: 0),
                .init(color: .clear, location: 0.35),
                .init(color: .clear, location: 0.5),
                .init(color: .black.opacity(controlsVisible ? 0.75 : 0.35), location: 1),
            ],
            startPoint: .top,
            endPoint: .bottom
        )
        .ignoresSafeArea()
        .allowsHitTesting(false)
    }

    private var landscapeControls: some View {
        VStack(spacing: 2) {
            SeekBar(
                model: model,
                foregroundPalette: landscapePalette,
                onScrubbingChanged: { scrubbing in
                    isScrubbing = scrubbing
                    interactionCount += 1
                }
            )
            transportButtons(playDiameter: 60, skipFont: .title2, palette: landscapePalette)
                // Full width before the overlay: the toggle anchors to the
                // screen edge instead of covering the back-15s button.
                .frame(maxWidth: .infinity)
                .overlay(alignment: .leading) {
                    if hasArtwork {
                        transcriptToggleButton(palette: landscapePalette)
                    }
                }
                .overlay(alignment: .trailing) {
                    fullscreenToggleButton(palette: landscapePalette)
                }
        }
    }

    /// Over full-bleed art with a dark bottom scrim the chrome is always
    /// light; the sampled palette only applies in transcript mode where the
    /// tinted gradient background shows through.
    private var landscapePalette: FullScreenForegroundPalette {
        transcriptVisible ? foregroundPalette : .overArtwork
    }

    // MARK: - Auto-hide

    /// Any change to this key cancels the pending hide and, if the conditions
    /// still hold, arms a fresh timer — so every interaction buys the chrome
    /// another few seconds.
    private var autoHideKey: String {
        "\(isLandscape)|\(controlsVisible)|\(model.isPlaying)|\(transcriptVisible)|\(isScrubbing)|\(interactionCount)"
    }

    private func autoHideControlsAfterIdle() async {
        guard isLandscape, controlsVisible, model.isPlaying, !transcriptVisible, !isScrubbing else { return }
        try? await Task.sleep(for: Self.autoHideDelay)
        guard !Task.isCancelled else { return }
        withAnimation(Self.controlsFade) { controlsVisible = false }
    }

    private func revealControls() {
        interactionCount += 1
        guard !controlsVisible else { return }
        withAnimation(Self.controlsFade) { controlsVisible = true }
    }

    @ViewBuilder
    private var transcript: some View {
        if model.supportsLyrics {
            LyricsListView(model: model, foregroundPalette: foregroundPalette)
        } else {
            liveCaption
        }
    }

    /// Full-bleed hero artwork running from the very top of the screen (under
    /// the status bar, behind the header) down to just above the seek bar. The
    /// bottom progressively blurs and melts into the tinted background, and the
    /// synced VTT caption rides on top of the blurred band. The
    /// matched-geometry id lets it "magic move" to the header thumbnail when
    /// the listener flips to the transcript.
    private var coverHero: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottom) {
                coverImage
                    .matchedGeometryEffect(id: coverID, in: coverNamespace)
                    .frame(width: geo.size.width, height: geo.size.height)
                    .clipped()
                    .overlay {
                        // Gradient-masked material approximates a progressive
                        // blur: sharp on top, fully frosted at the bottom edge.
                        Rectangle()
                            .fill(.ultraThinMaterial)
                            .mask {
                                LinearGradient(
                                    stops: [
                                        .init(color: .clear, location: 0.55),
                                        .init(color: .black, location: 0.9),
                                    ],
                                    startPoint: .top,
                                    endPoint: .bottom
                                )
                            }
                    }
                    .mask {
                        // Short alpha fade at the very bottom so the blurred
                        // band dissolves into the background instead of ending
                        // on a hard edge above the controls.
                        LinearGradient(
                            stops: [
                                .init(color: .black, location: 0),
                                .init(color: .black, location: 0.92),
                                .init(color: .clear, location: 1),
                            ],
                            startPoint: .top,
                            endPoint: .bottom
                        )
                    }

                if !artworkCaption.isEmpty {
                    heroCaption
                        .padding(.horizontal, 24)
                        .padding(.bottom, 20)
                }
            }
            .animation(.easeInOut(duration: 0.2), value: artworkCaption)
        }
        .ignoresSafeArea(edges: [.top, .horizontal])
    }

    /// Subtitle-style caption bubble: white text on a translucent dark backing,
    /// so it stays legible no matter what the artwork behind it looks like.
    private var heroCaption: some View {
        Text(artworkCaption)
            .font(.callout.weight(.semibold))
            .foregroundStyle(.white)
            .multilineTextAlignment(.center)
            .lineLimit(3)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(.black.opacity(0.45), in: .rect(cornerRadius: 12))
            .id(artworkCaption)
            .transition(.opacity)
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
        if let illustration = currentIllustrationURL {
            // Timed audiobook illustration: hard cut, no animation. The base
            // cover keeps showing until the first illustration has loaded.
            IllustrationImageView(url: illustration, prefetchURL: nextIllustrationURL) {
                baseCoverImage
            }
        } else {
            baseCoverImage
        }
    }

    @ViewBuilder
    private var baseCoverImage: some View {
        if let url = coverImageURL {
            KFImage.url(url)
                .placeholder {
                    ZStack { coverGradient; ProgressView().tint(.white) }
                }
                .cancelOnDisappear(false)
                .retry(maxCount: 3, interval: .seconds(1))
                .resizable()
                .scaledToFill()
        } else {
            coverGradient
        }
    }

    private var coverGradient: some View {
        Color(hex: cover?.gradientStart ?? "#8E5CF7")
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
            transportButtons(playDiameter: 76, skipFont: .title, palette: foregroundPalette)

            HStack {
                if hasArtwork {
                    transcriptToggleButton(palette: foregroundPalette)
                }
                Spacer()
                fullscreenToggleButton(palette: foregroundPalette)
            }
        }
    }

    private func transportButtons(
        playDiameter: CGFloat,
        skipFont: Font,
        palette: FullScreenForegroundPalette
    ) -> some View {
        HStack(spacing: 40) {
            Button {
                model.skipBackward()
                interactionCount += 1
            } label: {
                Image(systemName: "gobackward.15").font(skipFont)
            }
            .disabled(!model.canSeek)

            Button {
                model.togglePlay()
                interactionCount += 1
            } label: {
                Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                    .font(.system(size: playDiameter * 0.42, weight: .bold))
                    .foregroundStyle(palette.primary)
                    .frame(width: playDiameter, height: playDiameter)
                    .glassEffect(in: .circle)
            }

            Button {
                model.skipForward()
                interactionCount += 1
            } label: {
                Image(systemName: "goforward.15").font(skipFont)
            }
            .disabled(!model.canSeek)
        }
        .foregroundStyle(palette.primary)
    }

    /// Flips the center between the artwork and the transcript, echoing the
    /// system player's lyrics button. Hidden when there's no cover to flip from.
    private func transcriptToggleButton(palette: FullScreenForegroundPalette) -> some View {
        Button {
            withAnimation(.spring(duration: 0.45)) {
                showingTranscript.toggle()
            }
        } label: {
            Image(systemName: "quote.bubble.fill")
                .font(.title3)
                .foregroundStyle(showingTranscript ? palette.accent : palette.primary)
                .frame(width: 44, height: 44)
                .glassEffect(in: .circle)
        }
        .accessibilityLabel(showingTranscript ? "Show cover" : "Show transcript")
        .popoverTip(FullScreenCaptionTip(), arrowEdge: .bottom)
    }

    /// Flips the interface into the video-style landscape presentation (and
    /// back), like a video player's fullscreen button. It only nudges the
    /// orientation once — the listener can still rotate the device physically.
    private func fullscreenToggleButton(palette: FullScreenForegroundPalette) -> some View {
        Button {
            requestInterfaceOrientation(isLandscape ? .portrait : .landscapeRight)
            interactionCount += 1
        } label: {
            Image(systemName: isLandscape
                ? "arrow.down.right.and.arrow.up.left"
                : "arrow.up.left.and.arrow.down.right")
                .font(.title3)
                .foregroundStyle(palette.primary)
                .frame(width: 44, height: 44)
                .glassEffect(in: .circle)
        }
        .accessibilityLabel(isLandscape ? "Exit fullscreen" : "Enter fullscreen")
    }

    private func requestInterfaceOrientation(_ orientation: UIInterfaceOrientationMask) {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        guard let scene = scenes.first(where: { $0.activationState == .foregroundActive }) ?? scenes.first else { return }
        scene.requestGeometryUpdate(.iOS(interfaceOrientations: orientation))
        scene.keyWindow?.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
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
private struct FullScreenMovingArtwork<Content: View>: View {
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
private struct IllustrationImageView<Placeholder: View>: View {
    let url: URL
    let prefetchURL: URL?
    @ViewBuilder var placeholder: () -> Placeholder

    @State private var current: LoadedIllustration?

    private struct LoadedIllustration: Equatable {
        let url: URL
        let image: Image

        static func == (lhs: LoadedIllustration, rhs: LoadedIllustration) -> Bool {
            lhs.url == rhs.url
        }
    }

    var body: some View {
        ZStack {
            if let current {
                current.image.resizable().scaledToFill()
            } else {
                placeholder()
            }
        }
        .task(id: url) {
            if current?.url != url,
               let image = await IllustrationImageLoader.shared.load(url),
               !Task.isCancelled {
                withTransaction(Transaction(animation: nil)) {
                    current = LoadedIllustration(url: url, image: image)
                }
            }
            if let prefetchURL {
                await IllustrationImageLoader.shared.prefetch(prefetchURL)
            }
        }
    }
}

/// Small in-memory cache for player artwork illustrations, so hard cuts land
/// instantly and scrubbing back re-shows earlier images without a re-fetch.
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
private struct PreviewTokenProvider: TokenProviding {
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
