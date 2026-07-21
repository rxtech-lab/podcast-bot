import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif
import UniformTypeIdentifiers
import os

extension PodcastPlayerView {
    func transcript(_ model: PlayerModel) -> some View {
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
        #if canImport(UIKit)
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.12) {
                requestTranscriptScrollToBottom()
            }
        }
        #endif
    }

    @ViewBuilder
    func transcriptRow(_ item: TranscriptListItem) -> some View {
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
    func generateMoreChaptersRow(pendingCount: Int) -> some View {
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
    func isMyLine(_ line: LiveLine) -> Bool {
        PlayerModel.isLineAuthoredByCurrentUser(
            line,
            currentUserID: model?.currentUserID ?? auth.currentUser?.id ?? "",
            currentUsername: model?.currentUsername ?? auth.currentUser?.name ?? ""
        )
    }

    /// Transcript lines, plus the points summary as a trailing accessory row.
    func transcriptItems(for model: PlayerModel) -> [TranscriptListItem] {
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

    func discussionForTranscriptSources(_ sources: [SourceDTO]) -> Discussion {
        var copy = currentDiscussion
        copy.sources = sources
        return copy
    }

    /// Streaming is in effect while the most recent line is still being written.
    /// The `MessageList` follows the bottom while true and, on a fresh user send,
    /// pins that message to the top until the reply grows in.
    func isTranscriptStreaming(_ model: PlayerModel) -> Bool {
        !(model.lines.last?.done ?? true)
    }

    /// Toggle `transcriptShouldScrollToBottom` off→on so `MessageList` performs a
    /// one-shot scroll to the bottom (e.g. when the keyboard appears).
    func requestTranscriptScrollToBottom() {
        transcriptScrollRequestTask?.cancel()
        transcriptShouldScrollToBottom = false
        transcriptScrollRequestTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(10))
            guard !Task.isCancelled else { return }
            transcriptShouldScrollToBottom = true
        }
    }

    @ViewBuilder
    func footer(_ model: PlayerModel) -> some View {
        VStack(spacing: 10) {
            MusicPlayerBar(model: model) { playerSession?.isFullPlayerPresented = true }
            inputBar(model)
        }
        .padding(16)
    }

    func inputBar(_ model: PlayerModel) -> some View {
        let canSend = model.canSendMessages
        let trimmedMessage = message.trimmingCharacters(in: .whitespacesAndNewlines)
        let disabledControlColor = Theme.secondaryText
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

    func resumePlaybackIfNeededAfterRecorder(_ model: PlayerModel) {
        guard resumePlaybackAfterRecorder else { return }
        resumePlaybackAfterRecorder = false
        if !model.isPlaying {
            model.togglePlay()
        }
    }

    /// Uploads a recorded voice message to S3, then sends it: the transcript is the
    /// message text the agent reads, the audio URL/key let others replay it.
    func sendVoiceMessage(_ recording: VoiceMessageRecorder.Recording, model: PlayerModel) {
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
    func shareDocument(_ url: URL, model: PlayerModel) {
        let access = url.startAccessingSecurityScopedResource()
        let data = try? Data(contentsOf: url)
        if access { url.stopAccessingSecurityScopedResource() }
        let filename = url.lastPathComponent
        guard let data else { return }
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        shareData(data, filename: filename, mime: mime, model: model)
    }

    /// Loads a picked photo's bytes and shares it like a document.
    func sharePhoto(_ item: PhotosPickerItem, model: PlayerModel) {
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
    func shareData(_ data: Data, filename: String, mime: String, model: PlayerModel) {
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

    func makePrivate(_ model: PlayerModel) {
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

    func generateSummary() {
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

    func generateMindmap() {
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

    func generateVideo() {
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

    func createFromPlan() {
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
