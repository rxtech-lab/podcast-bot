import Foundation
import Observation
import AVFoundation
import MediaPlayer
import SwiftUI
import UIKit
import os

extension PlayerModel {
    // MARK: - Live transcript (WebSocket)

    func listenEvents(jobID: String) async {
        let socket = JobSocket(api: api, jobID: jobID, discussionID: discussion.id)
        self.socket = socket
        isStartingJobSocket = false
        for await env in socket.events() {
            handle(env)
            if Task.isCancelled { break }
        }
        socket.close()
        if self.socket === socket { self.socket = nil }
    }

    func listenForJobUpdatesIfNeeded() {
        guard let jobID = discussion.jobID, socket == nil, !isStartingJobSocket else { return }
        isStartingJobSocket = true
        tasks.append(Task { await listenEvents(jobID: jobID) })
    }

    func handle(_ env: JobEventEnvelope) {
        // The socket layer injects this after a drop+reconnect (e.g. the app was
        // backgrounded). It carries no data; use it to re-fetch state that may
        // have changed while disconnected.
        if env.event == JobSocket.reconnectEvent {
            refreshTranscriptAfterReconnect()
            refreshPodcastFromServer()
            return
        }
        guard let data = env.data else { return }
        switch env.event {
        case "transcript":
            // Image-only event (audiobook illustration): append an image bubble
            // and stop — there's no spoken text to merge.
            if let img = data.image_url?.trimmingCharacters(in: .whitespacesAndNewlines), !img.isEmpty {
                // Close the streaming text bubble so text before the image is one
                // finished message and text after it starts a new one (the
                // text / image / text split the transcript should show).
                LiveLine.finalizeLastOpenAgentLine(in: &lines)
                let imgLine = LiveLine(speaker: data.speaker ?? "",
                                       role: data.role ?? "",
                                       text: data.text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                                       isUser: false,
                                       done: true,
                                       imageURL: img,
                                       audioOffsetSeconds: Self.audioOffsetSeconds(fromMS: data.audio_offset_ms))
                lines.append(imgLine)
                hideTranscriptLoadingIfReady()
                return
            }
            guard let speaker = data.speaker, let text = data.text else { return }
            let role = data.role ?? ""
            if data.isUserMessage == true {
                mergeUserMessageEvent(speaker: speaker,
                                      role: role,
                                      text: text,
                                      done: data.done == true,
                                      senderUserID: data.sender_user_id,
                                      audioURL: data.audio_url)
                hideTranscriptLoadingIfReady()
                return
            }
            // The backend echoes the listener's own messages back here. We already
            // recorded that line optimistically in `send(_:)`, so drop the echo
            // rather than re-adding it as a non-user line (`applyTranscriptEvent`
            // always sets isUser:false), which would both render and double-persist
            // into `discussion.lines`.
            if Self.isUserRole(role) { return }
            if let completed = LiveLine.applyTranscriptEvent(to: &lines,
                                                             speaker: speaker,
                                                             role: role,
                                                             text: text,
                                                             done: data.done == true,
                                                             sources: data.sources,
                                                             judgementComment: data.judgement_comment) {
                persist(line: completed)
            }
            hideTranscriptLoadingIfReady()
        case "tick":
            updateJobProgress(elapsedMS: data.elapsed_ms, remainingMS: data.remaining_ms)
        case "phase":
            phaseLabel = data.label ?? data.phase ?? ""
            updateNowPlayingInfo()
        case "status":
            if let t = data.text {
                statusText = t
                updateNowPlayingInfo()
            }
        case "summary_ready":
            // The server changed the podcast summary state (generating, ready,
            // or failed). Re-fetch the discussion detail so the toolbar reflects
            // pending/manual/available states; the body itself is fetched later
            // by the summary view when it mounts.
            applySummaryEvent(data)
            refreshPodcastFromServer()
        case "resource_updated":
            if shouldRefreshForResourceUpdate(data) {
                uiActionsRefreshVersion += 1
                refreshPodcastFromServer()
            }
        default:
            break
        }
    }

    func shouldRefreshForResourceUpdate(_ data: JobEventData) -> Bool {
        let resourceType = data.resource_type?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased() ?? ""
        if !resourceType.isEmpty && resourceType != "podcast" && resourceType != "discussion" {
            return false
        }
        let resourceID = data.resource_id?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !resourceID.isEmpty {
            return resourceID == discussion.id
        }
        let link = (data.deep_link ?? data.id ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return link.isEmpty || link.contains(discussion.id)
    }

    /// Re-fetches the podcast detail after a websocket invalidation event,
    /// preserving local transcript/playback state while adopting server-owned
    /// metadata such as summary, text content, cover, video, and download URLs.
    func refreshPodcastFromServer() {
        tasks.append(Task { [weak self] in
            guard let self else { return }
            guard let fresh = try? await self.api.discussion(id: self.discussion.id, includeEditTurns: false) else { return }
            self.discussion = Self.mergingLocalDiscussionState(current: self.discussion, fresh: fresh)
            self.listenForJobUpdatesIfNeeded()
        })
    }

    func applySummaryEvent(_ data: JobEventData) {
        guard let raw = data.status, let status = SummaryStatus(rawValue: raw) else { return }
        let meta = SummaryMeta(
            docType: data.doc_type,
            status: status,
            available: status == .ready,
            pending: status == .generating,
            generation: false,
            generatedAt: nil
        )
        // The summary_ready event is shared by every generated document type;
        // route it by doc_type so a mindmap event never clobbers the summary
        // descriptor (and vice versa). Other doc types (e.g. "text") only ride
        // the refreshPodcastFromServer() that follows.
        switch data.doc_type {
        case "mindmap":
            discussion.mindmap = meta
        case "summary", nil:
            discussion.summary = meta
        default:
            break
        }
    }

    func mergeUserMessageEvent(speaker: String,
                                       role: String,
                                       text: String,
                                       done: Bool,
                                       senderUserID: String?,
                                       audioURL: String?) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let sender = senderUserID?.trimmingCharacters(in: .whitespacesAndNewlines)
        let audio = audioURL?.trimmingCharacters(in: .whitespacesAndNewlines)
        let existingIndex = lines.firstIndex {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser
        }
        if let existingIndex {
            lines[existingIndex].done = done
            if let sender, !sender.isEmpty {
                lines[existingIndex].senderUserID = sender
            }
            if let audio, !audio.isEmpty {
                lines[existingIndex].audioURL = audio
            }
        } else {
            // Close any streaming agent bubble first so an incoming participant
            // message lands in order rather than the agent bubble growing past it.
            LiveLine.finalizeLastOpenAgentLine(in: &lines)
            lines.append(LiveLine(speaker: speaker,
                                  role: role,
                                  text: trimmed,
                                  isUser: true,
                                  done: done,
                                  senderUserID: sender,
                                  audioURL: audio))
        }
        if let cachedIndex = discussion.lines?.firstIndex(where: {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed && $0.isUser
        }) {
            if let sender, !sender.isEmpty {
                discussion.lines?[cachedIndex].senderUserID = sender
            }
            if let audio, !audio.isEmpty {
                discussion.lines?[cachedIndex].audioURL = audio
            }
        } else {
            persistIfNeeded(speaker: speaker,
                            role: role,
                            text: trimmed,
                            isUser: true,
                            senderUserID: sender,
                            syncRemote: false,
                            audioURL: audio)
        }
    }

    func persist(line: LiveLine) {
        persistIfNeeded(line: line, startMs: Int(currentTime * 1000))
    }

    func persistIfNeeded(line: LiveLine, startMs: Int = 0, syncRemote: Bool = true,
                                 audioURL: String? = nil, audioKey: String? = nil) {
        persistIfNeeded(speaker: line.speaker,
                        role: line.role,
                        text: line.text,
                        startMs: startMs,
                        isUser: line.isUser,
                        senderUserID: line.senderUserID,
                        syncRemote: syncRemote,
                        audioURL: audioURL,
                        audioKey: audioKey,
                        sources: line.sources,
                        judgementComment: line.judgementComment)
    }

    func persistIfNeeded(speaker: String,
                                 role: String,
                                 text: String,
                                 startMs: Int = 0,
                                 isUser: Bool,
                                 senderUserID: String? = nil,
                                 syncRemote: Bool = true,
                                 audioURL: String? = nil,
                                 audioKey: String? = nil,
                                 sources: [SourceDTO]? = nil,
                                 judgementComment: String? = nil) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // Keep an empty voice message (audio present, transcript unavailable) — its
        // audio is still worth persisting; only empty text-only lines are dropped.
        let isVoice = audioURL != nil || audioKey != nil
        guard !trimmed.isEmpty || isVoice else { return }
        // A voice message is never a duplicate of an earlier line that happens to
        // share its transcript — distinct recordings carry distinct audio, so they
        // must not be deduped by text alone (that would drop the second note).
        let exists = audioURL == nil && discussion.sortedLines.contains {
            $0.speaker == speaker && $0.role == role && $0.text == trimmed
                && $0.isUser == isUser && $0.audioURL == nil
        }
        guard !exists else { return }
        let dto = DiscussionLineDTO(speaker: speaker, role: role, side: nil,
                                    text: trimmed, startMS: startMs, isUser: isUser,
                                    senderUserID: senderUserID, audioURL: audioURL,
                                    sources: sources, judgementComment: judgementComment)
        discussion.lines = (discussion.lines ?? []) + [dto]
        guard syncRemote else { return }
        Task {
            try? await api.appendDiscussionLine(
                id: discussion.id,
                line: DiscussionLineRequest(speaker: speaker,
                                            role: role,
                                            side: nil,
                                            text: trimmed,
                                            startMS: startMs,
                                            isUser: isUser,
                                            audioURL: audioURL,
                                            audioKey: audioKey),
                shareToken: shareToken
            )
        }
    }

    /// Loads the full discussion detail on entry and adopts its persisted lines.
    ///
    /// The library opens the player from a lightweight list row that omits
    /// `native_discussion_lines`, so a re-entered discussion starts with no voice
    /// lines. The detail endpoint returns them with freshly re-signed `audioURL`s.
    /// Adopting them here (a) restores the replay control, (b) refreshes playback
    /// URLs that expire ~1h after the last fetch, and (c) gives the subsequent
    /// text-only job-transcript snapshot an existing audio line to recognize, so it
    /// won't re-persist the utterance as a duplicate plain-text row.
    func hydratePersistedLines() async {
        guard let fresh = try? await api.discussion(id: discussion.id, includeEditTurns: false) else { return }
        // The library/market list responses omit the presigned audio download
        // URL (it is resolved only on the detail endpoint to keep lists fast), so
        // adopt it here to give the export/share action a direct link. Playback
        // and the download fallback already work from the jobID regardless.
        if let url = fresh.downloadURLString, !url.isEmpty {
            discussion.downloadURLString = url
            downloadURL = URL(string: url)
        }
        discussion.summary = fresh.summary
        if let cover = fresh.cover, cover.hasImage || cover.hasGradient {
            discussion.cover = cover
        }
        listenForJobUpdatesIfNeeded()
        let persisted = fresh.sortedLines
        guard !persisted.isEmpty else {
            await refreshCoverAssets()
            return
        }

        // Adopt persisted lines as the authoritative cache without dropping any
        // local lines not yet synced server-side.
        var cache = persisted
        for local in discussion.sortedLines where !cache.contains(local) {
            cache.append(local)
        }
        discussion.lines = cache

        // Reflect persisted lines into the visible transcript: upgrade a matching
        // text line with its audio URL, or append one we don't have yet.
        for dto in persisted {
            guard Self.hasDisplayablePayload(dto) else { continue }
            if let idx = lines.firstIndex(where: {
                $0.speaker == dto.speaker && $0.role == dto.role && $0.text == dto.text && $0.isUser == dto.isUser
            }) {
                Self.applyPersistedMetadata(to: &lines[idx], from: dto)
            } else {
                lines.append(LiveLine(speaker: dto.speaker, role: dto.role, text: dto.text,
                                      isUser: dto.isUser, done: true,
                                      senderUserID: dto.senderUserID, audioURL: dto.audioURL,
                                      imageURL: dto.imageURL,
                                      audioOffsetSeconds: Self.audioOffsetSeconds(fromMS: dto.startMS),
                                      sources: dto.sources, judgementComment: dto.judgementComment))
            }
        }
        hideTranscriptLoadingIfReady()
        await refreshCoverAssets()
    }

    nonisolated static func hasDisplayablePayload(_ dto: DiscussionLineDTO) -> Bool {
        !dto.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            || !(dto.audioURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
            || !(dto.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
            || !(dto.sources?.isEmpty ?? true)
            || !(dto.judgementComment?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true)
    }

    func loadTranscriptSnapshot(jobID: String) async {
        while !Task.isCancelled && !hasPodcastTranscript {
            if let snapshot = try? await api.jobTranscript(id: jobID) {
                mergeTranscriptSnapshot(snapshot)
                if hasPodcastTranscript { return }
            }
            try? await Task.sleep(for: .seconds(2))
        }
    }

    /// Re-fetch the merged job transcript once and reconcile it. The live socket
    /// only delivers events that occur while it's connected, so anything that
    /// streamed during a drop (app backgrounded, network blip, pod hand-off) is
    /// missing until we pull the authoritative snapshot back in. `mergeTranscript`
    /// dedups, so re-running is safe and only fills gaps.
    func refreshTranscriptSnapshot(jobID: String) async {
        if let snapshot = try? await api.jobTranscript(id: jobID) {
            mergeTranscriptSnapshot(snapshot)
        }
    }

    /// Kicks a one-shot transcript backfill after the socket reconnected.
    func refreshTranscriptAfterReconnect() {
        guard let jobID = discussion.jobID else { return }
        tasks.append(Task { await self.refreshTranscriptSnapshot(jobID: jobID) })
    }

    /// Foreground hook: when the app returns to the foreground while the job is
    /// still generating, reconcile the transcript immediately instead of waiting
    /// for the socket to notice it dropped. Safe to call repeatedly.
    func foregroundRefresh() {
        guard discussion.status == .generating else { return }
        refreshTranscriptAfterReconnect()
    }

    func mergeTranscriptSnapshot(_ snapshot: [TranscriptDTO]) {
        var didChange = false
        for item in snapshot {
            if let imageURL = item.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
               !imageURL.isEmpty {
                let role = item.role
                let offset = Self.audioOffsetSeconds(fromMS: item.audioOffsetMS)
                if let existing = lines.firstIndex(where: { $0.imageURL == imageURL }) {
                    // Backfill timing onto an image bubble that arrived over the
                    // socket before the snapshot (or from an older client state).
                    if lines[existing].audioOffsetSeconds == nil, let offset {
                        lines[existing].audioOffsetSeconds = offset
                        didChange = true
                    }
                    let caption = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
                    if !caption.isEmpty, lines[existing].displayText.isEmpty {
                        lines[existing].text = caption
                        didChange = true
                    }
                } else {
                    lines.append(LiveLine(speaker: item.speaker,
                                          role: role,
                                          text: item.text.trimmingCharacters(in: .whitespacesAndNewlines),
                                          isUser: false,
                                          done: true,
                                          imageURL: imageURL,
                                          audioOffsetSeconds: offset))
                    didChange = true
                }
                continue
            }
            let text = item.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { continue }
            let role = item.role
            let isUser = role == "user" || role == "viewer"
            let persisted = discussion.sortedLines.first {
                $0.speaker == item.speaker && $0.role == role && $0.text == text && $0.isUser == isUser
            }
            let existingIndex = lines.firstIndex { $0.speaker == item.speaker && $0.role == role && $0.text == text && $0.isUser == isUser }
                ?? Self.snapshotPrefixReplacementIndex(for: item,
                                                       text: text,
                                                       isUser: isUser,
                                                       in: lines,
                                                       snapshot: snapshot)
            if let existingIndex {
                if lines[existingIndex].text != text {
                    let oldText = lines[existingIndex].text.trimmingCharacters(in: .whitespacesAndNewlines)
                    lines[existingIndex].text = text
                    lines[existingIndex].done = true
                    didChange = true
                    replaceCachedPrefixLine(speaker: item.speaker,
                                            role: role,
                                            oldText: oldText,
                                            newText: text,
                                            isUser: isUser)
                }
                if let persisted {
                    Self.applyPersistedMetadata(to: &lines[existingIndex], from: persisted)
                }
                if let sources = item.sources {
                    lines[existingIndex].sources = sources
                }
                if let judgement = item.judgementComment {
                    lines[existingIndex].judgementComment = judgement
                }
            } else {
                lines.append(LiveLine(speaker: item.speaker, role: role, text: text, isUser: isUser, done: true,
                                      senderUserID: persisted?.senderUserID, audioURL: persisted?.audioURL,
                                      sources: item.sources ?? persisted?.sources,
                                      judgementComment: item.judgementComment ?? persisted?.judgementComment))
                didChange = true
            }
            // The job transcript has no audio metadata, so a voice message comes
            // back as plain text. If we already hold it as a voice line, it is
            // already persisted (with its audio) — re-persisting would add a
            // duplicate text-only row that survives reload. A genuine user text
            // message (no audioURL) still backfills normally.
            if let existingIndex, lines[existingIndex].isUser, lines[existingIndex].audioURL?.isEmpty == false {
                continue
            }
            persistIfNeeded(speaker: item.speaker, role: role, text: text, isUser: isUser)
        }
        if didChange {
            let ordered = lines.enumerated().sorted { lhs, rhs in
                let leftOrder = transcriptSnapshotOrder(for: lhs.element, in: snapshot)
                let rightOrder = transcriptSnapshotOrder(for: rhs.element, in: snapshot)
                if leftOrder != rightOrder { return leftOrder < rightOrder }
                return lhs.offset < rhs.offset
            }.map(\.element)
            lines = ordered
        }
        hideTranscriptLoadingIfReady()
    }

    nonisolated static func snapshotPrefixReplacementIndex(for item: TranscriptDTO,
                                                           text: String,
                                                           isUser: Bool,
                                                           in lines: [LiveLine],
                                                           snapshot: [TranscriptDTO]) -> Int? {
        guard !text.isEmpty else { return nil }
        for index in lines.indices.reversed() {
            let line = lines[index]
            guard line.speaker == item.speaker,
                  line.role == item.role,
                  line.isUser == isUser,
                  !line.hasImage,
                  !line.hasAudio else { continue }
            let localText = line.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !localText.isEmpty,
                  localText != text,
                  text.hasPrefix(localText) else { continue }
            let localIsAuthoritative = snapshot.contains { snapshotItem in
                let role = snapshotItem.role
                let snapshotIsUser = role == "user" || role == "viewer"
                return snapshotItem.speaker == line.speaker &&
                    role == line.role &&
                    snapshotIsUser == line.isUser &&
                    snapshotItem.text.trimmingCharacters(in: .whitespacesAndNewlines) == localText
            }
            if !localIsAuthoritative {
                return index
            }
        }
        return nil
    }

    func replaceCachedPrefixLine(speaker: String,
                                         role: String,
                                         oldText: String,
                                         newText: String,
                                         isUser: Bool) {
        guard var cachedLines = discussion.lines else { return }
        guard let index = cachedLines.lastIndex(where: {
            $0.speaker == speaker &&
                $0.role == role &&
                $0.text.trimmingCharacters(in: .whitespacesAndNewlines) == oldText &&
                $0.isUser == isUser
        }) else { return }
        cachedLines[index].text = newText
        discussion.lines = cachedLines
    }

    func transcriptSnapshotOrder(for line: LiveLine, in snapshot: [TranscriptDTO]) -> Int {
        if let imageURL = line.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines),
           !imageURL.isEmpty,
           let snapshotIndex = snapshot.firstIndex(where: {
               $0.imageURL?.trimmingCharacters(in: .whitespacesAndNewlines) == imageURL
           }) {
            return snapshotIndex
        }
        let text = line.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !text.isEmpty,
           let snapshotIndex = snapshot.firstIndex(where: { item in
               let role = item.role
               let isUser = role == "user" || role == "viewer"
               return item.speaker == line.speaker && role == line.role &&
                   item.text.trimmingCharacters(in: .whitespacesAndNewlines) == text &&
                   isUser == line.isUser
           }) {
            return snapshotIndex
        }
        if text.isEmpty {
            return Int.max
        }
        return discussion.sortedLines.firstIndex {
            $0.speaker == line.speaker && $0.role == line.role && $0.text == text && $0.isUser == line.isUser
        } ?? Int.max
    }

    var hasPodcastTranscript: Bool {
        Self.containsPodcastTranscript(lines)
    }

    nonisolated static func containsPodcastTranscript(_ lines: [LiveLine]) -> Bool {
        lines.contains { line in
            let role = line.role.lowercased()
            return !line.isUser
                && role != "user"
                && role != "viewer"
                && !line.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    nonisolated static func remainingTranscriptLoadingDelay(startedAt: Date?,
                                                            now: Date = Date()) -> TimeInterval {
        guard let startedAt else { return 0 }
        let elapsed = max(0, now.timeIntervalSince(startedAt))
        return max(0, minimumTranscriptLoadingSeconds - elapsed)
    }

    nonisolated static func transcriptLoadingVisibleAfterTerminalFailure(wasVisible: Bool) -> Bool {
        false
    }

    func showTranscriptLoadingIfNeeded() {
        guard !isTranscriptLoading else { return }
        isTranscriptLoading = true
        transcriptLoadingStartedAt = Date()
        transcriptLoadingHideTask?.cancel()
        transcriptLoadingHideTask = nil
    }

    func forceHideTranscriptLoading() {
        transcriptLoadingHideTask?.cancel()
        transcriptLoadingHideTask = nil
        transcriptLoadingStartedAt = nil
        isTranscriptLoading = Self.transcriptLoadingVisibleAfterTerminalFailure(wasVisible: isTranscriptLoading)
    }

    func hideTranscriptLoadingIfReady() {
        guard isTranscriptLoading, hasPodcastTranscript else { return }

        transcriptLoadingHideTask?.cancel()
        let delay = Self.remainingTranscriptLoadingDelay(startedAt: transcriptLoadingStartedAt)
        if delay <= 0 {
            isTranscriptLoading = false
            transcriptLoadingStartedAt = nil
            transcriptLoadingHideTask = nil
            return
        }

        let task = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                guard let self, self.hasPodcastTranscript else { return }
                self.isTranscriptLoading = false
                self.transcriptLoadingStartedAt = nil
                self.transcriptLoadingHideTask = nil
            }
        }
        transcriptLoadingHideTask = task
        tasks.append(task)
    }

}
