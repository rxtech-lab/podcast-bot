import Kingfisher
import SwiftUI
import TipKit
import UIKit

/// Full-screen "now playing" experience presented over `PodcastPlayerView`.
/// Timed audiobook illustrations bleed edge-to-edge with the synced caption
/// overlaid on them, while ordinary cover art stays centered like a system Now
/// Playing screen. A button flips either presentation to an Apple-Music-style
/// synced caption list (or the single live caption while streaming). Bottom:
/// scrubber + skip ±15s + play/pause.
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
    @State private var showingCaptionDownloadSheet = false

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
        .sheet(isPresented: $showingCaptionDownloadSheet) {
            if let jobID = model.discussion.jobID {
                CaptionDownloadSheet(
                    jobID: jobID,
                    title: model.discussion.displayTitle,
                    language: model.presentationLanguage,
                    api: model.api
                )
            }
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
                    if currentIllustrationURL != nil {
                        illustrationHero
                    } else {
                        centeredCoverArt
                    }
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
            if currentIllustrationURL != nil {
                FullScreenMovingArtwork(isPlaying: model.isPlaying) {
                    artworkImage
                }
                .frame(width: geo.size.width, height: geo.size.height)
                .clipped()
                .opacity(transcriptVisible ? 0.2 : 1)
            } else {
                let side = min(min(geo.size.width * 0.46, geo.size.height * 0.68), 440)
                baseCoverImage
                    .frame(width: side, height: side)
                    .clipShape(.rect(cornerRadius: 20))
                    .shadow(color: .black.opacity(0.35), radius: 24, y: 14)
                    .scaleEffect(model.isPlaying ? 1.0 : 0.9)
                    .animation(.spring(duration: 0.5), value: model.isPlaying)
                    .frame(width: geo.size.width, height: geo.size.height)
                    .opacity(transcriptVisible ? 0.2 : 1)
            }
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
        transcriptVisible || currentIllustrationURL == nil ? foregroundPalette : .overArtwork
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

    /// Full-bleed audiobook illustration running from the very top of the
    /// screen (under the status bar, behind the header) down to just above the
    /// seek bar. The bottom progressively blurs into the tinted background and
    /// the synced VTT caption rides on top of the blurred band.
    private var illustrationHero: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottom) {
                artworkImage
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

    /// Large, bounded cover in the middle of the player. Timed audiobook
    /// illustrations deliberately do not use this view: they retain the
    /// existing full-bleed presentation above.
    private var centeredCoverArt: some View {
        GeometryReader { geo in
            let side = max(0, min(min(geo.size.width - 40, geo.size.height - 32), 520))
            baseCoverImage
                .matchedGeometryEffect(id: coverID, in: coverNamespace)
                .frame(width: side, height: side)
                .clipShape(.rect(cornerRadius: 20))
                .shadow(color: .black.opacity(0.35), radius: 24, y: 14)
                .scaleEffect(model.isPlaying ? 1.0 : 0.86)
                .animation(.spring(duration: 0.5), value: model.isPlaying)
                .overlay(alignment: .bottom) {
                    if !artworkCaption.isEmpty {
                        heroCaption
                            .frame(maxWidth: side)
                            // Hang the caption below the artwork so it never
                            // covers the cover; the cover itself stays centered
                            // whether or not a caption is showing.
                            .alignmentGuide(.bottom) { $0[.top] - 16 }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .animation(.easeInOut(duration: 0.2), value: artworkCaption)
        }
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
        artworkImage
            .matchedGeometryEffect(id: coverID, in: coverNamespace)
            .frame(width: 48, height: 48)
            .clipShape(.rect(cornerRadius: 10))
            .shadow(color: .black.opacity(0.25), radius: 6, y: 3)
    }

    @ViewBuilder
    private var artworkImage: some View {
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
    /// a dark purple gradient when no palette is available yet.
    private var background: some View {
        let palette = model.coverColors.count >= 2
            ? model.coverColors
            : FullScreenPlayerStyle.defaultBackgroundColors
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
        .glassChip()
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
                onCreateFromPlan: nil,
                onDownloadCaptions: downloadCaptionsAction
            )
                .font(.title3)
                .foregroundStyle(foregroundPalette.primary)
                .frame(width: 40, height: 40)
                .glassEffect(in: .circle)
        } else {
            Color.clear.frame(width: 40, height: 40)
        }
    }

    private var downloadCaptionsAction: (() -> Void)? {
        guard model.discussion.status == .ready, model.discussion.jobID != nil else { return nil }
        return { showingCaptionDownloadSheet = true }
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
        .accessibilityIdentifier("player.lyrics")
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
