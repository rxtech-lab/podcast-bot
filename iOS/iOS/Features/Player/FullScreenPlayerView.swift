import SwiftUI

/// Full-screen "now playing" experience presented over `PodcastPlayerView`.
/// Top: minimize. Center: an Apple-Music-style synced caption list when the
/// discussion is ready, or the single live caption while streaming. Bottom:
/// scrubber + skip ±15s + play/pause.
struct FullScreenPlayerView: View {
    @Bindable var model: PlayerModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        ZStack {
            background
            VStack(spacing: 0) {
                topBar
                Group {
                    if model.supportsLyrics {
                        LyricsListView(model: model)
                    } else {
                        liveCaption
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
            PodcastDocumentExporter(url: file.url)
        }
    }

    private var background: some View {
        LinearGradient(
            colors: [Theme.accent.opacity(0.35), Theme.background],
            startPoint: .top,
            endPoint: .bottom
        )
        .ignoresSafeArea()
        .overlay(Theme.background.opacity(0.3).ignoresSafeArea())
    }

    private var topBar: some View {
        HStack {
            Button {
                dismiss()
            } label: {
                Image(systemName: "chevron.down")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(.white)
                    .frame(width: 40, height: 40)
                    .glassEffect(in: .circle)
            }
            .accessibilityLabel("Minimize")

            Spacer()

            VStack(spacing: 2) {
                Text(model.discussion.displayTitle.isEmpty ? "Podcast" : model.discussion.displayTitle)
                    .font(.subheadline.weight(.semibold))
                    .lineLimit(1)
                if !model.phaseLabel.isEmpty || !model.statusText.isEmpty {
                    Text(model.phaseLabel.isEmpty ? model.statusText : model.phaseLabel)
                        .font(.caption2)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(1)
                }
            }

            Spacer()

            if model.showsPodcastActions {
                PodcastActionsMenu(model: model)
                    .font(.title3)
                    .foregroundStyle(.white)
                    .frame(width: 40, height: 40)
                    .glassEffect(in: .circle)
            } else {
                Color.clear.frame(width: 40, height: 40)
            }
        }
        .padding(.top, 8)
    }

    private var liveCaption: some View {
        VStack(spacing: 16) {
            if !model.captionSpeaker.isEmpty {
                Text(model.captionSpeaker.uppercased())
                    .font(.headline.weight(.bold))
                    .foregroundStyle(Theme.accent)
            }
            Text(model.caption.isEmpty ? "…" : model.caption)
                .font(.title2.weight(.semibold))
                .multilineTextAlignment(.center)
                .foregroundStyle(.white)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 8)
    }

    private var controls: some View {
        VStack(spacing: 20) {
            SeekBar(model: model)
            HStack(spacing: 40) {
                Button { model.skipBackward() } label: {
                    Image(systemName: "gobackward.15").font(.title)
                }
                .disabled(!model.canSeek)

                Button(action: model.togglePlay) {
                    Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                        .font(.system(size: 32, weight: .bold))
                        .foregroundStyle(.white)
                        .frame(width: 76, height: 76)
                        .glassEffect(in: .circle)
                }

                Button { model.skipForward() } label: {
                    Image(systemName: "goforward.15").font(.title)
                }
                .disabled(!model.canSeek)
            }
            .foregroundStyle(.white)
        }
    }
}

/// Scrubber + elapsed/remaining labels. Mirrors the mini-bar slider logic but
/// fills the full width; falls back to a progress bar while streaming.
private struct SeekBar: View {
    @Bindable var model: PlayerModel
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
                Text(timeString(displayTime)).font(.caption2).foregroundStyle(Theme.secondaryText)
                Spacer()
                Text(timeString(progressDuration)).font(.caption2).foregroundStyle(Theme.secondaryText)
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
                            isActive: group.id == model.activeLyricGroupID
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

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !speaker.isEmpty {
                Text(speaker.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(isActive ? Theme.accent : Theme.secondaryText)
            }
            Text(group.text)
                .font(.title3.weight(.semibold))
                .foregroundStyle(isActive ? .white : Theme.secondaryText)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
        }
        .opacity(isActive ? 1 : 0.55)
        .scaleEffect(isActive ? 1.0 : 0.98, anchor: .leading)
        .animation(.spring(duration: 0.3), value: isActive)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
