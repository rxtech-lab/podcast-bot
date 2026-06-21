import SwiftUI
import SwiftData
import RxAuthSwift

/// The live podcast screen: streaming per-agent transcript bubbles, a synced
/// caption, a Liquid Glass music-player bar, and a message input — matching the
/// mockups.
struct PodcastPlayerView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.modelContext) private var context
    let discussion: Discussion

    @State private var model: PlayerModel?
    @State private var message = ""

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            if let model {
                VStack(spacing: 0) {
                    transcript(model)
                    footer(model)
                }
            } else {
                ProgressView().tint(Theme.accent)
            }
        }
        .navigationTitle(discussion.title.isEmpty ? "Podcast" : discussion.title)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if model == nil {
                let m = PlayerModel(discussion: discussion,
                                    api: APIClient(tokens: auth),
                                    context: context,
                                    username: auth.currentUser?.name ?? "You")
                m.start()
                model = m
            }
        }
        .onDisappear { model?.stop() }
    }

    private func transcript(_ model: PlayerModel) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 12) {
                    ForEach(model.lines) { line in
                        TranscriptBubble(line: line).id(line.id)
                    }
                }
                .padding(16)
            }
            .onChange(of: model.transcriptScrollToken) { _, _ in
                scrollToBottom(proxy, model: model)
            }
        }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy, model: PlayerModel) {
        guard let lastID = model.lines.last?.id else { return }
        DispatchQueue.main.async {
            withAnimation { proxy.scrollTo(lastID, anchor: .bottom) }
        }
    }

    @ViewBuilder
    private func footer(_ model: PlayerModel) -> some View {
        VStack(spacing: 10) {
            MusicPlayerBar(model: model)
            inputBar(model)
        }
        .padding(16)
    }

    private func inputBar(_ model: PlayerModel) -> some View {
        HStack(spacing: 10) {
            TextField("Send message", text: $message, axis: .vertical)
                .lineLimit(1...3)
                .textFieldStyle(.plain)
            Button {
                model.send(message)
                message = ""
            } label: {
                Image(systemName: "arrow.up.circle.fill").font(.title2).foregroundStyle(Theme.accent)
            }
            .disabled(message.trimmingCharacters(in: .whitespaces).isEmpty)
        }
        .padding(12)
        .glassEffect(in: .capsule)
    }
}

/// One transcript message: agents left with a name header, the user right in an
/// accent bubble (mockup image 4).
private struct TranscriptBubble: View {
    let line: LiveLine

    var body: some View {
        HStack {
            if line.isUser { Spacer(minLength: 40) }
            VStack(alignment: line.isUser ? .trailing : .leading, spacing: 4) {
                if !line.isUser {
                    Text(line.speaker.uppercased())
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(Theme.accent)
                }
                bubbleText
                    .font(.body)
                    .padding(12)
                    .background(
                        line.isUser ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(Theme.agentBubble),
                        in: .rect(cornerRadius: 18)
                    )
                    .foregroundStyle(line.isUser ? .white : .primary)
            }
            if !line.isUser { Spacer(minLength: 40) }
        }
    }

    @ViewBuilder
    private var bubbleText: some View {
        if line.isUser {
            Text(line.text)
        } else {
            MarkdownText(line.text)
        }
    }
}

/// Liquid Glass transport bar: title/phase, play-pause, progress.
private struct MusicPlayerBar: View {
    @Bindable var model: PlayerModel

    var body: some View {
        HStack(spacing: 14) {
            Button(action: model.togglePlay) {
                Image(systemName: model.isPlaying ? "pause.fill" : "play.fill")
                    .font(.title3)
                    .foregroundStyle(.white)
                    .frame(width: 44, height: 44)
                    .glassEffect(in: .circle)
            }
            VStack(alignment: .leading, spacing: 6) {
                Text(titleLine).font(.subheadline.weight(.medium)).lineLimit(1)
                if !model.caption.isEmpty {
                    Text(model.caption)
                        .font(.callout.weight(.medium))
                        .foregroundStyle(.primary)
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                ProgressView(value: progress)
                    .tint(Theme.accent)
                HStack {
                    Text(timeString(progressTime)).font(.caption2).foregroundStyle(Theme.secondaryText)
                    Spacer()
                    if model.isFinished, model.downloadURL != nil {
                        Label("Ready", systemImage: "checkmark.circle.fill")
                            .font(.caption2).foregroundStyle(.green)
                    } else {
                        Text(timeString(progressDuration)).font(.caption2).foregroundStyle(Theme.secondaryText)
                    }
                }
            }
            if let url = model.downloadURL {
                Link(destination: url) {
                    Image(systemName: "arrow.down.circle").font(.title3).foregroundStyle(Theme.accent)
                }
            }
        }
        .padding(12)
        .glassEffect(in: .rect(cornerRadius: 20))
    }

    private var titleLine: String {
        if !model.phaseLabel.isEmpty { return model.phaseLabel }
        if !model.statusText.isEmpty { return model.statusText }
        return model.discussion.title
    }

    private var progress: Double {
        guard progressDuration > 0 else { return 0 }
        return min(1, progressTime / progressDuration)
    }

    private var progressTime: Double {
        if model.duration > 0 { return model.currentTime }
        return max(model.currentTime, model.elapsedTime)
    }

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
