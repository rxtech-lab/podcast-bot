import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
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
    var onCreatedFromPlan: ((Discussion) -> Void)?
    /// Non-nil when this discussion was opened via a private share link; passed
    /// to the player model so a non-owner participant's comments are authorized.
    var shareToken: String? = nil
    var onSignOut: (() -> Void)?

    @State private var model: PlayerModel?
    @State private var message = ""
    @State private var showingPlan = false
    @State private var showingSummary = false
    @State private var showingFullPlayer = false
    @State private var showingImporter = false
    @State private var showingPhotos = false
    @State private var showingPointsHistory = false
    @State private var showingPublishSheet = false
    @State private var showingCoverEditor = false
    @State private var showingShareSheet = false
    @State private var showingCreatorProfile = false
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var showingRecorder = false
    @State private var resumePlaybackAfterRecorder = false
    @State private var isUploadingAttachment = false
    @State private var isCreatingFromPlan = false
    @State private var createFromPlanError: String?
    @State private var isGeneratingSummary = false
    @State private var summaryGenerateError: String?
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
        .toolbar { podcastToolbar }
        .alert("Could not create \(AppStringLiteral.stationNameRaw)", isPresented: createFromPlanErrorBinding) {
            Button("OK", role: .cancel) { createFromPlanError = nil }
        } message: {
            Text(createFromPlanError ?? "")
        }
        .alert("Could not generate summary", isPresented: summaryGenerateErrorBinding) {
            Button("OK", role: .cancel) { summaryGenerateError = nil }
        } message: {
            Text(summaryGenerateError ?? "")
        }
        .sheet(isPresented: $showingPlan) {
            PlanSheetView(discussion: currentDiscussion)
        }
        .sheet(isPresented: $showingSummary) {
            summarySheet
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
        .sheet(isPresented: $showingCoverEditor) {
            if let model {
                CoverEditorSheet(discussion: Binding(
                    get: { model.discussion },
                    set: { model.discussion = $0 }
                ))
            }
        }
        .sheet(isPresented: $showingShareSheet) {
            ShareSheet(discussionID: currentDiscussion.id,
                       api: APIClient(tokens: auth))
        }
        .sheet(isPresented: $showingCreatorProfile) {
            if let creator = currentCreator {
                CreatorProfileView(creatorID: creator.id,
                                   initialProfile: creator,
                                   onCreateFromPlan: onCreatedFromPlan)
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
            FileShareSheet(url: file.url)
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
                                    username: auth.currentUser?.name ?? "You",
                                    userID: auth.currentUser?.id ?? "",
                                    shareToken: shareToken)
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
        .preventsIdleSleep()
    }

    @ToolbarContentBuilder
    private var podcastToolbar: some ToolbarContent {
        if currentCreator != nil {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    showingCreatorProfile = true
                } label: {
                    Image(systemName: "person.crop.circle")
                }
                .accessibilityLabel("View creator")
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                Button {
                    showingPlan = true
                } label: {
                    Label("Plan", systemImage: "doc.text")
                }
                if summaryAvailable {
                    Button {
                        showingSummary = true
                    } label: {
                        Label("Summary", systemImage: "doc.richtext")
                    }
                } else if summaryPending || isGeneratingSummary {
                    Button {
                    } label: {
                        Label("Generating summary", systemImage: "hourglass")
                    }
                    .disabled(true)
                } else if summaryGenerationAvailable {
                    Button {
                        generateSummary()
                    } label: {
                        Label("Generate summary", systemImage: "sparkles")
                    }
                } else {
                    Button {
                    } label: {
                        Label("Summary", systemImage: "doc.richtext")
                    }
                    .disabled(true)
                }
            } label: {
                Image(systemName: "doc.text")
            }
            .accessibilityLabel("Documents")
            .popoverTip(PodcastPlanTip(), arrowEdge: .top)
        }
        if showsActionsMenu {
            ToolbarItem(placement: .topBarTrailing) {
                if let model {
                    PodcastActionsMenu(
                        model: model,
                        showsPoints: purchases.isConfigured,
                        pointsMenuLabel: pointsMenuLabel,
                        onShowPoints: { showingPointsHistory = true },
                        onPublish: { showingPublishSheet = true },
                        onEditCover: { showingCoverEditor = true },
                        onMakePrivate: { makePrivate(model) },
                        onShare: { showingShareSheet = true },
                        isCreatingFromPlan: isCreatingFromPlan,
                        onCreateFromPlan: createFromPlanAction,
                        onSignOut: onSignOut
                    )
                } else {
                    PodcastLoadingMenu(
                        showsPoints: purchases.isConfigured,
                        pointsMenuLabel: pointsMenuLabel,
                        onShowPoints: { showingPointsHistory = true },
                        isCreatingFromPlan: isCreatingFromPlan,
                        onCreateFromPlan: createFromPlanAction,
                        onSignOut: onSignOut
                    )
                }
            }
        }
    }

    private var currentCreator: CreatorProfile? {
        model?.discussion.creator ?? discussion.creator
    }

    private var currentDiscussion: Discussion {
        model?.discussion ?? discussion
    }

    /// Whether the Summary menu item is enabled — true only once the server has
    /// generated the podcast's summary document (status `ready`).
    private var summaryAvailable: Bool {
        currentDiscussion.hasSummary
    }

    private var summaryPending: Bool {
        currentDiscussion.summaryPending
    }

    private var summaryGenerationAvailable: Bool {
        currentDiscussion.status == .ready
            && currentDiscussion.isOwner == true
            && currentDiscussion.canGenerateSummary
    }

    /// Extracted so the construction of `SummaryView` (and its `APIClient`) stays
    /// out of the main `body` modifier chain, which is large enough that inlining
    /// it pushes the SwiftUI type-checker past its time budget.
    private var summarySheet: some View {
        SummaryView(discussionID: currentDiscussion.id,
                    title: currentDiscussion.displayTitle,
                    api: APIClient(tokens: auth))
    }

    private var showsActionsMenu: Bool {
        purchases.isConfigured
            || model?.showsPodcastActions == true
            || onCreatedFromPlan != nil
            || onSignOut != nil
    }

    private var createFromPlanAction: (() -> Void)? {
        guard onCreatedFromPlan != nil else { return nil }
        return { createFromPlan() }
    }

    private var createFromPlanErrorBinding: Binding<Bool> {
        Binding(
            get: { createFromPlanError != nil },
            set: { if !$0 { createFromPlanError = nil } }
        )
    }

    private var summaryGenerateErrorBinding: Binding<Bool> {
        Binding(
            get: { summaryGenerateError != nil },
            set: { if !$0 { summaryGenerateError = nil } }
        )
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
        case .line(let line, let isMine):
            TranscriptBubble(line: line, isMine: isMine)
        case .usage(_, let points):
            PointsSummaryBubble(points: points)
        }
    }

    /// Whether a transcript line was authored by *this* participant. Only
    /// user-authored lines qualify (panelists never). Ownership is decided by the
    /// server-owned `senderUserID` so another human participant — who also arrives
    /// with `isUser == true` — never renders in my accent bubble. Display-name
    /// matching is used only as a fallback for legacy rows that predate the sender
    /// id (no id on either side to compare).
    private func isMyLine(_ line: LiveLine) -> Bool {
        PlayerModel.isLineAuthoredByCurrentUser(
            line,
            currentUserID: model?.currentUserID ?? auth.currentUser?.id ?? "",
            currentUsername: model?.currentUsername ?? auth.currentUser?.name ?? ""
        )
    }

    /// Transcript lines, plus the points summary as a trailing accessory row.
    private func transcriptItems(for model: PlayerModel) -> [TranscriptListItem] {
        var items = model.lines
            .filter { PlayerModel.isVisibleTranscriptLine($0) }
            .map { TranscriptListItem.line($0, isMine: isMyLine($0)) }
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
        let canSend = model.canSendMessages
        let trimmedMessage = message.trimmingCharacters(in: .whitespacesAndNewlines)
        let disabledControlColor = Color(uiColor: .secondaryLabel)
        let attachmentColor = canSend ? Theme.accent : disabledControlColor
        let sendColor = canSend && !trimmedMessage.isEmpty ? Theme.accent : disabledControlColor
        return HStack(spacing: 10) {
            Menu {
                Button {
                    if model.isPlaying {
                        resumePlaybackAfterRecorder = true
                        model.togglePlay()
                    } else {
                        resumePlaybackAfterRecorder = false
                    }
                    showingRecorder = true
                } label: {
                    Label("Record Audio", systemImage: "mic.fill")
                }
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
                    ProgressView().controlSize(.small).tint(attachmentColor)
                } else {
                    Image(systemName: "plus.circle.fill").font(.title2).foregroundStyle(attachmentColor)
                }
            }
            .disabled(isUploadingAttachment || !canSend)
            .popoverTip(SendAudioTip(), arrowEdge: .bottom)
            TextField("Send message", text: $message, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.plain)
                .disabled(!canSend)
            Button {
                model.send(message)
                message = ""
            } label: {
                Image(systemName: "arrow.up.circle.fill").font(.title2).foregroundStyle(sendColor)
            }
            .disabled(!canSend || trimmedMessage.isEmpty)
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
        .sheet(isPresented: $showingRecorder, onDismiss: {
            resumePlaybackIfNeededAfterRecorder(model)
        }) {
            VoiceRecorderSheet(defaultLanguage: model.discussion.language) { recording in
                sendVoiceMessage(recording, model: model)
            }
        }
    }

    private func resumePlaybackIfNeededAfterRecorder(_ model: PlayerModel) {
        guard resumePlaybackAfterRecorder else { return }
        resumePlaybackAfterRecorder = false
        if !model.isPlaying {
            model.togglePlay()
        }
    }

    /// Uploads a recorded voice message to S3, then sends it: the transcript is the
    /// message text the agent reads, the audio URL/key let others replay it.
    private func sendVoiceMessage(_ recording: VoiceMessageRecorder.Recording, model: PlayerModel) {
        let access = recording.fileURL.startAccessingSecurityScopedResource()
        let data = try? Data(contentsOf: recording.fileURL)
        if access { recording.fileURL.stopAccessingSecurityScopedResource() }
        try? FileManager.default.removeItem(at: recording.fileURL)
        guard let data else { return }
        let filename = "Voice-\(UUID().uuidString.prefix(6)).m4a"
        isUploadingAttachment = true
        let api = APIClient(tokens: auth)
        Task { @MainActor in
            defer { isUploadingAttachment = false }
            do {
                let resp = try await api.uploadFile(data: data, filename: filename, mimeType: recording.mimeType)
                // Prefer the on-device transcript. When the device couldn't produce
                // one, fall back to a server-side (gateway whisper) transcription so
                // the agent still reads the message — but never block sending.
                var text = recording.transcript
                if text.isEmpty, let key = resp.key {
                    text = (try? await api.transcribeAudio(key: key)) ?? ""
                }
                model.send(text, audioURL: resp.url, audioKey: resp.key)
            } catch {
                // Best-effort: fall back to sending the transcript as plain text so
                // the user's message still reaches the discussion.
                if !recording.transcript.isEmpty {
                    model.send(recording.transcript)
                }
            }
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

    private func generateSummary() {
        guard !isGeneratingSummary else { return }
        isGeneratingSummary = true
        Task { @MainActor in
            defer { isGeneratingSummary = false }
            do {
                let updated = try await APIClient(tokens: auth).generateSummary(id: currentDiscussion.id)
                if let model {
                    model.discussion = PlayerModel.mergingLocalDiscussionState(
                        current: model.discussion,
                        fresh: updated
                    )
                    model.listenForJobUpdatesIfNeeded()
                }
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                summaryGenerateError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func createFromPlan() {
        guard !isCreatingFromPlan else { return }
        isCreatingFromPlan = true
        Task { @MainActor in
            defer { isCreatingFromPlan = false }
            do {
                let created = try await APIClient(tokens: auth).createDiscussionFromPlan(id: discussion.id)
                onCreatedFromPlan?(created)
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                createFromPlanError = (error as? APIError)?.errorDescription ?? error.localizedDescription
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
    @State private var showingForceStopConfirm = false

    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void
    let onPublish: () -> Void
    let onEditCover: () -> Void
    let onMakePrivate: () -> Void
    /// Opens the private share sheet (duration picker + manage links). Only
    /// invoked for private discussions; public ones share a plain link inline.
    var onShare: () -> Void = {}
    let isCreatingFromPlan: Bool
    let onCreateFromPlan: (() -> Void)?
    var onSignOut: (() -> Void)?

    /// The plain, permanent public deep link for a published discussion.
    private var publicShareURL: URL {
        AppConfig.websiteBaseURL.appendingPathComponent("d").appendingPathComponent(model.discussion.id)
    }

    private var actionsTip: (any Tip)? {
        if model.discussion.isPublic {
            return ShareStationTip()
        }
        if model.discussion.isOwner != false {
            return PublishToMarketTip()
        }
        return nil
    }

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
            if showsPoints && (model.showsPodcastActions || onCreateFromPlan != nil) {
                Divider()
            }
            if let onCreateFromPlan {
                Button(action: onCreateFromPlan) {
                    Label(isCreatingFromPlan ? "Creating" : "Create from Plan",
                          systemImage: isCreatingFromPlan ? "hourglass" : "plus.circle")
                }
                .disabled(isCreatingFromPlan)
            }
            if model.discussion.isOwner != false {
                Button(action: onEditCover) {
                    Label("Edit Cover", systemImage: "photo.badge.plus")
                }
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
            // Share: public discussions hand out a plain permanent link; private
            // ones open the duration sheet to mint an expiring, revocable link.
            if model.discussion.isPublic {
                ShareLink(item: publicShareURL) {
                    Label("Share", systemImage: "square.and.arrow.up")
                }
            } else if model.discussion.isOwner != false {
                Button(action: onShare) {
                    Label("Share Link", systemImage: "square.and.arrow.up")
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
                    showingForceStopConfirm = true
                } label: {
                    Label(model.isForceStopping ? "Finalising" : "Force Stop",
                          systemImage: model.isForceStopping ? "hourglass" : "stop.fill")
                }
                .disabled(!model.canForceStop)
            }
            if let onSignOut {
                if hasNonSignOutActions {
                    Divider()
                }
                Button(role: .destructive, action: onSignOut) {
                    Label("Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
                }
            }
        } label: {
            Image(systemName: "ellipsis")
        }
        .accessibilityLabel("\(AppStringLiteral.stationNameRaw) actions")
        .popoverTip(actionsTip, arrowEdge: .top)
        .confirmationDialog(
            "Force stop this \(AppStringLiteral.stationNameRaw)?",
            isPresented: $showingForceStopConfirm,
            titleVisibility: .visible
        ) {
            Button("Force Stop", role: .destructive) {
                model.forceStop()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The current generation will stop after finalising audio that has already been created. New turns will not be generated.")
        }
    }

    private var hasNonSignOutActions: Bool {
        showsPoints
            || onCreateFromPlan != nil
            || model.discussion.isOwner != false
            || model.discussion.isPublic
            || model.canDownloadPodcast
            || model.showsForceStopAction
    }
}

struct PodcastLoadingMenu: View {
    let showsPoints: Bool
    let pointsMenuLabel: String
    let onShowPoints: () -> Void
    let isCreatingFromPlan: Bool
    let onCreateFromPlan: (() -> Void)?
    var onSignOut: (() -> Void)?

    var body: some View {
        Menu {
            if showsPoints {
                Button {
                    onShowPoints()
                } label: {
                    Label(pointsMenuLabel, systemImage: "sparkles")
                }
            }
            if showsPoints && onCreateFromPlan != nil {
                Divider()
            }
            if let onCreateFromPlan {
                Button(action: onCreateFromPlan) {
                    Label(isCreatingFromPlan ? "Creating" : "Create from Plan",
                          systemImage: isCreatingFromPlan ? "hourglass" : "plus.circle")
                }
                .disabled(isCreatingFromPlan)
            }
            if let onSignOut {
                if showsPoints || onCreateFromPlan != nil {
                    Divider()
                }
                Button(role: .destructive, action: onSignOut) {
                    Label("Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
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

private struct PlanSheetView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    @State private var discussion: Discussion
    @State private var showingSources = false
    @State private var showingSpeakerModels = false
    @State private var isLoadingFullPlan = false
    @State private var loadError: String?

    init(discussion: Discussion) {
        _discussion = State(initialValue: discussion)
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        PlanSnapshotCard(
                            label: "Plan",
                            snapshot: PlanSnapshot(discussion: discussion),
                            onSourcesTapped: { showingSources = true },
                            onEditModels: { showingSpeakerModels = true }
                        )
                        if isLoadingFullPlan && discussion.script == nil {
                            ProgressView()
                                .tint(Theme.accent)
                                .frame(maxWidth: .infinity, alignment: .center)
                                .padding(.top, 12)
                        } else if let loadError, discussion.script == nil {
                            Text(loadError)
                                .font(.footnote)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                    .padding(16)
                }
                .scrollDismissesKeyboard(.interactively)
            }
            .task(id: discussion.id) {
                await fetchFullPlanIfNeeded()
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
            .sheet(isPresented: $showingSpeakerModels) {
                SpeakerModelsSheet(discussion: $discussion, allowsEditing: false)
            }
        }
    }

    private func fetchFullPlanIfNeeded() async {
        guard discussion.script == nil else { return }
        isLoadingFullPlan = true
        defer { isLoadingFullPlan = false }
        do {
            discussion = try await APIClient(tokens: auth).discussion(id: discussion.id)
            loadError = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            loadError = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// A row in the transcript `MessageList`: either a live transcript line or the
/// trailing points-summary accessory.
private enum TranscriptListItem: Identifiable, MessageListItem {
    /// `isMine` is the current user's ownership of the line (server-owned identity,
    /// not the broad `LiveLine.isUser` "human-authored" flag). It drives both the
    /// bubble styling and `isUserMessage` pinning, so another participant's message
    /// is never pinned/scrolled as if it were my outgoing turn.
    case line(LiveLine, isMine: Bool)
    case usage(id: UUID, points: String)

    var id: UUID {
        switch self {
        case .line(let line, _): return line.id
        case .usage(let id, _): return id
        }
    }

    var isUserMessage: Bool {
        if case .line(_, let isMine) = self { return isMine }
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

/// One transcript message: the current user's own turns sit right in an accent
/// bubble (mockup image 4); everyone else — AI panelists *and* other human
/// participants — render left with an avatar + name header in their own color,
/// so a co-listener's comment reads as a distinct speaker rather than as my own
/// message. `isMine` (not `line.isUser`) drives this: other users also persist
/// with `isUser == true`, so the flag alone can't tell them apart from me.
private struct TranscriptBubble: View {
    let line: LiveLine
    /// True only when this line was authored by the current participant.
    let isMine: Bool

    private var speakerColor: Color { SpeakerPalette.color(for: line.speaker) }

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            if isMine { Spacer(minLength: 40) }
            if !isMine {
                SpeakerAvatar(speaker: line.speaker)
            }
            VStack(alignment: isMine ? .trailing : .leading, spacing: 4) {
                if !isMine {
                    HStack(spacing: 6) {
                        Text(line.speaker.uppercased())
                            .font(.caption2.weight(.bold))
                            .foregroundStyle(speakerColor)
                        // Tag a human participant's turn so a co-listener's comment
                        // is not mistaken for an AI panelist's line.
                        if line.isUser {
                            Text("USER", comment: "Badge marking a transcript line as written by a human participant, not an AI panelist")
                                .font(.system(size: 9, weight: .bold))
                                .foregroundStyle(speakerColor)
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .overlay {
                                    Capsule().strokeBorder(speakerColor.opacity(0.5), lineWidth: 0.5)
                                }
                        }
                    }
                }
                VStack(alignment: isMine ? .trailing : .leading, spacing: 8) {
                    if let audioURL = line.audioURL, !audioURL.isEmpty {
                        VoiceMessageControl(urlString: audioURL, isUser: isMine)
                    }
                    if line.hasDisplayText {
                        bubbleText
                    } else if line.hasAudio {
                        Text("Audio message", comment: "Fallback label for a voice message whose transcript is unavailable")
                            .font(.caption.weight(.medium))
                            .foregroundStyle((isMine ? Color.white : speakerColor).opacity(0.78))
                    }
                }
                .font(.body)
                .padding(12)
                .background(bubbleStyle, in: .rect(cornerRadius: 18))
                .overlay {
                    RoundedRectangle(cornerRadius: 18)
                        .strokeBorder(isMine ? .clear : speakerColor.opacity(0.28),
                                      lineWidth: 0.5)
                }
                .foregroundStyle(isMine ? .white : .primary)
            }
            if !isMine { Spacer(minLength: 40) }
        }
    }

    /// My bubbles get an accent gradient for depth; everyone else takes a soft
    /// tint of their speaker color so each speaker's turns are recognizable.
    private var bubbleStyle: AnyShapeStyle {
        if isMine {
            AnyShapeStyle(LinearGradient(
                colors: [Theme.accent, Theme.accent.opacity(0.82)],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            ))
        } else {
            AnyShapeStyle(speakerColor.opacity(0.14))
        }
    }

    /// Human messages (mine or another participant's) are plain typed text, so we
    /// render them with `Text`, which hugs its content — otherwise `MarkdownText`'s
    /// block layout greedily fills the row and leaves the bubble far wider than the
    /// message. Only agent lines actually contain markdown.
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
            HStack(spacing: 12) {
                ZStack {
                    Circle()
                        .fill(Theme.accent.opacity(0.14))
                    Image(systemName: "sparkles")
                        .font(.system(size: 15, weight: .bold))
                        .foregroundStyle(Theme.accent)
                }
                .frame(width: 38, height: 38)

                VStack(alignment: .leading, spacing: 2) {
                    Text("Points used")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.secondaryText)
                    Text(points)
                        .font(.headline.weight(.bold))
                        .foregroundStyle(.primary)
                        .monospacedDigit()
                }
            }
            .padding(.leading, 10)
            .padding(.trailing, 16)
            .padding(.vertical, 10)
            .background {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .fill(LinearGradient(
                        colors: [
                            Theme.accent.opacity(0.13),
                            Color(uiColor: .secondarySystemBackground)
                        ],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    ))
            }
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .strokeBorder(Theme.accent.opacity(0.18), lineWidth: 0.75)
            }
            .shadow(color: Theme.accent.opacity(0.08), radius: 10, x: 0, y: 5)
            .accessibilityElement(children: .combine)
            .accessibilityLabel("Points used: \(points)")
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

    private func items() -> [TranscriptListItem] { lines.map { .line($0, isMine: $0.isUser) } }

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            MessageList(
                messages: items(),
                isStreaming: isStreaming,
                shouldScrollToBottom: shouldScrollToBottom,
                isAtBottom: $isAtBottom
            ) { item in
                if case .line(let line, let isMine) = item {
                    TranscriptBubble(line: line, isMine: isMine)
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
