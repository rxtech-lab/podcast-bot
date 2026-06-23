import PhotosUI
import RxAuthSwift
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// The live podcast screen: streaming per-agent transcript bubbles, a synced
/// caption, a Liquid Glass music-player bar, and a message input — matching the
/// mockups.
struct PodcastPlayerView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(\.scenePhase) private var scenePhase
    let discussion: Discussion

    @State private var model: PlayerModel?
    @State private var message = ""
    @State private var showingPlan = false
    @State private var showingFullPlayer = false
    @State private var showingImporter = false
    @State private var showingPhotos = false
    @State private var showingPointsHistory = false
    @State private var showingPublishSheet = false
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var isUploadingAttachment = false
    @State private var transcriptIsAtBottom = true
    @State private var transcriptShouldScrollToBottom = false
    @State private var transcriptScrollRequestTask: Task<Void, Never>?

    /// Stable id for the optional points-summary accessory row so it doesn't
    /// churn its identity across renders.
    private static let usageItemID = UUID()

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            if let model {
                if model.isTranscriptLoading {
                    PodcastTranscriptLoadingView()
                } else {
                    transcript(model)
                        .safeAreaInset(edge: .bottom, spacing: 0) {
                            footer(model)
                        }
                }
            } else {
                ProgressView().tint(Theme.accent)
            }
        }
        .navigationTitle(discussion.displayTitle.isEmpty ? AppStringLiteral.stationNameRaw : discussion.displayTitle)
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
            if purchases.isConfigured || model?.showsPodcastActions == true {
                ToolbarItem(placement: .topBarTrailing) {
                    if let model {
                        PodcastActionsMenu(
                            model: model,
                            showsPoints: purchases.isConfigured,
                            pointsMenuLabel: pointsMenuLabel,
                            onShowPoints: { showingPointsHistory = true },
                            onPublish: { showingPublishSheet = true },
                            onMakePrivate: { makePrivate(model) }
                        )
                    } else {
                        PodcastLoadingMenu(
                            showsPoints: purchases.isConfigured,
                            pointsMenuLabel: pointsMenuLabel,
                            onShowPoints: { showingPointsHistory = true }
                        )
                    }
                }
            }
        }
        .sheet(isPresented: $showingPlan) {
            PlanSheetView(discussion: discussion)
        }
        .sheet(isPresented: $showingPointsHistory) {
            PointsHistoryView()
        }
        .sheet(isPresented: $showingPublishSheet) {
            if let model {
                PublishStationSheet(discussion: Binding(
                    get: { model.discussion },
                    set: { model.discussion = $0 }
                ))
            }
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
            await purchases.refreshBalance()
        }
        .onDisappear {
            // Presenting the full-screen cover disappears this view; don't tear
            // down the shared model in that case — only on real navigation exit.
            guard !showingFullPlayer else { return }
            model?.stop()
        }
        .onChange(of: scenePhase) { _, phase in
            // Returning to the foreground while the job is live: the socket may
            // have been torn down while suspended, so reconcile the transcript
            // immediately to recover anything that streamed in the background.
            if phase == .active { model?.foregroundRefresh() }
        }
    }

    /// Balance label for the podcast options menu, matching the discussion page.
    private var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Points", comment: "Podcast menu label when the points balance is unknown")
        }
        let pointLabel = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Points (Balance \(UsageSummary.formatInt(balance)) \(pointLabel))",
                      comment: "Podcast menu points label; first value is the formatted balance, second is the localized unit")
    }

    private func transcript(_ model: PlayerModel) -> some View {
        MessageList(
            messages: transcriptItems(for: model),
            isStreaming: isTranscriptStreaming(model),
            shouldScrollToBottom: transcriptShouldScrollToBottom,
            isAtBottom: $transcriptIsAtBottom
        ) { item in
            transcriptRow(item)
                .padding(.horizontal, 16)
                .padding(.vertical, 6)
        }
        .scrollDismissesKeyboard(.interactively)
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.12) {
                requestTranscriptScrollToBottom()
            }
        }
    }

    @ViewBuilder
    private func transcriptRow(_ item: TranscriptListItem) -> some View {
        switch item {
        case .line(let line):
            TranscriptBubble(line: line)
        case .usage(_, let points):
            PointsSummaryBubble(points: points)
        }
    }

    /// Transcript lines, plus the points summary as a trailing accessory row.
    ///
    /// The listener's own messages are intentionally hidden: a sent message is
    /// only used to steer the panel, and the backend echoes it straight back
    /// over the socket as a `role: "user"` transcript event. Surfacing it would
    /// duplicate the listener's text in what is otherwise a podcast transcript,
    /// so we drop any user-authored line here (the message is still sent and
    /// persisted — just not rendered).
    private func transcriptItems(for model: PlayerModel) -> [TranscriptListItem] {
        var items = model.lines
            .filter { !$0.isUser && !PlayerModel.isUserRole($0.role) }
            .map { TranscriptListItem.line($0) }
        // Show only the points this podcast consumed (planning + generation),
        // never the underlying token/cost detail. Points are known once the
        // discussion is charged (after generation completes).
        if let points = model.discussion.pointsText {
            items.append(.usage(id: Self.usageItemID, points: points))
        }
        return items
    }

    /// Streaming is in effect while the most recent line is still being written.
    /// The `MessageList` follows the bottom while true and, on a fresh user send,
    /// pins that message to the top until the reply grows in.
    private func isTranscriptStreaming(_ model: PlayerModel) -> Bool {
        !(model.lines.last?.done ?? true)
    }

    /// Toggle `transcriptShouldScrollToBottom` off→on so `MessageList` performs a
    /// one-shot scroll to the bottom (e.g. when the keyboard appears).
    private func requestTranscriptScrollToBottom() {
        transcriptScrollRequestTask?.cancel()
        transcriptShouldScrollToBottom = false
        transcriptScrollRequestTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(10))
            guard !Task.isCancelled else { return }
            transcriptShouldScrollToBottom = true
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
            Menu {
                Button {
                    showingPhotos = true
                } label: {
                    Label("Photo Library", systemImage: "photo.on.rectangle")
                }
                Button {
                    showingImporter = true
                } label: {
                    Label("Files", systemImage: "folder")
                }
            } label: {
                if isUploadingAttachment {
                    ProgressView().controlSize(.small).tint(Theme.accent)
                } else {
                    Image(systemName: "paperclip").font(.title3).foregroundStyle(Theme.accent)
                }
            }
            .disabled(isUploadingAttachment)
            TextField("Send message", text: $message, axis: .vertical)
                .lineLimit(1 ... 3)
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
        .fileImporter(isPresented: $showingImporter,
                      allowedContentTypes: attachmentContentTypes,
                      allowsMultipleSelection: false)
        { result in
            if case .success(let urls) = result, let url = urls.first {
                shareDocument(url, model: model)
            }
        }
        .photosPicker(isPresented: $showingPhotos, selection: $selectedPhoto, matching: .images)
        .onChange(of: selectedPhoto) { _, item in
            if let item { sharePhoto(item, model: model) }
        }
    }

    /// Uploads a document and injects its parsed text into the live discussion.
    private func shareDocument(_ url: URL, model: PlayerModel) {
        let access = url.startAccessingSecurityScopedResource()
        let data = try? Data(contentsOf: url)
        if access { url.stopAccessingSecurityScopedResource() }
        let filename = url.lastPathComponent
        guard let data else { return }
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        shareData(data, filename: filename, mime: mime, model: model)
    }

    /// Loads a picked photo's bytes and shares it like a document.
    private func sharePhoto(_ item: PhotosPickerItem, model: PlayerModel) {
        let utType = item.supportedContentTypes.first
        let ext = utType?.preferredFilenameExtension ?? "jpg"
        let mime = utType?.preferredMIMEType ?? "image/jpeg"
        let filename = "Photo-\(UUID().uuidString.prefix(6)).\(ext)"
        isUploadingAttachment = true
        Task { @MainActor in
            defer { selectedPhoto = nil }
            guard let data = try? await item.loadTransferable(type: Data.self) else {
                isUploadingAttachment = false
                return
            }
            shareData(data, filename: filename, mime: mime, model: model)
        }
    }

    /// Uploads bytes and injects the returned reference into the live discussion.
    private func shareData(_ data: Data, filename: String, mime: String, model: PlayerModel) {
        isUploadingAttachment = true
        let api = APIClient(tokens: auth)
        Task { @MainActor in
            defer { isUploadingAttachment = false }
            do {
                let resp = try await api.uploadFile(data: data, filename: filename, mimeType: mime)
                if let markdown = resp.markdown, !markdown.isEmpty {
                    let trimmed = String(markdown.prefix(6000))
                    model.send("I'm sharing a document \"\(filename)\". Please consider it:\n\n" + trimmed)
                } else if resp.mimeType?.hasPrefix("image/") == true {
                    model.send("I'm sharing an image \"\(filename)\": \(resp.url)")
                }
            } catch {
                // Best-effort: a failed share is silently dropped here; the
                // user can retry. (Surfaced state would need a toast/banner.)
            }
        }
    }

    private func makePrivate(_ model: PlayerModel) {
        Task { @MainActor in
            do {
                model.discussion = try await APIClient(tokens: auth).updateDiscussionVisibility(
                    id: model.discussion.id,
                    visibility: .private
                )
            } catch {
                // The existing player surface does not have a toast lane; leave
                // the station public and let the user retry from the menu.
            }
        }
    }
}

private struct PodcastTranscriptLoadingView: View {
    var body: some View {
        VStack(spacing: 16) {
            ProgressView()
                .controlSize(.large)
                .tint(Theme.accent)
            VStack(spacing: 6) {
                Text("Preparing Transcript")
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text("Loading \(AppStringLiteral.stationNameRaw)...")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }
}

struct PodcastActionsMenu: View {
    @Bindable var model: PlayerModel
    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void
    let onPublish: () -> Void
    let onMakePrivate: () -> Void

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
            if showsPoints && model.showsPodcastActions {
                Divider()
            }
            if model.discussion.isOwner != false {
                if model.discussion.isPublic {
                    Button(role: .destructive, action: onMakePrivate) {
                        Label("Make Private", systemImage: "lock")
                    }
                } else {
                    Button(action: onPublish) {
                        Label("Publish to Market", systemImage: "globe")
                    }
                }
            }
            if model.canDownloadPodcast {
                Button {
                    model.downloadPodcast()
                } label: {
                    Label(model.isDownloadingPodcast ? "Downloading" : "Download \(AppStringLiteral.stationNameRaw)",
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
            Image(systemName: "ellipsis")
        }
        .accessibilityLabel("\(AppStringLiteral.stationNameRaw) actions")
    }
}

struct PodcastLoadingMenu: View {
    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
        } label: {
            Image(systemName: "ellipsis.circle")
        }
        .accessibilityLabel("\(AppStringLiteral.stationNameRaw) actions")
    }
}

struct DownloadProgressSheet: View {
    @Bindable var model: PlayerModel

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: model.downloadErrorText == nil ? "arrow.down.circle.fill" : "exclamationmark.triangle.fill")
                .font(.system(size: 44, weight: .semibold))
                .foregroundStyle(model.downloadErrorText == nil ? Theme.accent : .orange)
            Text(model.downloadErrorText == nil ? "Downloading \(AppStringLiteral.stationNameRaw)" : "Download Failed")
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
    @State private var discussion: Discussion
    @State private var showingSources = false

    init(discussion: Discussion) {
        _discussion = State(initialValue: discussion)
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        PlanSnapshotCard(label: "Plan", snapshot: PlanSnapshot(discussion: discussion)) {
                            showingSources = true
                        }
                    }
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
            .sheet(isPresented: $showingSources) {
                SourcesSheet(
                    discussion: discussion,
                    allowsAddingSources: false
                )
            }
        }
    }
}

/// A row in the transcript `MessageList`: either a live transcript line or the
/// trailing points-summary accessory.
private enum TranscriptListItem: Identifiable, MessageListItem {
    case line(LiveLine)
    case usage(id: UUID, points: String)

    var id: UUID {
        switch self {
        case .line(let line): return line.id
        case .usage(let id, _): return id
        }
    }

    var isUserMessage: Bool {
        if case .line(let line) = self { return line.isUser }
        return false
    }

    /// The points summary is an accessory — it never participates in user-message
    /// pinning.
    var isMessageListAccessory: Bool {
        if case .usage = self { return true }
        return false
    }
}

/// Deterministic per-speaker identity: each panelist gets a stable color and an
/// initials avatar so the transcript reads as a conversation between distinct
/// people instead of a wall of identical grey bubbles.
enum SpeakerPalette {
    /// Distinct hues that all sit well on black and harmonize with the purple
    /// accent. The first entry is the accent itself so a lone host echoes the app.
    private static let colors: [Color] = [
        Theme.accent, // violet
        Color(red: 0.20, green: 0.72, blue: 0.90), // cyan
        Color(red: 0.95, green: 0.45, blue: 0.62), // rose
        Color(red: 0.97, green: 0.66, blue: 0.31), // amber
        Color(red: 0.36, green: 0.79, blue: 0.55), // green
        Color(red: 0.46, green: 0.56, blue: 0.98), // blue
    ]

    static func color(for speaker: String) -> Color {
        guard !speaker.isEmpty else { return colors[0] }
        // djb2 — stable across launches so a speaker keeps the same color.
        var hash = 5381
        for scalar in speaker.unicodeScalars {
            hash = (hash &* 33) &+ Int(scalar.value)
        }
        return colors[abs(hash) % colors.count]
    }

    static func initials(for speaker: String) -> String {
        let letters = initialsSource(for: speaker)
            .split(separator: " ")
            .prefix(2)
            .compactMap(\.first)
            .map(String.init)
        return letters.isEmpty ? "?" : letters.joined().uppercased()
    }

    private static func initialsSource(for speaker: String) -> String {
        var result = ""
        var parenthesisDepth = 0
        for character in speaker {
            switch character {
            case "(", "（":
                parenthesisDepth += 1
            case ")", "）":
                if parenthesisDepth > 0 {
                    parenthesisDepth -= 1
                } else {
                    result.append(character)
                }
            default:
                if parenthesisDepth == 0 {
                    result.append(character)
                }
            }
        }
        return result.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// A small gradient avatar with the speaker's initials in their palette color.
private struct SpeakerAvatar: View {
    let speaker: String
    var size: CGFloat = 32

    var body: some View {
        let color = SpeakerPalette.color(for: speaker)
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

/// One transcript message: agents left with an avatar + name header, the user
/// right in an accent bubble (mockup image 4).
private struct TranscriptBubble: View {
    let line: LiveLine

    private var speakerColor: Color { SpeakerPalette.color(for: line.speaker) }

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if line.isUser { Spacer(minLength: 40) }
            if !line.isUser {
                SpeakerAvatar(speaker: line.speaker)
            }
            VStack(alignment: line.isUser ? .trailing : .leading, spacing: 4) {
                if !line.isUser {
                    Text(line.speaker.uppercased())
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(speakerColor)
                }
                bubbleText
                    .font(.body)
                    .padding(12)
                    .background(bubbleStyle, in: .rect(cornerRadius: 18))
                    .overlay {
                        RoundedRectangle(cornerRadius: 18)
                            .strokeBorder(line.isUser ? .clear : speakerColor.opacity(0.28),
                                          lineWidth: 0.5)
                    }
                    .foregroundStyle(line.isUser ? .white : .primary)
            }
            if !line.isUser { Spacer(minLength: 40) }
        }
    }

    /// User bubbles get an accent gradient for depth; agent bubbles take a soft
    /// tint of their speaker color so each panelist's turns are recognizable.
    private var bubbleStyle: AnyShapeStyle {
        if line.isUser {
            AnyShapeStyle(LinearGradient(
                colors: [Theme.accent, Theme.accent.opacity(0.82)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            ))
        } else {
            AnyShapeStyle(speakerColor.opacity(0.14))
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

/// Trailing accessory row showing only the points this podcast consumed. The
/// detailed token/cost breakdown is intentionally hidden from users; the server
/// sends only the points total.
private struct PointsSummaryBubble: View {
    let points: String

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 6) {
                Label("This \(AppStringLiteral.stationNameRaw)", systemImage: "sparkles")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                Text("Used \(points)")
                    .font(.callout.weight(.semibold))
                    .foregroundStyle(.primary)
                    .monospacedDigit()
            }
            .padding(14)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 14))
            Spacer(minLength: 40)
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
                    .foregroundStyle(.primary)
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

#if DEBUG
/// Offline harness that exercises the pinned-turn behavior of `MessageList`
/// using the real `TranscriptBubble` rows. Tap send: the user message pins to
/// the top, a simulated host reply streams in below it, and the list releases to
/// follow the bottom once the reply finishes.
private struct PodcastPinPreview: View {
    @State private var lines: [LiveLine] = [
        LiveLine(speaker: "Dr. Lena Ortiz", role: "host", text: "Welcome back. Today we're asking how AI will reshape the classroom over the next decade.", isUser: false, done: true),
        LiveLine(speaker: "Prof. Adeyemi", role: "discussant", text: "Personalized tutoring is the headline, but the research on learning gains is still mixed.", isUser: false, done: true),
        LiveLine(speaker: "Maya Chen", role: "discussant", text: "From the product side, adoption is exploding — the question is whether outcomes follow.", isUser: false, done: true),
    ]
    @State private var message = "What about students who can't afford these tools?"
    @State private var isAtBottom = true
    @State private var shouldScrollToBottom = false
    @State private var replyTask: Task<Void, Never>?

    private var isStreaming: Bool { !(lines.last?.done ?? true) }

    private func items() -> [TranscriptListItem] { lines.map { .line($0) } }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            MessageList(
                messages: items(),
                isStreaming: isStreaming,
                shouldScrollToBottom: shouldScrollToBottom,
                isAtBottom: $isAtBottom
            ) { item in
                if case .line(let line) = item {
                    TranscriptBubble(line: line)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 6)
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) { inputBar }
        }
    }

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField("Send message", text: $message, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.plain)
            Button(action: send) {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundStyle(Theme.accent)
            }
            .disabled(message.trimmingCharacters(in: .whitespaces).isEmpty || isStreaming)
        }
        .padding(12)
        .glassEffect(in: .capsule)
        .padding(16)
    }

    private func send() {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        message = ""
        lines.append(LiveLine(speaker: "You", role: "user", text: text, isUser: true, done: true))
        replyTask?.cancel()
        replyTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(450))
            guard !Task.isCancelled else { return }
            lines.append(LiveLine(speaker: "Dr. Lena Ortiz", role: "host", text: "", isUser: false, done: false))
            let idx = lines.count - 1
            let chunks = [
                "That's the equity question at the heart of this. ",
                "If the best tutors are paywalled, ",
                "AI could widen the gap it promises to close. ",
                "Districts will need procurement and access policies ",
                "before the tools, not after.",
            ]
            var acc = ""
            for chunk in chunks {
                try? await Task.sleep(for: .milliseconds(380))
                guard !Task.isCancelled else { return }
                acc += chunk
                lines[idx].text = acc
            }
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            lines[idx].done = true
        }
    }
}

#Preview("PodcastPlayerView · Pin to top") {
    PodcastPinPreview()
}
#endif
