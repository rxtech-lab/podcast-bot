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

let transcriptImageLog = Logger(subsystem: "com.debatebot.ios", category: "TranscriptImage")
let textContentLog = Logger(subsystem: "com.debatebot.ios", category: "TextContent")

/// The live podcast screen: streaming per-agent transcript bubbles, a synced
/// caption, a Liquid Glass music-player bar, and a message input — matching the
/// mockups.
struct PodcastPlayerView: View {
    @Environment(AuthManager.self) var auth
    @Environment(PurchaseManager.self) var purchases
    @Environment(PlayerSessionStore.self) var playerSessions
    @Environment(\.scenePhase) var scenePhase
    let discussion: Discussion
    var onCreatedFromPlan: ((Discussion) -> Void)?
    var onCreatedFollowUp: ((Discussion) -> Void)?
    /// Non-nil when this discussion was opened via a share link; passed
    /// to the player model so a non-owner participant's comments are authorized.
    var shareToken: String? = nil
    /// Marketplace detail pages keep the player toolbar, but hide the root tab bar.
    var hidesTabBar: Bool = false
    var onSignOut: (() -> Void)?

    @State var playerSession: PlayerSession?
    @State var message = ""
    @State var showingPlan = false
    @State var showingSummary = false
    @State var showingText = false
    @State var showingMindmap = false
    @State var audioBookVideoURL: IdentifiableURL?
    @State var showingImporter = false
    @State var showingPhotos = false
    @State var showingPointsHistory = false
    @State var showingPublishSheet = false
    @State var showingCoverEditor = false
    @State var showingShareSheet = false
    @State var showingCreatorProfile = false
    @State var showingFollowUpForm = false
    @State var showingChapterChecklist = false
    @State var showingAlbum = false
    @State var showingAlbumPicker = false
    @State var chapterProgress: ChaptersResponse?
    @State var selectedPhoto: PhotosPickerItem?
    @State var showingRecorder = false
    @State var selectedTranscriptSources: TranscriptSourcesSelection?
    @State var selectedTranscriptImageURL: IdentifiableURL?
    @State var resumePlaybackAfterRecorder = false
    @State var isUploadingAttachment = false
    @State var isCreatingFromPlan = false
    @State var createFromPlanError: String?
    @State var isGeneratingSummary = false
    @State var summaryGenerateError: String?
    @State var isGeneratingMindmap = false
    @State var mindmapGenerateError: String?
    @State var isGeneratingVideo = false
    @State var isVideoGenerationPending = false
    @State var videoGenerateError: String?
    @State var documentActionItems: [DiscussionUIActionItem] = []
    @State var podcastActionItems: [DiscussionUIActionItem] = []
    @State var showingForceStopConfirm = false
    @State var transcriptIsAtBottom = true
    @State var transcriptShouldScrollToBottom = false
    @State var transcriptScrollRequestTask: Task<Void, Never>?

    /// Stable id for the optional points-summary accessory row so it doesn't
    /// churn its identity across renders.
    static let usageItemID = UUID()
    /// Stable id for the optional "generate more chapters" accessory row.
    static let generateMoreItemID = UUID()

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
    var podcastToolbar: some ToolbarContent {
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

    var currentCreator: CreatorProfile? {
        model?.discussion.creator ?? discussion.creator
    }

    var model: PlayerModel? {
        playerSession?.model
    }

    var currentDiscussion: Discussion {
        model?.discussion ?? discussion
    }

    /// Whether the Summary menu item is enabled — true only once the server has
    /// generated the podcast's summary document (status `ready`).
    var summaryAvailable: Bool {
        currentDiscussion.hasSummary
    }

    var summaryPending: Bool {
        currentDiscussion.summaryPending
    }

    var summaryGenerationAvailable: Bool {
        currentDiscussion.status == .ready
            && currentDiscussion.isOwner == true
            && currentDiscussion.canGenerateSummary
    }

    var uiActionsRefreshKey: String {
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

    var playerSessionTaskKey: String {
        "\(discussion.id)|\(shareToken ?? "")"
    }

    /// Extracted so the construction of `SummaryView` (and its `APIClient`) stays
    /// out of the main `body` modifier chain, which is large enough that inlining
    /// it pushes the SwiftUI type-checker past its time budget.
    var summarySheet: some View {
        SummaryView(discussionID: currentDiscussion.id,
                    title: currentDiscussion.displayTitle,
                    mindmapEditable: currentDiscussion.isOwner == true,
                    api: APIClient(tokens: auth))
    }

    /// The audiobook "text-based content" book view (narration + illustrations).
    /// Kept out of `body` for the same type-checker reason as
    /// `summarySheet`.
    var textSheet: some View {
        TextContentView(discussionID: currentDiscussion.id,
                        title: currentDiscussion.displayTitle,
                        api: APIClient(tokens: auth))
    }

    /// The discussion mindmap editor. Kept out of `body` for the same
    /// type-checker reason as `summarySheet`.
    var mindmapSheet: some View {
        MindmapView(discussionID: currentDiscussion.id,
                    title: currentDiscussion.displayTitle,
                    isEditable: currentDiscussion.isOwner == true,
                    api: APIClient(tokens: auth))
    }

    func loadUIActions() async {
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
    func refreshChapterProgress() async {
        guard currentDiscussion.script?.type == "audio-book",
              currentDiscussion.status == .ready,
              currentDiscussion.isOwner != false,
              onCreatedFollowUp != nil else {
            chapterProgress = nil
            return
        }
        chapterProgress = try? await APIClient(tokens: auth).discussionChapters(id: currentDiscussion.id)
    }

    func performDocumentAction(_ item: DiscussionUIActionItem) {
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

    func performPodcastAction(_ item: DiscussionUIActionItem) {
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

    func openVideoAction(_ item: DiscussionUIActionItem) -> Bool {
        // The video action carries a raw playback URL (not a debatepod deep
        // link), so handle it before the path-based routing.
        guard item.action.type == "play-video" else { return false }
        if let url = URL(string: item.action.link) {
            audioBookVideoURL = IdentifiableURL(url: url)
        }
        return true
    }

    func validatedDiscussionActionPath(_ item: DiscussionUIActionItem) -> [String]? {
        guard let url = URL(string: item.action.link),
              url.scheme == "debatepod",
              url.host == "discussion" else { return nil }
        let components = url.pathComponents.filter { $0 != "/" }
        guard components.first == currentDiscussion.id else { return nil }
        return Array(components.dropFirst())
    }

    func isDocumentActionBusy(_ item: DiscussionUIActionItem) -> Bool {
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

    func reconcileVideoGenerationPending(with items: [DiscussionUIActionItem]) {
        guard isVideoGenerationPending else { return }
        if items.contains(where: { $0.id == "video-rendering" || $0.id == "view-video" }) {
            isVideoGenerationPending = false
        }
    }

    func isPodcastActionBusy(_ item: DiscussionUIActionItem) -> Bool {
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

    func podcastActionTitle(_ item: DiscussionUIActionItem) -> String? {
        item.id == "points" ? pointsMenuLabel : nil
    }

    func podcastActionsTip(for model: PlayerModel) -> (any Tip)? {
        if model.discussion.isPublic {
            return ShareStationTip()
        }
        if model.discussion.isOwner != false {
            return PublishToMarketTip()
        }
        return nil
    }

    @ViewBuilder
    var publishStationSheet: some View {
        if let model {
            PublishStationSheet(discussion: discussionBinding(for: model))
        }
    }

    @ViewBuilder
    var coverEditorSheet: some View {
        if let model {
            CoverEditorSheet(discussion: discussionBinding(for: model))
        }
    }

    var shareSheet: some View {
        ShareSheet(discussionID: currentDiscussion.id,
                   api: APIClient(tokens: auth))
    }

    @ViewBuilder
    var creatorProfileSheet: some View {
        if let creator = currentCreator {
            CreatorProfileView(creatorID: creator.id,
                               initialProfile: creator,
                               onCreateFromPlan: onCreatedFromPlan)
        }
    }

    var followUpFormSheet: some View {
        NewDiscussionView(reference: currentPodcastReference) { created in
            showingFollowUpForm = false
            onCreatedFollowUp?(created)
        }
    }

    /// Chapter batch picker for "generate more chapters": creates a follow-up
    /// podcast narrating the checked chapters. The server's 400 (over the
    /// 5-chapter batch limit, or chapters already generated) surfaces as an
    /// alert inside the sheet.
    var chapterChecklistSheet: some View {
        ChapterChecklistSheet(mode: .discussion(id: currentDiscussion.id)) { indices in
            let created = try await APIClient(tokens: auth).generateChapters(id: currentDiscussion.id, chapters: indices)
            showingChapterChecklist = false
            await refreshChapterProgress()
            onCreatedFollowUp?(created)
        }
    }

    var albumSheet: some View {
        NavigationStack {
            AlbumView(albumID: currentDiscussion.albumID ?? chapterProgress?.albumID ?? "",
                      ownsNavigation: true,
                      mode: currentDiscussion.isOwner == true ? .owner : .publicMarket)
        }
    }

    var albumPickerSheet: some View {
        AlbumPickerSheet(discussion: currentDiscussion) { album in
            showingAlbumPicker = false
            model?.discussion.albumID = album.id
        }
    }

    @ViewBuilder
    var downloadProgressSheet: some View {
        if let model {
            DownloadProgressSheet(model: model)
        }
    }

    @ViewBuilder
    var fullPlayerCover: some View {
        if let playerSession {
            FullScreenPlayerView(model: playerSession.model)
        }
    }

    var fullPlayerPresentedBinding: Binding<Bool> {
        Binding(
            get: { playerSession?.isFullPlayerPresented == true },
            set: { playerSession?.isFullPlayerPresented = $0 }
        )
    }

    func discussionBinding(for model: PlayerModel) -> Binding<Discussion> {
        Binding(
            get: { model.discussion },
            set: { model.discussion = $0 }
        )
    }

    var downloadDialogBinding: Binding<Bool> {
        Binding(
            get: { model?.showsDownloadDialog == true },
            set: { isPresented in
                if !isPresented { model?.showsDownloadDialog = false }
            }
        )
    }

    var downloadedPodcastFileBinding: Binding<DownloadedPodcastFile?> {
        Binding(
            get: { model?.downloadedPodcastFile },
            set: { model?.downloadedPodcastFile = $0 }
        )
    }

    func fileShareSheet(_ file: DownloadedPodcastFile) -> some View {
        FileShareSheet(url: file.url)
    }

    var currentPodcastReference: PodcastReference {
        PodcastReference(id: currentDiscussion.id,
                         title: currentDiscussion.displayTitle,
                         topic: currentDiscussion.topic)
    }

    var showsActionsMenu: Bool {
        purchases.isConfigured
            || model?.showsPodcastActions == true
            || !podcastActionItems.isEmpty
            || onCreatedFromPlan != nil
            || onCreatedFollowUp != nil
            || onSignOut != nil
    }

    var createFollowUpAction: (() -> Void)? {
        guard onCreatedFollowUp != nil else { return nil }
        return { showingFollowUpForm = true }
    }

    var createFromPlanAction: (() -> Void)? {
        guard onCreatedFromPlan != nil else { return nil }
        return { createFromPlan() }
    }

    var createFromPlanErrorBinding: Binding<Bool> {
        Binding(
            get: { createFromPlanError != nil },
            set: { if !$0 { createFromPlanError = nil } }
        )
    }

    var summaryGenerateErrorBinding: Binding<Bool> {
        Binding(
            get: { summaryGenerateError != nil },
            set: { if !$0 { summaryGenerateError = nil } }
        )
    }

    func loadPlayerIfNeeded() async {
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

    func stopPlayerIfNeeded() {
        guard let playerSession else { return }
        playerSessions.release(playerSession)
    }

    func handleScenePhaseChange(_ phase: ScenePhase) {
        // Returning to the foreground while the job is live: the socket may have
        // been torn down while suspended, so reconcile immediately.
        guard phase == .active else { return }
        model?.foregroundRefresh()
    }

    /// Balance label for the podcast options menu, matching the discussion page.
    var pointsMenuLabel: String {
        guard let balance = purchases.pointsBalance else {
            return String(localized: "Points", comment: "Podcast menu label when the points balance is unknown")
        }
        let pointLabel = balance == 1
            ? String(localized: "Point", comment: "Singular unit for a points balance")
            : String(localized: "Points", comment: "Plural unit for a points balance")
        return String(localized: "Points (Balance \(UsageSummary.formatInt(balance)) \(pointLabel))",
                      comment: "Podcast menu points label; first value is the formatted balance, second is the localized unit")
    }

}
