import SwiftUI
import UIKit
import RxAuthSwift

/// The live podcast screen: streaming per-agent transcript bubbles, a synced
/// caption, a Liquid Glass music-player bar, and a message input — matching the
/// mockups.
struct PodcastPlayerView: View {
    private static let transcriptBottomID = "transcript-bottom"

    @Environment(AuthManager.self) private var auth
    let discussion: Discussion

    @State private var model: PlayerModel?
    @State private var message = ""
    @State private var showingPlan = false
    @State private var showingFullPlayer = false

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            if let model {
                transcript(model)
                    .safeAreaInset(edge: .bottom, spacing: 0) {
                        footer(model)
                    }
            } else {
                ProgressView().tint(Theme.accent)
            }
        }
        .navigationTitle(discussion.displayTitle.isEmpty ? "Podcast" : discussion.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    showingPlan = true
                } label: {
                    Image(systemName: "doc.text")
                }
                .accessibilityLabel("Plan")
            }
            if let model, model.showsPodcastActions {
                ToolbarItem(placement: .topBarTrailing) {
                    PodcastActionsMenu(model: model)
                }
            }
        }
        .sheet(isPresented: $showingPlan) {
            PlanSheetView(discussion: discussion)
        }
        .sheet(isPresented: Binding(
            get: { model?.showsDownloadDialog == true },
            set: { isPresented in
                if !isPresented { model?.showsDownloadDialog = false }
            }
        )) {
            if let model {
                DownloadProgressSheet(model: model)
            }
        }
        .sheet(item: Binding(
            get: { model?.downloadedPodcastFile },
            set: { model?.downloadedPodcastFile = $0 }
        )) { file in
            PodcastDocumentExporter(url: file.url)
        }
        .fullScreenCover(isPresented: $showingFullPlayer) {
            if let model {
                FullScreenPlayerView(model: model)
            }
        }
        .task {
            if model == nil {
                let m = PlayerModel(discussion: discussion,
                                    api: APIClient(tokens: auth),
                                    username: auth.currentUser?.name ?? "You")
                m.start()
                model = m
            }
        }
        .onDisappear {
            // Presenting the full-screen cover disappears this view; don't tear
            // down the shared model in that case — only on real navigation exit.
            guard !showingFullPlayer else { return }
            model?.stop()
        }
    }

    private func transcript(_ model: PlayerModel) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 12) {
                    ForEach(model.lines) { line in
                        TranscriptBubble(line: line).id(line.id)
                    }
                    if let summary = model.usageSummary {
                        UsageSummaryBubble(summary: summary)
                            .id("usage-summary")
                    } else if !model.usageSummaryText.isEmpty {
                        UsageSummaryBubble(fallbackText: model.usageSummaryText)
                            .id("usage-summary")
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.transcriptBottomID)
                }
                .padding(16)
            }
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: model.transcriptScrollToken) { _, _ in
                scrollToBottom(proxy)
            }
            .onChange(of: model.usageSummaryText) { _, _ in
                scrollToBottom(proxy)
            }
            .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
                scrollToBottom(proxy, delay: 0.12)
            }
        }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy, delay: TimeInterval = 0) {
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) {
            withAnimation { proxy.scrollTo(Self.transcriptBottomID, anchor: .bottom) }
        }
    }

    @ViewBuilder
    private func footer(_ model: PlayerModel) -> some View {
        VStack(spacing: 10) {
            MusicPlayerBar(model: model) { showingFullPlayer = true }
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

struct PodcastActionsMenu: View {
    @Bindable var model: PlayerModel

    var body: some View {
        Menu {
            if model.canDownloadPodcast {
                Button {
                    model.downloadPodcast()
                } label: {
                    Label(model.isDownloadingPodcast ? "Downloading" : "Download Podcast",
                          systemImage: model.isDownloadingPodcast ? "hourglass" : "arrow.down.circle")
                }
                .disabled(model.isDownloadingPodcast)
            } else if model.showsForceStopAction {
                Button(role: .destructive) {
                    model.forceStop()
                } label: {
                    Label(model.isForceStopping ? "Finalising" : "Force Stop",
                          systemImage: model.isForceStopping ? "hourglass" : "stop.fill")
                }
                .disabled(!model.canForceStop)
            }
        } label: {
            Image(systemName: "ellipsis.circle")
        }
        .accessibilityLabel("Podcast actions")
    }
}

struct DownloadProgressSheet: View {
    @Bindable var model: PlayerModel

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: model.downloadErrorText == nil ? "arrow.down.circle.fill" : "exclamationmark.triangle.fill")
                .font(.system(size: 44, weight: .semibold))
                .foregroundStyle(model.downloadErrorText == nil ? Theme.accent : .orange)
            Text(model.downloadErrorText == nil ? "Downloading Podcast" : "Download Failed")
                .font(.headline)
            if model.isDownloadingPodcast && model.downloadProgress <= 0 {
                ProgressView()
                    .tint(Theme.accent)
            } else {
                ProgressView(value: model.downloadProgress)
                    .tint(Theme.accent)
            }
            Text("\(Int(model.downloadProgress * 100))%")
                .font(.caption)
                .foregroundStyle(Theme.secondaryText)
            if let error = model.downloadErrorText {
                Text(error)
                    .font(.footnote)
                    .multilineTextAlignment(.center)
                    .foregroundStyle(Theme.secondaryText)
                Button("Close") {
                    model.showsDownloadDialog = false
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding(24)
        .presentationDetents([.height(model.downloadErrorText == nil ? 220 : 320)])
        .interactiveDismissDisabled(model.isDownloadingPodcast)
    }
}

struct PodcastDocumentExporter: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forExporting: [url], asCopy: true)
        picker.shouldShowFileExtensions = true
        return picker
    }

    func updateUIViewController(_ uiViewController: UIDocumentPickerViewController, context: Context) {}
}

private struct PlanSheetView: View {
    @Environment(\.dismiss) private var dismiss
    let discussion: Discussion

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                ScrollView {
                    PlanSnapshotCard(label: "Plan", snapshot: PlanSnapshot(discussion: discussion))
                        .padding(16)
                }
                .scrollDismissesKeyboard(.interactively)
            }
            .navigationTitle("Plan")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark")
                    }
                    .accessibilityLabel("Close")
                }
            }
        }
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

private struct UsageSummaryBubble: View {
    private let summary: UsageSummary?
    private let fallbackText: String?

    init(summary: UsageSummary) {
        self.summary = summary
        self.fallbackText = nil
    }

    init(fallbackText: String) {
        self.summary = nil
        self.fallbackText = fallbackText
    }

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 10) {
                Label("Generation summary", systemImage: "wand.and.stars")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                if let summary {
                    breakdown(summary)
                } else if let fallbackText {
                    Text(fallbackText)
                        .font(.callout.weight(.medium))
                        .foregroundStyle(.primary)
                }
            }
            .padding(14)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 14))
            Spacer(minLength: 40)
        }
    }

    @ViewBuilder
    private func breakdown(_ s: UsageSummary) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            // Tokens
            VStack(alignment: .leading, spacing: 4) {
                row("Tokens", value: UsageSummary.formatInt(s.totalTokens), emphasized: true)
                row("Input", value: UsageSummary.formatInt(s.promptTokens), indented: true)
                row("Output", value: UsageSummary.formatInt(s.completionTokens), indented: true)
            }

            Divider().overlay(Theme.secondaryText.opacity(0.3))

            // Cost
            if s.costKnown {
                VStack(alignment: .leading, spacing: 4) {
                    if let llm = s.llmCostUSD {
                        row("Language model", value: UsageSummary.formatUSD(llm))
                    }
                    if s.ttsCostUSD > 0 {
                        row("Speech (TTS)", value: UsageSummary.formatUSD(s.ttsCostUSD))
                    }
                    if s.musicCostUSD > 0 {
                        row("Music", value: UsageSummary.formatUSD(s.musicCostUSD))
                    }
                }
                if let total = s.totalCostUSD {
                    Divider().overlay(Theme.secondaryText.opacity(0.3))
                    row("Total cost", value: UsageSummary.formatUSD(total), emphasized: true)
                }
            } else {
                Text("Total cost unavailable")
                    .font(.caption.weight(.medium))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
    }

    private func row(_ label: String, value: String, emphasized: Bool = false, indented: Bool = false) -> some View {
        HStack(spacing: 8) {
            Text(label)
                .font(indented ? .caption.weight(.medium) : .callout.weight(emphasized ? .semibold : .regular))
                .foregroundStyle(indented ? Theme.secondaryText : .primary)
                .padding(.leading, indented ? 12 : 0)
            Spacer(minLength: 16)
            Text(value)
                .font(.callout.weight(emphasized ? .bold : .medium))
                .foregroundStyle(emphasized ? Theme.accent : .primary)
                .monospacedDigit()
        }
    }
}

/// Liquid Glass transport bar: title/phase, play-pause, progress.
private struct MusicPlayerBar: View {
    @Bindable var model: PlayerModel
    var onExpand: () -> Void = {}

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
                HStack(spacing: 6) {
                    Text(headerLine).font(.subheadline.weight(.medium)).lineLimit(1)
                    Spacer(minLength: 4)
                    Image(systemName: "chevron.up")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(Theme.secondaryText)
                }
                if !model.caption.isEmpty {
                    Text(model.caption)
                        .font(.callout.weight(.medium))
                        .foregroundStyle(.primary)
                        .lineLimit(4)
                        .fixedSize(horizontal: false, vertical: true)
                }
                ProgressView(value: progress)
                    .tint(Theme.accent)
                HStack {
                    Text(timeString(progressTime)).font(.caption2).foregroundStyle(Theme.secondaryText)
                    Spacer()
                    if model.canDownloadPodcast {
                        Label("Ready", systemImage: "checkmark.circle.fill")
                            .font(.caption2).foregroundStyle(.green)
                    } else {
                        Text(timeString(progressDuration)).font(.caption2).foregroundStyle(Theme.secondaryText)
                    }
                }
            }
            .contentShape(.rect)
            .onTapGesture(perform: onExpand)
            if model.canDownloadPodcast {
                Button {
                    model.downloadPodcast()
                } label: {
                    Image(systemName: "arrow.down.circle").font(.title3).foregroundStyle(Theme.accent)
                }
                .disabled(model.isDownloadingPodcast)
            }
        }
        .padding(12)
        .glassEffect(in: .rect(cornerRadius: 20))
    }

    private var titleLine: String {
        if !model.phaseLabel.isEmpty { return model.phaseLabel }
        if !model.statusText.isEmpty { return model.statusText }
        return model.discussion.displayTitle
    }

    private var headerLine: String {
        guard !model.captionSpeaker.isEmpty else { return titleLine }
        return "\(titleLine) · \(model.captionSpeaker)"
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
