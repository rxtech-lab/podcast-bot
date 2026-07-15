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

private let transcriptImageLog = Logger(subsystem: "com.debatebot.ios", category: "TranscriptImage")
private let textContentLog = Logger(subsystem: "com.debatebot.ios", category: "TextContent")

/// The live podcast screen: streaming per-agent transcript bubbles, a synced
/// caption, a Liquid Glass music-player bar, and a message input — matching the
/// mockups.
struct PodcastPlayerView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(PlayerSessionStore.self) private var playerSessions
    @Environment(\.scenePhase) private var scenePhase
    let discussion: Discussion
    var onCreatedFromPlan: ((Discussion) -> Void)?
    var onCreatedFollowUp: ((Discussion) -> Void)?
    /// Non-nil when this discussion was opened via a private share link; passed
    /// to the player model so a non-owner participant's comments are authorized.
    var shareToken: String? = nil
    /// Marketplace detail pages keep the player toolbar, but hide the root tab bar.
    var hidesTabBar: Bool = false
    var onSignOut: (() -> Void)?

    @State private var playerSession: PlayerSession?
    @State private var message = ""
    @State private var showingPlan = false
    @State private var showingSummary = false
    @State private var showingText = false
    @State private var showingMindmap = false
    @State private var audioBookVideoURL: IdentifiableURL?
    @State private var showingImporter = false
    @State private var showingPhotos = false
    @State private var showingPointsHistory = false
    @State private var showingPublishSheet = false
    @State private var showingCoverEditor = false
    @State private var showingShareSheet = false
    @State private var showingCreatorProfile = false
    @State private var showingFollowUpForm = false
    @State private var showingChapterChecklist = false
    @State private var showingAlbum = false
    @State private var showingAlbumPicker = false
    @State private var chapterProgress: ChaptersResponse?
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var showingRecorder = false
    @State private var selectedTranscriptSources: TranscriptSourcesSelection?
    @State private var selectedTranscriptImageURL: IdentifiableURL?
    @State private var resumePlaybackAfterRecorder = false
    @State private var isUploadingAttachment = false
    @State private var isCreatingFromPlan = false
    @State private var createFromPlanError: String?
    @State private var isGeneratingSummary = false
    @State private var summaryGenerateError: String?
    @State private var isGeneratingMindmap = false
    @State private var mindmapGenerateError: String?
    @State private var isGeneratingVideo = false
    @State private var isVideoGenerationPending = false
    @State private var videoGenerateError: String?
    @State private var documentActionItems: [DiscussionUIActionItem] = []
    @State private var podcastActionItems: [DiscussionUIActionItem] = []
    @State private var showingForceStopConfirm = false
    @State private var transcriptIsAtBottom = true
    @State private var transcriptShouldScrollToBottom = false
    @State private var transcriptScrollRequestTask: Task<Void, Never>?

    /// Stable id for the optional points-summary accessory row so it doesn't
    /// churn its identity across renders.
    private static let usageItemID = UUID()
    /// Stable id for the optional "generate more chapters" accessory row.
    private static let generateMoreItemID = UUID()

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
        .toolbar(hidesTabBar ? .hidden : .visible, for: .tabBar)
        .modifier(CreateFromPlanErrorAlert(error: $createFromPlanError))
        .modifier(SummaryGenerateErrorAlert(error: $summaryGenerateError))
        .modifier(MindmapGenerateErrorAlert(error: $mindmapGenerateError))
        .modifier(VideoGenerateErrorAlert(error: $videoGenerateError))
        .sheet(isPresented: $showingPlan) {
            PlanSheetView(discussion: currentDiscussion)
        }
        .sheet(isPresented: $showingSummary) {
            summarySheet
        }
        .sheet(isPresented: $showingText) {
            textSheet
        }
        .sheet(isPresented: $showingMindmap) {
            mindmapSheet
        }
        .fullScreenCover(item: $audioBookVideoURL) { item in
            AudioBookVideoView(url: item.url)
        }
        .fullScreenCover(item: $selectedTranscriptImageURL) { item in
            TranscriptImageFullScreenView(url: item.url)
        }
        .sheet(item: $selectedTranscriptSources) { selection in
            SourcesSheet(
                discussion: discussionForTranscriptSources(selection.sources),
                allowsAddingSources: false
            )
        }
        .sheet(isPresented: $showingPointsHistory) {
            PointsHistoryView()
        }
        .sheet(isPresented: $showingPublishSheet) {
            publishStationSheet
        }
        .sheet(isPresented: $showingCoverEditor) {
            coverEditorSheet
        }
        .sheet(isPresented: $showingShareSheet) {
            shareSheet
        }
        .sheet(isPresented: $showingCreatorProfile) {
            creatorProfileSheet
        }
        .sheet(isPresented: $showingFollowUpForm) {
            followUpFormSheet
        }
        .sheet(isPresented: $showingChapterChecklist) {
            chapterChecklistSheet
        }
        .sheet(isPresented: $showingAlbum) {
            albumSheet
        }
        .sheet(isPresented: $showingAlbumPicker) {
            albumPickerSheet
        }
        .sheet(isPresented: downloadDialogBinding) {
            downloadProgressSheet
        }
        .sheet(item: downloadedPodcastFileBinding) { file in
            fileShareSheet(file)
        }
        .fullScreenCover(isPresented: fullPlayerPresentedBinding) {
            fullPlayerCover
        }
        .task(id: playerSessionTaskKey) {
            await loadPlayerIfNeeded()
        }
        .task(id: uiActionsRefreshKey) {
            await loadUIActions()
        }
        .onDisappear {
            stopPlayerIfNeeded()
        }
        .onChange(of: scenePhase) { _, phase in
            handleScenePhaseChange(phase)
        }
        .confirmationDialog(
            "Force stop this \(AppStringLiteral.stationNameRaw)?",
            isPresented: $showingForceStopConfirm,
            titleVisibility: .visible
        ) {
            Button("Force Stop", role: .destructive) {
                model?.forceStop()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The current generation will stop after finalising audio that has already been created. New turns will not be generated.")
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
            DiscussionActionsMenu(
                items: documentActionItems,
                labelSystemImage: "doc.text",
                accessibilityLabel: "Documents",
                isBusy: isDocumentActionBusy,
                perform: performDocumentAction
            )
            .accessibilityIdentifier("player.documents")
            .popoverTip(PodcastPlanTip(), arrowEdge: .top)
        }
        if showsActionsMenu {
            ToolbarItem(placement: .topBarTrailing) {
                if let model {
                    DiscussionActionsMenu(
                        items: podcastActionItems,
                        labelSystemImage: "ellipsis",
                        accessibilityLabel: "\(AppStringLiteral.stationNameRaw) actions",
                        titleOverride: podcastActionTitle,
                        isBusy: isPodcastActionBusy,
                        perform: performPodcastAction
                    )
                    .accessibilityIdentifier("player.more")
                    .popoverTip(podcastActionsTip(for: model), arrowEdge: .top)
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

    private var model: PlayerModel? {
        playerSession?.model
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

    private var uiActionsRefreshKey: String {
        let d = currentDiscussion
        return [
            d.id,
            d.status.rawValue,
            d.visibility?.rawValue ?? "",
            d.isOwner == true ? "owner" : "viewer",
            d.summary?.status?.rawValue ?? "",
            d.summary?.available == true ? "summary-ready" : "summary-not-ready",
            d.summary?.pending == true ? "summary-pending" : "summary-not-pending",
            d.summary?.generation == true ? "summary-generation" : "summary-no-generation",
            d.mindmap?.status?.rawValue ?? "",
            d.mindmap?.available == true ? "mindmap-ready" : "mindmap-not-ready",
            d.mindmap?.pending == true ? "mindmap-pending" : "mindmap-not-pending",
            d.mindmap?.generation == true ? "mindmap-generation" : "mindmap-no-generation",
            d.jobID ?? "",
            d.downloadURLString ?? "",
            d.albumID ?? "",
            "\(model?.uiActionsRefreshVersion ?? 0)",
            onCreatedFollowUp == nil ? "no-follow-up" : "follow-up",
            onCreatedFromPlan == nil ? "no-create-from-plan" : "create-from-plan",
            onSignOut == nil ? "no-sign-out" : "sign-out"
        ].joined(separator: "|")
    }

    private var playerSessionTaskKey: String {
        "\(discussion.id)|\(shareToken ?? "")"
    }

    /// Extracted so the construction of `SummaryView` (and its `APIClient`) stays
    /// out of the main `body` modifier chain, which is large enough that inlining
    /// it pushes the SwiftUI type-checker past its time budget.
    private var summarySheet: some View {
        SummaryView(discussionID: currentDiscussion.id,
                    title: currentDiscussion.displayTitle,
                    mindmapEditable: currentDiscussion.isOwner == true,
                    api: APIClient(tokens: auth))
    }

    /// The audiobook "text-based content" book view (narration + illustrations).
    /// Kept out of `body` for the same type-checker reason as
    /// `summarySheet`.
    private var textSheet: some View {
        TextContentView(discussionID: currentDiscussion.id,
                        title: currentDiscussion.displayTitle,
                        api: APIClient(tokens: auth))
    }

    /// The discussion mindmap editor. Kept out of `body` for the same
    /// type-checker reason as `summarySheet`.
    private var mindmapSheet: some View {
        MindmapView(discussionID: currentDiscussion.id,
                    title: currentDiscussion.displayTitle,
                    isEditable: currentDiscussion.isOwner == true,
                    api: APIClient(tokens: auth))
    }

    private func loadUIActions() async {
        let api = APIClient(tokens: auth)
        do {
            let documents = try await api.discussionUIActions(id: currentDiscussion.id,
                                                              surface: "podcast-documents")
            documentActionItems = documents.items
            reconcileVideoGenerationPending(with: documents.items)
        } catch {
            documentActionItems = []
        }
        do {
            let actions = try await api.discussionUIActions(
                id: currentDiscussion.id,
                surface: "podcast-actions",
                supportsPoints: purchases.isConfigured,
                supportsFollowUp: onCreatedFollowUp != nil,
                supportsCreateFromPlan: onCreatedFromPlan != nil,
                supportsSignOut: onSignOut != nil,
                supportsChapterBatches: onCreatedFollowUp != nil,
                supportsAlbums: true
            )
            podcastActionItems = actions.items
        } catch {
            podcastActionItems = []
        }
        await refreshChapterProgress()
    }

    /// Loads which chapters of the audiobook chain are generated/pending; the
    /// pending set drives the "Generate more chapters" transcript footer.
    private func refreshChapterProgress() async {
        guard currentDiscussion.script?.type == "audio-book",
              currentDiscussion.status == .ready,
              currentDiscussion.isOwner != false,
              onCreatedFollowUp != nil else {
            chapterProgress = nil
            return
        }
        chapterProgress = try? await APIClient(tokens: auth).discussionChapters(id: currentDiscussion.id)
    }

    private func performDocumentAction(_ item: DiscussionUIActionItem) {
        if openVideoAction(item) {
            return
        }
        guard let path = validatedDiscussionActionPath(item) else { return }
        switch path {
        case ["sheet", "plan"]:
            showingPlan = true
        case ["sheet", "summary"]:
            showingSummary = true
        case ["sheet", "text"]:
            showingText = true
        case ["sheet", "mindmap"]:
            showingMindmap = true
        case ["action", "summary-generate"]:
            generateSummary()
        case ["action", "mindmap-generate"]:
            generateMindmap()
        case ["action", "video-generate"]:
            generateVideo()
        default:
            break
        }
    }

    private func performPodcastAction(_ item: DiscussionUIActionItem) {
        if openVideoAction(item) {
            return
        }
        guard let path = validatedDiscussionActionPath(item) else { return }
        switch path {
        case ["sheet", "points"]:
            showingPointsHistory = true
        case ["sheet", "publish"]:
            showingPublishSheet = true
        case ["sheet", "cover"]:
            showingCoverEditor = true
        case ["sheet", "share"]:
            showingShareSheet = true
        case ["sheet", "follow-up"]:
            showingFollowUpForm = true
        case ["sheet", "generate-chapters"]:
            showingChapterChecklist = true
        case ["sheet", "album"]:
            showingAlbum = true
        case ["sheet", "add-to-album"]:
            showingAlbumPicker = true
        case ["action", "create-from-plan"]:
            createFromPlan()
        case ["action", "make-private"]:
            if let model { makePrivate(model) }
        case ["action", "download-podcast"]:
            model?.downloadPodcast()
        case ["action", "force-stop"]:
            showingForceStopConfirm = true
        case ["action", "sign-out"]:
            onSignOut?()
        default:
            break
        }
    }

    private func openVideoAction(_ item: DiscussionUIActionItem) -> Bool {
        // The video action carries a raw playback URL (not a debatepod deep
        // link), so handle it before the path-based routing.
        guard item.action.type == "play-video" else { return false }
        if let url = URL(string: item.action.link) {
            audioBookVideoURL = IdentifiableURL(url: url)
        }
        return true
    }

    private func validatedDiscussionActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "discussion" else { return nil }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == currentDiscussion.id else { return nil }
        return Array(components.dropFirst())
    }

    private func isDocumentActionBusy(_ item: DiscussionUIActionItem) -> Bool {
        switch item.id {
        case "generate-summary":
            return isGeneratingSummary
        case "generate-mindmap":
            return isGeneratingMindmap
        case "generate-video":
            return isGeneratingVideo || isVideoGenerationPending
        default:
            return false
        }
    }

    private func reconcileVideoGenerationPending(with items: [DiscussionUIActionItem]) {
        guard isVideoGenerationPending else { return }
        if items.contains(where: { $0.id == "video-rendering" || $0.id == "view-video" }) {
            isVideoGenerationPending = false
        }
    }

    private func isPodcastActionBusy(_ item: DiscussionUIActionItem) -> Bool {
        switch item.id {
        case "create-from-plan":
            return isCreatingFromPlan
        case "download-podcast":
            return model?.isDownloadingPodcast == true
        case "force-stop":
            return model?.isForceStopping == true
        default:
            return false
        }
    }

    private func podcastActionTitle(_ item: DiscussionUIActionItem) -> String? {
        item.id == "points" ? pointsMenuLabel : nil
    }

    private func podcastActionsTip(for model: PlayerModel) -> (any Tip)? {
        if model.discussion.isPublic {
            return ShareStationTip()
        }
        if model.discussion.isOwner != false {
            return PublishToMarketTip()
        }
        return nil
    }

    @ViewBuilder
    private var publishStationSheet: some View {
        if let model {
            PublishStationSheet(discussion: discussionBinding(for: model))
        }
    }

    @ViewBuilder
    private var coverEditorSheet: some View {
        if let model {
            CoverEditorSheet(discussion: discussionBinding(for: model))
        }
    }

    private var shareSheet: some View {
        ShareSheet(discussionID: currentDiscussion.id,
                   api: APIClient(tokens: auth))
    }

    @ViewBuilder
    private var creatorProfileSheet: some View {
        if let creator = currentCreator {
            CreatorProfileView(creatorID: creator.id,
                               initialProfile: creator,
                               onCreateFromPlan: onCreatedFromPlan)
        }
    }

    private var followUpFormSheet: some View {
        NewDiscussionView(reference: currentPodcastReference) { created in
            showingFollowUpForm = false
            onCreatedFollowUp?(created)
        }
    }

    /// Chapter batch picker for "generate more chapters": creates a follow-up
    /// podcast narrating the checked chapters. The server's 400 (over the
    /// 5-chapter batch limit, or chapters already generated) surfaces as an
    /// alert inside the sheet.
    private var chapterChecklistSheet: some View {
        ChapterChecklistSheet(mode: .discussion(id: currentDiscussion.id)) { indices in
            let created = try await APIClient(tokens: auth).generateChapters(id: currentDiscussion.id, chapters: indices)
            showingChapterChecklist = false
            await refreshChapterProgress()
            onCreatedFollowUp?(created)
        }
    }

    private var albumSheet: some View {
        NavigationStack {
            AlbumView(albumID: currentDiscussion.albumID ?? chapterProgress?.albumID ?? "",
                      ownsNavigation: true,
                      mode: currentDiscussion.isOwner == true ? .owner : .publicMarket)
        }
    }

    private var albumPickerSheet: some View {
        AlbumPickerSheet(discussion: currentDiscussion) { album in
            showingAlbumPicker = false
            model?.discussion.albumID = album.id
        }
    }

    @ViewBuilder
    private var downloadProgressSheet: some View {
        if let model {
            DownloadProgressSheet(model: model)
        }
    }

    @ViewBuilder
    private var fullPlayerCover: some View {
        if let playerSession {
            FullScreenPlayerView(model: playerSession.model)
        }
    }

    private var fullPlayerPresentedBinding: Binding<Bool> {
        Binding(
            get: { playerSession?.isFullPlayerPresented == true },
            set: { playerSession?.isFullPlayerPresented = $0 }
        )
    }

    private func discussionBinding(for model: PlayerModel) -> Binding<Discussion> {
        Binding(
            get: { model.discussion },
            set: { model.discussion = $0 }
        )
    }

    private var downloadDialogBinding: Binding<Bool> {
        Binding(
            get: { model?.showsDownloadDialog == true },
            set: { isPresented in
                if !isPresented { model?.showsDownloadDialog = false }
            }
        )
    }

    private var downloadedPodcastFileBinding: Binding<DownloadedPodcastFile?> {
        Binding(
            get: { model?.downloadedPodcastFile },
            set: { model?.downloadedPodcastFile = $0 }
        )
    }

    private func fileShareSheet(_ file: DownloadedPodcastFile) -> some View {
        FileShareSheet(url: file.url)
    }

    private var currentPodcastReference: PodcastReference {
        PodcastReference(id: currentDiscussion.id,
                         title: currentDiscussion.displayTitle,
                         topic: currentDiscussion.topic)
    }

    private var showsActionsMenu: Bool {
        purchases.isConfigured
            || model?.showsPodcastActions == true
            || !podcastActionItems.isEmpty
            || onCreatedFromPlan != nil
            || onCreatedFollowUp != nil
            || onSignOut != nil
    }

    private var createFollowUpAction: (() -> Void)? {
        guard onCreatedFollowUp != nil else { return nil }
        return { showingFollowUpForm = true }
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

    private func loadPlayerIfNeeded() async {
        let session = playerSessions.acquire(
            discussion: discussion,
            api: APIClient(tokens: auth),
            username: auth.currentUser?.name ?? "You",
            userID: auth.currentUser?.id ?? "",
            shareToken: shareToken
        )
        if let playerSession, playerSession !== session {
            playerSessions.release(playerSession)
        }
        playerSession = session
        await purchases.refreshBalance()
    }

    private func stopPlayerIfNeeded() {
        guard let playerSession else { return }
        playerSessions.release(playerSession)
    }

    private func handleScenePhaseChange(_ phase: ScenePhase) {
        // Returning to the foreground while the job is live: the socket may have
        // been torn down while suspended, so reconcile immediately.
        guard phase == .active else { return }
        model?.foregroundRefresh()
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
            TranscriptBubble(
                line: line,
                isMine: isMine,
                speakerColor: SpeakerPalette.color(for: line.speaker, in: model?.lines ?? [line])
            ) { sources in
                selectedTranscriptSources = TranscriptSourcesSelection(sources: sources)
            } onImageTapped: { url in
                selectedTranscriptImageURL = IdentifiableURL(url: url)
            }
        case .usage(_, let points):
            PointsSummaryBubble(points: points)
        case .generateMore(_, let pendingCount):
            generateMoreChaptersRow(pendingCount: pendingCount)
        }
    }

    /// Trailing transcript call-to-action shown when the audiobook chain still
    /// has pending chapters; opens the chapter checklist sheet.
    private func generateMoreChaptersRow(pendingCount: Int) -> some View {
        HStack {
            Spacer(minLength: 0)
            Button {
                showingChapterChecklist = true
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: "text.badge.plus")
                    Text("Generate more chapters (\(pendingCount) left)")
                        .font(.subheadline.weight(.semibold))
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 11)
                .background(Theme.accent.opacity(0.12), in: .capsule)
                .overlay {
                    Capsule().strokeBorder(Theme.accent.opacity(0.35), lineWidth: 1)
                }
                .foregroundStyle(Theme.accent)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("player.generateMoreChapters")
            Spacer(minLength: 0)
        }
        .padding(.vertical, 6)
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
        var items = PlayerModel.visibleTranscriptLines(model.lines)
            .map { TranscriptListItem.line($0, isMine: isMyLine($0)) }
        // Show only the points this podcast consumed (planning + generation),
        // never the underlying token/cost detail. Points are known once the
        // discussion is charged (after generation completes).
        if let points = model.discussion.pointsText {
            items.append(.usage(id: Self.usageItemID, points: points))
        }
        // Audiobooks with ungenerated chapters end the transcript with a
        // "generate more chapters" call-to-action.
        if let pending = chapterProgress?.pendingChapters.count, pending > 0,
           model.discussion.status == .ready {
            items.append(.generateMore(id: Self.generateMoreItemID, pendingCount: pending))
        }
        return items
    }

    private func discussionForTranscriptSources(_ sources: [SourceDTO]) -> Discussion {
        var copy = currentDiscussion
        copy.sources = sources
        return copy
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
            MusicPlayerBar(model: model) { playerSession?.isFullPlayerPresented = true }
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

    private func generateMindmap() {
        guard !isGeneratingMindmap else { return }
        isGeneratingMindmap = true
        Task { @MainActor in
            defer { isGeneratingMindmap = false }
            do {
                let updated = try await APIClient(tokens: auth).generateMindmap(id: currentDiscussion.id)
                if let model {
                    model.discussion = PlayerModel.mergingLocalDiscussionState(
                        current: model.discussion,
                        fresh: updated
                    )
                    model.listenForJobUpdatesIfNeeded()
                }
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                mindmapGenerateError = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func generateVideo() {
        guard !isGeneratingVideo && !isVideoGenerationPending else { return }
        isGeneratingVideo = true
        Task { @MainActor in
            do {
                let updated = try await APIClient(tokens: auth).generateVideo(id: currentDiscussion.id)
                if let model {
                    model.discussion = PlayerModel.mergingLocalDiscussionState(
                        current: model.discussion,
                        fresh: updated
                    )
                    model.listenForJobUpdatesIfNeeded()
                }
                isVideoGenerationPending = true
                await loadUIActions()
                isGeneratingVideo = false
            } catch {
                isGeneratingVideo = false
                isVideoGenerationPending = false
                guard !APIClient.isCancellation(error) else { return }
                videoGenerateError = (error as? APIError)?.errorDescription ?? error.localizedDescription
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

private struct CreateFromPlanErrorAlert: ViewModifier {
    @Binding var error: String?

    private var isPresented: Binding<Bool> {
        Binding(
            get: { error != nil },
            set: { if !$0 { error = nil } }
        )
    }

    func body(content: Content) -> some View {
        content
            .alert("Could not create \(AppStringLiteral.stationNameRaw)", isPresented: isPresented) {
                Button("OK", role: .cancel) { error = nil }
            } message: {
                Text(error ?? "")
            }
    }
}

private struct SummaryGenerateErrorAlert: ViewModifier {
    @Binding var error: String?

    private var isPresented: Binding<Bool> {
        Binding(
            get: { error != nil },
            set: { if !$0 { error = nil } }
        )
    }

    func body(content: Content) -> some View {
        content
            .alert("Could not generate summary", isPresented: isPresented) {
                Button("OK", role: .cancel) { error = nil }
            } message: {
                Text(error ?? "")
            }
    }
}

private struct MindmapGenerateErrorAlert: ViewModifier {
    @Binding var error: String?

    private var isPresented: Binding<Bool> {
        Binding(
            get: { error != nil },
            set: { if !$0 { error = nil } }
        )
    }

    func body(content: Content) -> some View {
        content
            .alert("Could not generate mindmap", isPresented: isPresented) {
                Button("OK", role: .cancel) { error = nil }
            } message: {
                Text(error ?? "")
            }
    }
}

private struct VideoGenerateErrorAlert: ViewModifier {
    @Binding var error: String?

    private var isPresented: Binding<Bool> {
        Binding(
            get: { error != nil },
            set: { if !$0 { error = nil } }
        )
    }

    func body(content: Content) -> some View {
        content
            .alert("Could not generate video", isPresented: isPresented) {
                Button("OK", role: .cancel) { error = nil }
            } message: {
                Text(error ?? "")
            }
    }
}

struct DiscussionActionsMenu: View {
    let items: [DiscussionUIActionItem]
    let labelSystemImage: String
    let accessibilityLabel: String
    var titleOverride: (DiscussionUIActionItem) -> String? = { _ in nil }
    let isBusy: (DiscussionUIActionItem) -> Bool
    let perform: (DiscussionUIActionItem) -> Void

    var body: some View {
        Menu {
            if items.isEmpty {
                Label("Loading", systemImage: "hourglass")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(items) { item in
                    actionRow(item)
                }
            }
        } label: {
            Image(systemName: labelSystemImage)
        }
        .accessibilityLabel(accessibilityLabel)
    }

    @ViewBuilder
    private func actionRow(_ item: DiscussionUIActionItem) -> some View {
        if item.children.count > 1 {
            Menu {
                ForEach(item.children) { child in
                    leafActionRow(child)
                }
            } label: {
                rowLabel(item, busy: false)
            }
            .disabled(!item.enabled)
        } else if let child = item.children.first {
            leafActionRow(child)
        } else {
            leafActionRow(item)
        }
    }

    @ViewBuilder
    private func leafActionRow(_ item: DiscussionUIActionItem) -> some View {
        if item.isDivider {
            Divider()
        } else {
            let busy = isBusy(item)
            let disabled = !item.enabled || busy
            if item.action.type == "share-link", let url = URL(string: item.action.link) {
                ShareLink(item: url) {
                    rowLabel(item, busy: busy)
                }
                .disabled(disabled)
            } else {
                Button(role: buttonRole(for: item)) {
                    perform(item)
                } label: {
                    rowLabel(item, busy: busy)
                }
                .disabled(disabled)
            }
        }
    }

    @ViewBuilder
    private func rowLabel(_ item: DiscussionUIActionItem, busy: Bool) -> some View {
        let title = busy ? (item.loadingTitle ?? titleOverride(item) ?? item.title) : (titleOverride(item) ?? item.title)
        if let systemImage = item.systemImage, !systemImage.isEmpty {
            Label(title, systemImage: busy && item.loadingTitle != nil ? "hourglass" : systemImage)
        } else {
            Text(title)
        }
    }

    private func buttonRole(for item: DiscussionUIActionItem) -> ButtonRole? {
        item.role == "destructive" ? .destructive : nil
    }
}

struct PodcastActionsMenu: View {
    @Bindable var model: PlayerModel
    @Environment(EntitlementsManager.self) private var entitlements
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
    let onCreateFollowUp: (() -> Void)?
    let isCreatingFromPlan: Bool
    let onCreateFromPlan: (() -> Void)?
    var onSignOut: (() -> Void)?

    /// The plain, permanent public link for a published discussion. The server
    /// builds it (`share_url`, the same `/p/{id}` web-player URL embedded as the
    /// summary's "listen again" link) so the shared link and the markdown link
    /// always match. Falls back to building it locally only if an older server
    /// response omits the field.
    private var publicShareURL: URL {
        if let raw = model.discussion.shareURL,
           let url = URL(string: raw) {
            return url
        }
        return AppConfig.websiteBaseURL.appendingPathComponent("p").appendingPathComponent(model.discussion.id)
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
            if showsPoints && (model.showsPodcastActions || onCreateFollowUp != nil || onCreateFromPlan != nil) {
                Divider()
            }
            if let onCreateFollowUp {
                Button(action: onCreateFollowUp) {
                    Label("Create Follow-up", systemImage: "arrow.triangle.branch")
                }
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
                .disabled(!entitlements.features.canGenerateCoverWithAI)
                if model.discussion.isPublic {
                    Button(role: .destructive, action: onMakePrivate) {
                        Label("Make Private", systemImage: "lock")
                    }
                } else {
                    Button(action: onPublish) {
                        Label("Publish to Market", systemImage: "globe")
                    }
                    .disabled(!entitlements.features.canPublishPodcast)
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
                .disabled(!entitlements.features.canSharePodcastPrivately)
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
        .accessibilityIdentifier("player.more")
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
            || onCreateFollowUp != nil
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
    @State private var selectedChapters: PlanChaptersPresentation?
    @State private var selectedTranscript: UploadedAudioTranscriptPresentation?
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
                            onChaptersTapped: {
                                let snapshot = PlanSnapshot(discussion: discussion)
                                if snapshot.isUploadedAudio {
                                    selectedTranscript = UploadedAudioTranscriptPresentation(snapshot: snapshot)
                                } else {
                                    selectedChapters = PlanChaptersPresentation(title: snapshot.title, chapters: snapshot.chapters)
                                }
                            },
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
            .sheet(item: $selectedChapters) { presentation in
                AudioBookChaptersSheet(presentation: presentation)
            }
            .sheet(item: $selectedTranscript) { presentation in
                UploadedAudioTranscriptSheet(
                    discussionID: discussion.id,
                    presentation: presentation,
                    allowsEditing: false
                )
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
    case generateMore(id: UUID, pendingCount: Int)

    var id: UUID {
        switch self {
        case .line(let line, _): return line.id
        case .usage(let id, _): return id
        case .generateMore(let id, _): return id
        }
    }

    var isUserMessage: Bool {
        if case .line(_, let isMine) = self { return isMine }
        return false
    }

    /// The points summary is an accessory — it never participates in user-message
    /// pinning.
    var isMessageListAccessory: Bool {
        switch self {
        case .usage, .generateMore: return true
        case .line: return false
        }
    }
}

private struct TranscriptSourcesSelection: Identifiable {
    let id = UUID()
    var sources: [SourceDTO]
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
        colors[index(for: speaker)]
    }

    static func color(for speaker: String, in lines: [LiveLine]) -> Color {
        colors[index(for: speaker, in: lines)]
    }

    static func index(for speaker: String) -> Int {
        guard !normalizedSpeaker(speaker).isEmpty else { return 0 }
        // djb2 — stable across launches so a speaker keeps the same color.
        var hash = 5381
        for scalar in speaker.unicodeScalars {
            hash = (hash &* 33) &+ Int(scalar.value)
        }
        return abs(hash) % colors.count
    }

    static func index(for speaker: String, in lines: [LiveLine]) -> Int {
        let target = normalizedSpeaker(speaker)
        guard !target.isEmpty else { return index(for: speaker) }
        var seen: [String: Int] = [:]
        var nextIndex = 0
        for line in lines {
            let key = normalizedSpeaker(line.speaker)
            guard !key.isEmpty, seen[key] == nil else { continue }
            seen[key] = nextIndex
            nextIndex += 1
        }
        return seen[target].map { $0 % colors.count } ?? index(for: speaker)
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

    private static func normalizedSpeaker(_ speaker: String) -> String {
        speaker.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}

/// A small gradient avatar with the speaker's initials in their palette color.
private struct SpeakerAvatar: View {
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
private struct TranscriptBubble: View {
    let line: LiveLine
    /// True only when this line was authored by the current participant.
    let isMine: Bool
    let speakerColor: Color
    var onSourcesTapped: ([SourceDTO]) -> Void = { _ in }
    var onImageTapped: (URL) -> Void = { _ in }

    private var sources: [SourceDTO] { line.sources ?? [] }
    private var judgementComment: String {
        line.judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    init(line: LiveLine,
         isMine: Bool,
         speakerColor: Color? = nil,
         onSourcesTapped: @escaping ([SourceDTO]) -> Void = { _ in },
         onImageTapped: @escaping (URL) -> Void = { _ in }) {
        self.line = line
        self.isMine = isMine
        self.speakerColor = speakerColor ?? SpeakerPalette.color(for: line.speaker)
        self.onSourcesTapped = onSourcesTapped
        self.onImageTapped = onImageTapped
    }

    var body: some View {
        Group {
            if line.hasRenderablePayload {
                HStack(alignment: .top, spacing: 8) {
                    if isMine { Spacer(minLength: 40) }
                    if !isMine {
                        SpeakerAvatar(speaker: line.speaker, color: speakerColor)
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
                            if line.hasImage {
                                let urlStr = line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                                if let url = URL(string: urlStr) {
                                    TranscriptImageBubble(url: url, line: line, speakerColor: speakerColor) {
                                        onImageTapped(url)
                                    }
                                } else {
                                    transcriptImagePlaceholder(speakerColor: speakerColor)
                                        .onAppear {
                                            transcriptImageLog.error(
                                                "Transcript image URL invalid line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) rawLength=\(urlStr.count, privacy: .public)"
                                            )
                                        }
                                }
                            }
                            if line.hasDisplayText {
                                bubbleText
                            } else if line.hasAudio {
                                Text("Audio message", comment: "Fallback label for a voice message whose transcript is unavailable")
                                    .font(.caption.weight(.medium))
                                    .foregroundStyle((isMine ? Color.white : speakerColor).opacity(0.78))
                            }
                            if !sources.isEmpty {
                                Button {
                                    onSourcesTapped(sources)
                                } label: {
                                    Label("Sources", systemImage: "link")
                                        .font(.caption.weight(.semibold))
                                }
                                .buttonStyle(.bordered)
                                .controlSize(.small)
                                .tint(isMine ? .white.opacity(0.9) : speakerColor)
                            }
                            if !judgementComment.isEmpty {
                                HStack(alignment: .top, spacing: 6) {
                                    Image(systemName: "exclamationmark.triangle.fill")
                                        .font(.caption2.weight(.bold))
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text("Judgement")
                                            .font(.caption2.weight(.bold))
                                            .textCase(.uppercase)
                                        Text(judgementComment)
                                            .font(.caption)
                                    }
                                }
                                .foregroundStyle(isMine ? Color.white.opacity(0.82) : Color.orange)
                                .padding(.top, 2)
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
            Text(line.displayText)
        } else {
            MarkdownText(line.displayText)
        }
    }
}

private struct TranscriptImageBubble: View {
    let url: URL
    let line: LiveLine
    let speakerColor: Color
    var onTap: () -> Void = {}
    @State private var didFail = false

    var body: some View {
        Group {
            if didFail {
                transcriptImagePlaceholder(speakerColor: speakerColor)
                    .overlay { Image(systemName: "photo").foregroundStyle(speakerColor) }
            } else {
                KFImage.url(url)
                    .placeholder {
                        transcriptImagePlaceholder(speakerColor: speakerColor)
                            .overlay { ProgressView() }
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .onSuccess { _ in
                        didFail = false
                        transcriptImageLog.info(
                            "Transcript image loaded line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public)"
                        )
                    }
                    .onFailure { error in
                        didFail = true
                        transcriptImageLog.error(
                            "Transcript image failed line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public) error=\(error.localizedDescription, privacy: .public)"
                        )
                    }
                    .resizable()
                    .scaledToFit()
            }
        }
        .onAppear {
            transcriptImageLog.info(
                "Transcript image requested line=\(line.id.uuidString, privacy: .public) speaker=\(line.speaker, privacy: .public) url=\(redactedURLDescription(url), privacy: .public)"
            )
        }
        .onChange(of: url.absoluteString) { _, _ in
            didFail = false
        }
        .frame(maxWidth: 280)
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onTapGesture(perform: onTap)
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("Open image")
    }
}

private struct TranscriptImageFullScreenView: View {
    @Environment(\.dismiss) private var dismiss
    let url: URL
    @State private var didFail = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Color.black.ignoresSafeArea()

            Group {
                if didFail {
                    Image(systemName: "photo")
                        .font(.system(size: 52, weight: .semibold))
                        .foregroundStyle(.white.opacity(0.75))
                } else {
                    KFImage.url(url)
                        .placeholder {
                            ProgressView()
                                .tint(.white)
                        }
                        .cancelOnDisappear(false)
                        .retry(maxCount: 3, interval: .seconds(1))
                        .onSuccess { _ in
                            didFail = false
                        }
                        .onFailure { error in
                            didFail = true
                            transcriptImageLog.error(
                                "Transcript image fullscreen failed url=\(redactedURLDescription(url), privacy: .public) error=\(error.localizedDescription, privacy: .public)"
                            )
                        }
                        .resizable()
                        .scaledToFit()
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, maxHeight: .infinity)

            Button {
                dismiss()
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.system(size: 32, weight: .semibold))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Close image")
            .padding(20)
        }
        .presentationBackground(.black)
    }
}

private func transcriptImagePlaceholder(speakerColor: Color) -> some View {
    RoundedRectangle(cornerRadius: 12)
        .fill(speakerColor.opacity(0.12))
        .frame(height: 160)
}

private func redactedURLDescription(_ url: URL) -> String {
    let components = URLComponents(url: url, resolvingAgainstBaseURL: false)
    let queryNames = components?.queryItems?
        .map(\.name)
        .sorted()
        .joined(separator: ",") ?? ""
    let base = "\(url.scheme ?? "unknown")://\(url.host ?? "no-host")\(url.path)"
    if queryNames.isEmpty {
        return base
    }
    return "\(base)?[\(queryNames)]"
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
        if !model.currentAudioBookChapterTitle.isEmpty { return model.currentAudioBookChapterTitle }
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

private struct TranscriptJudgementSourcesPreview: View {
    @State private var selectedSources: TranscriptSourcesSelection?

    private let sourceLine = LiveLine(
        speaker: "韩猎头（资深行业猎头 / 人才智库专家）",
        role: "discussant",
        text: "现在的筛选标准已经变了，企业不再看你做了多少，而是在看你用什么效率做，以及能不能带着 AI 一起做。",
        isUser: false,
        done: true,
        sources: [
            SourceDTO(
                title: "Hiring trends report",
                url: "https://example.com/hiring-trends",
                snippet: "Employers increasingly ask candidates to describe tool fluency and measurable impact.",
                markdown: nil
            ),
            SourceDTO(
                title: "AI skills survey",
                url: "https://example.com/ai-skills",
                snippet: "Survey data on AI tooling expectations in technical interviews.",
                markdown: nil
            ),
        ],
        judgementComment: "这点需要更强的证据支撑，先不要把它当成定论。"
    )

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ScrollView {
                VStack(spacing: 14) {
                    TranscriptBubble(line: sourceLine, isMine: false) { sources in
                        selectedSources = TranscriptSourcesSelection(sources: sources)
                    }
                    TranscriptBubble(
                        line: LiveLine(
                            speaker: "You",
                            role: "user",
                            text: "Can you show the sources behind that?",
                            isUser: true,
                            done: true
                        ),
                        isMine: true
                    )
                }
                .padding(16)
            }
        }
        .sheet(item: $selectedSources) { selection in
            SourcesSheet(
                discussion: Self.previewDiscussion(sources: selection.sources),
                allowsAddingSources: false
            )
        }
    }

    private static func previewDiscussion(sources: [SourceDTO]) -> Discussion {
        var discussion = try! JSONDecoder().decode(
            Discussion.self,
            from: Data("""
            {
              "id": "preview-transcript-sources",
              "topic": "AI changes in hiring",
              "title": "Hiring Signals in the AI Era",
              "status": "ready",
              "language": "zh"
            }
            """.utf8)
        )
        discussion.sources = sources
        return discussion
    }
}

#Preview("PodcastPlayerView · Pin to top") {
    PodcastPinPreview()
}

#Preview("Transcript · Judgement and Sources") {
    TranscriptJudgementSourcesPreview()
}
#endif

/// A small Identifiable wrapper so a bare URL can drive `.fullScreenCover(item:)`.
struct IdentifiableURL: Identifiable {
    let id = UUID()
    let url: URL
}

/// Renders an audiobook's "text-based content" — the book version of the
/// narration with the generated illustrations inline. The body is Markdown
/// (images embedded as `![](url)`), with remote images loaded through
/// Kingfisher so each URL gets its own cache identity.
struct TextContentView: View {
    let discussionID: String
    let title: String
    let api: APIClient

    @Environment(\.dismiss) private var dismiss
    @State private var markdown: String = ""
    @State private var isLoading = true
    @State private var errorText: String?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if let errorText {
                    ContentUnavailableView(
                        "Text unavailable",
                        systemImage: "book.closed",
                        description: Text(errorText)
                    )
                } else {
                    ScrollView {
                        Markdown(markdown)
                            .markdownImageProvider(TextContentMarkdownImageProvider())
                            .padding()
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await loadText() }
        }
    }

    private func loadText() async {
        isLoading = true
        errorText = nil
        do {
            let doc = try await api.summary(id: discussionID, docType: "text")
            logRawMarkdownForDebug(doc.markdown)
            markdown = doc.markdown
        } catch {
            errorText = error.localizedDescription
        }
        isLoading = false
    }

    private func logRawMarkdownForDebug(_ markdown: String) {
        let chunkSize = 2_000
        let totalParts = max(1, (markdown.count + chunkSize - 1) / chunkSize)
        textContentLog.info("Raw markdown begin source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text chars=\(markdown.count, privacy: .public) parts=\(totalParts, privacy: .public)")

        guard !markdown.isEmpty else {
            textContentLog.info("Raw markdown chunk source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text part=1/1 markdown=''")
            textContentLog.info("Raw markdown end source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text")
            return
        }

        var part = 1
        var index = markdown.startIndex
        while index < markdown.endIndex {
            let next = markdown.index(index, offsetBy: chunkSize, limitedBy: markdown.endIndex) ?? markdown.endIndex
            let chunk = String(markdown[index..<next])
            textContentLog.info("Raw markdown chunk source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text part=\(part, privacy: .public)/\(totalParts, privacy: .public) markdown=\(chunk, privacy: .public)")
            index = next
            part += 1
        }

        textContentLog.info("Raw markdown end source=TextContentView.loadText discussion=\(discussionID, privacy: .public) doc_type=text")
    }
}

private struct TextContentMarkdownImageProvider: ImageProvider {
    func makeImage(url: URL?) -> some View {
        Group {
            if let url {
                KFImage.url(url)
                    .placeholder {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                            .frame(height: 160)
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .fade(duration: 0.15)
                    .resizable()
                    .scaledToFit()
                    .id(url.absoluteString)
            } else {
                Color.clear
                    .frame(width: 0, height: 0)
            }
        }
    }
}

/// Full-screen player for an audiobook's rendered 1080p video (the illustration
/// slideshow with narration audio + captions). Presented from the context menu's
/// "View Video" action once the post-audio render has finished.
struct AudioBookVideoView: View {
    let url: URL
    @Environment(\.dismiss) private var dismiss
    @State private var player: AVPlayer?
    @State private var localFile: URL?
    @State private var isDownloading = false
    @State private var message: String?
    @State private var showingShareSheet = false
    @State private var showChrome = false
    @State private var hideChromeTask: Task<Void, Never>?

    private var chromeVisible: Bool {
        showChrome || isDownloading || message != nil
    }

    var body: some View {
        ZStack(alignment: .top) {
            Color.black.ignoresSafeArea()
            VideoPlayer(player: player)
                .ignoresSafeArea()
                .contentShape(Rectangle())
                .simultaneousGesture(TapGesture().onEnded {
                    toggleChromeFromVideoTap()
                })

            VStack(spacing: 0) {
                HStack(spacing: 12) {
                    Button {
                        dismiss()
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.system(size: 30))
                            .symbolRenderingMode(.hierarchical)
                            .foregroundStyle(.white)
                    }
                    .accessibilityLabel("Close")

                    Spacer()

                    Button {
                        revealChromeTemporarily()
                        shareVideo()
                    } label: {
                        Image(systemName: "folder")
                            .font(.system(size: 22, weight: .semibold))
                            .foregroundStyle(.white)
                            .frame(width: 38, height: 38)
                    }
                    .disabled(isDownloading)
                    .accessibilityLabel("Save to Files")

                    Button {
                        revealChromeTemporarily()
                        saveToCameraRoll()
                    } label: {
                        Image(systemName: "square.and.arrow.down")
                            .font(.system(size: 22, weight: .semibold))
                            .foregroundStyle(.white)
                            .frame(width: 38, height: 38)
                    }
                    .disabled(isDownloading)
                    .accessibilityLabel("Save to Camera Roll")
                }
                .padding(.horizontal, 16)
                .padding(.top, 58)
                .padding(.bottom, 12)

                if isDownloading || message != nil {
                    HStack(spacing: 10) {
                        if isDownloading {
                            ProgressView().tint(.white)
                        }
                        if let message {
                            Text(message)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.white)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(.black.opacity(0.62), in: .capsule)
                }

                Spacer()
            }
            .opacity(chromeVisible ? 1 : 0)
            .allowsHitTesting(chromeVisible)
            .animation(.easeInOut(duration: 0.18), value: chromeVisible)
        }
        .sheet(isPresented: $showingShareSheet) {
            if let localFile {
                FileShareSheet(url: localFile)
            }
        }
        .onAppear {
            let p = AVPlayer(url: url)
            player = p
            p.play()
        }
        .onDisappear {
            hideChromeTask?.cancel()
            player?.pause()
            player = nil
        }
    }

    private func toggleChromeFromVideoTap() {
        if isDownloading || message != nil {
            revealChromeTemporarily()
            return
        }
        if chromeVisible {
            hideChromeTask?.cancel()
            showChrome = false
        } else {
            revealChromeTemporarily()
        }
    }

    private func revealChromeTemporarily() {
        hideChromeTask?.cancel()
        showChrome = true
        hideChromeTask = Task { @MainActor in
            try? await Task.sleep(for: .seconds(3))
            if !isDownloading && message == nil {
                showChrome = false
            }
        }
    }

    private func shareVideo() {
        revealChromeTemporarily()
        Task {
            if let file = await localVideoFile() {
                localFile = file
                showingShareSheet = true
            }
        }
    }

    private func saveToCameraRoll() {
        revealChromeTemporarily()
        Task {
            guard let file = await localVideoFile() else { return }
            let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
            guard status == .authorized || status == .limited else {
                showMessage("Photo access was not granted.")
                return
            }
            do {
                try await PHPhotoLibrary.shared().performChanges {
                    PHAssetChangeRequest.creationRequestForAssetFromVideo(atFileURL: file)
                }
                showMessage("Saved to Camera Roll.")
            } catch {
                showMessage("Could not save video.")
            }
        }
    }

    @MainActor
    private func localVideoFile() async -> URL? {
        if let localFile { return localFile }
        isDownloading = true
        message = "Preparing video..."
        defer { isDownloading = false }
        do {
            let (downloaded, _) = try await URLSession.shared.download(from: url)
            let destination = FileManager.default.temporaryDirectory
                .appendingPathComponent("audiobook-video-\(UUID().uuidString).mp4")
            if FileManager.default.fileExists(atPath: destination.path) {
                try FileManager.default.removeItem(at: destination)
            }
            try FileManager.default.moveItem(at: downloaded, to: destination)
            localFile = destination
            message = nil
            return destination
        } catch {
            showMessage("Could not download video.")
            return nil
        }
    }

    private func showMessage(_ text: String) {
        message = text
        revealChromeTemporarily()
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            if message == text {
                message = nil
            }
        }
    }
}
