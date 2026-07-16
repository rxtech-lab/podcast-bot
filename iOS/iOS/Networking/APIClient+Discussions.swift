import Foundation
import JSONSchemaForm
import OSLog

extension APIClient {
    // MARK: - Planning

    func plan(_ req: PlanRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan", body: req)
    }

    func improve(_ req: PlanImproveRequest) async throws -> PlanResponse {
        try await send("POST", "/api/plan/improve", body: req)
    }

    // MARK: - Server-owned discussions

    func discussions(limit: Int = 20,
                     offset: Int = 0,
                     query: String? = nil,
                     visibility: DiscussionVisibility? = nil,
                     type: String? = nil) async throws -> [Discussion] {
        var queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        if let visibility {
            queryItems.append(URLQueryItem(name: "visibility", value: visibility.rawValue))
        }
        if let type, !type.isEmpty {
            queryItems.append(URLQueryItem(name: "type", value: type))
        }
        return try await get("/api/discussions", query: queryItems)
    }

    func discussion(id: String, editLimit: Int? = nil, editBefore: Int64? = nil, includeEditTurns: Bool? = nil) async throws -> Discussion {
        var query: [URLQueryItem] = []
        if let editLimit {
            query.append(URLQueryItem(name: "edit_limit", value: String(editLimit)))
        }
        if let editBefore {
            query.append(URLQueryItem(name: "edit_before", value: String(editBefore)))
        }
        if let includeEditTurns {
            query.append(URLQueryItem(name: "include_edit_turns", value: includeEditTurns ? "true" : "false"))
        }
        return try await get("/api/discussions/\(id)", query: query)
    }

    /// Opens a web-player/deep-link discussion ID. Private owner podcasts are
    /// only visible through the authenticated detail endpoint; public podcasts
    /// remain available through the market endpoint.
    func playerDiscussion(id: String) async throws -> Discussion {
        do {
            return try await discussion(id: id)
        } catch APIError.http(404, _) {
            let discussion = try await marketStation(id: id)
            try? await joinDiscussion(id: id, token: nil)
            return discussion
        }
    }

    func parentPodcasts(limit: Int = 50, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        var queryItems = [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ]
        let trimmedQuery = query?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !trimmedQuery.isEmpty {
            queryItems.append(URLQueryItem(name: "q", value: trimmedQuery))
        }
        return try await get("/api/discussions/parent-podcasts", query: queryItems)
    }

    func parentPodcast(id: String) async throws -> PodcastReference {
        try await get("/api/discussions/\(id)/parent-podcast")
    }

    /// Fetches the generated summary document (Markdown body) for a podcast. The
    /// detail payload only carries a content-free `summary` descriptor; this is
    /// the separate endpoint the summary view calls on mount. Throws (404) when no
    /// summary exists yet.
    func summary(id: String, docType: String = "summary") async throws -> SummaryDocument {
        var query: [URLQueryItem] = []
        if docType != "summary" {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        return try await get("/api/discussions/\(id)/summary", query: query)
    }

    /// Starts or retries summary generation for an owned, finished podcast and
    /// returns the refreshed discussion so the toolbar can show the pending state.
    func generateSummary(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/summary/generate", body: EmptyRequest())
    }

    /// Starts or retries the backend-owned audiobook companion video render.
    func generateVideo(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/video/generate", body: EmptyRequest())
    }

    /// Fetches the generated mindmap node tree for a discussion podcast. The
    /// detail payload only carries a content-free `mindmap` descriptor; this is
    /// the separate endpoint the mindmap view calls on mount. Throws (404) when
    /// no mindmap exists yet.
    func mindmap(id: String) async throws -> MindmapDocument {
        try await get("/api/discussions/\(id)/mindmap")
    }

    /// Starts or retries mindmap generation for an owned, finished discussion
    /// and returns the refreshed discussion so the menu can show the pending state.
    func generateMindmap(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/mindmap/generate", body: EmptyRequest())
    }

    /// Persists the owner's edited mindmap tree (whole-tree replace).
    func saveMindmap(id: String, spec: MindmapSpec) async throws -> MindmapDocument {
        try await send("PUT", "/api/discussions/\(id)/mindmap", body: MindmapSaveRequest(mindmap: spec))
    }

    func discussionUIActions(id: String,
                             surface: String,
                             docType: String? = nil,
                             supportsPoints: Bool = false,
                             supportsFollowUp: Bool = false,
                             supportsCreateFromPlan: Bool = false,
                             supportsSignOut: Bool = false,
                             supportsChapterBatches: Bool = false,
                             supportsAlbums: Bool = false) async throws -> DiscussionUIActionsResponse {
        var query = [URLQueryItem(name: "surface", value: surface)]
        if let docType, !docType.isEmpty {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        if supportsPoints {
            query.append(URLQueryItem(name: "supports_points", value: "true"))
        }
        if supportsFollowUp {
            query.append(URLQueryItem(name: "supports_follow_up", value: "true"))
        }
        if supportsCreateFromPlan {
            query.append(URLQueryItem(name: "supports_create_from_plan", value: "true"))
        }
        if supportsSignOut {
            query.append(URLQueryItem(name: "supports_sign_out", value: "true"))
        }
        if supportsChapterBatches {
            query.append(URLQueryItem(name: "supports_chapter_batches", value: "true"))
        }
        if supportsAlbums {
            query.append(URLQueryItem(name: "supports_albums", value: "true"))
        }
        return try await get("/api/discussions/\(id)/ui-actions", query: query)
    }

    func homeUIActions(supportsPoints: Bool = false,
                       visibility: String? = nil,
                       type: String? = nil) async throws -> DiscussionUIActionsResponse {
        var query: [URLQueryItem] = []
        if supportsPoints {
            query.append(URLQueryItem(name: "supports_points", value: "true"))
        }
        if let visibility, !visibility.isEmpty {
            query.append(URLQueryItem(name: "visibility", value: visibility))
        }
        if let type, !type.isEmpty {
            query.append(URLQueryItem(name: "type", value: type))
        }
        return try await get("/api/home/ui-actions", query: query)
    }

    /// Downloads the summary rendered as a PDF (produced server-side via
    /// Cloudflare Browser Rendering, with ```mermaid blocks drawn as real
    /// diagrams) and writes it to a temporary file, returning the local URL ready
    /// to share/export. Throws 404 when no summary exists, 503 when PDF export
    /// isn't configured on the server.
    func downloadSummaryPDF(id: String, docType: String = "summary", title: String) async throws -> URL {
        var query: [URLQueryItem] = []
        if docType != "summary" {
            query.append(URLQueryItem(name: "doc_type", value: docType))
        }
        var req = request(method: "GET",
                          path: "/api/discussions/\(id)/summary/pdf",
                          query: query)
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pdf")
        } catch {
            apiLog.error("summary pdf export failed discussion=\(id, privacy: .public) docType=\(docType, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Downloads the summary rendered as a PowerPoint deck. The server creates
    /// the deck on first export and reuses the cached S3 artifact afterwards.
    func downloadSummaryPPTX(id: String, title: String) async throws -> URL {
        var req = request(method: "GET", path: "/api/discussions/\(id)/summary/pptx")
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pptx")
        } catch {
            apiLog.error("summary pptx export failed discussion=\(id, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Downloads the generated summary slide deck converted to a PDF.
    func downloadSummarySlidesPDF(id: String, title: String) async throws -> URL {
        var req = request(method: "GET", path: "/api/discussions/\(id)/summary/ppt/pdf")
        req.timeoutInterval = Self.summaryExportTimeout
        do {
            let (data, _) = try await perform(req)
            return try writeSummaryFile(data: data, title: title, ext: "pdf")
        } catch {
            apiLog.error("summary slides pdf export failed discussion=\(id, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Writes already-fetched summary Markdown to a temporary `.md` file and
    /// returns the local URL, so the Markdown export shares the PDF export's sheet.
    func writeSummaryMarkdown(_ markdown: String, title: String) throws -> URL {
        try writeSummaryFile(data: Data(markdown.utf8), title: title, ext: "md")
    }

    /// Writes summary export bytes to a uniquely-named temp file under
    /// `SummaryDownloads/`, sanitising the title into a safe base filename.
    func writeSummaryFile(data: Data, title: String, ext: String) throws -> URL {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("SummaryDownloads", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)

        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: " -_"))
        let sanitized = title.unicodeScalars.map { allowed.contains($0) ? Character($0) : "-" }
        let baseName = String(sanitized).trimmingCharacters(in: CharacterSet(charactersIn: " -_"))
        let name = baseName.isEmpty ? "Summary" : String(baseName.prefix(80))
        let url = directory.appendingPathComponent(name).appendingPathExtension(ext)
        try data.write(to: url, options: .atomic)
        return url
    }

    func planDiscussion(_ req: PlanRequest) async throws -> Discussion {
        try await send("POST", "/api/discussions/plan", body: req)
    }

    func improveDiscussion(id: String, instruction: String,
                           attachments: [Attachment] = []) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/improve",
                       body: DiscussionImproveRequest(instruction: instruction,
                                                      attachments: attachments.isEmpty ? nil : attachments))
    }

    /// Creates an empty placeholder discussion (status "planning") and returns it
    /// with a server id, so the client can navigate to the plan page and stream
    /// the plan into it. Decouples creation from the multi-minute planning run.
    /// Creates the placeholder discussion from the raw new-discussion form values
    /// (the JSONSchemaForm output for GET /api/precheck). The server reads every
    /// field, so the client posts the form verbatim without interpreting any key.
    func createDiscussion(form: FormData,
                          referenceDiscussionID: String? = nil) async throws -> Discussion {
        try await send("POST", "/api/discussions",
                       body: DiscussionCreateRequest(form: form,
                                                     referenceDiscussionID: referenceDiscussionID))
    }

    /// Creates a private planning discussion by copying the current plan from a
    /// public or owned market discussion.
    func createDiscussionFromPlan(id: String) async throws -> Discussion {
        try await send("POST", "/api/discussions/\(id)/create/plan", body: EmptyRequest())
    }

    /// Creates a discussion from the user's own uploaded audio (the upload-audio
    /// form values posted verbatim) and starts server-side transcription. The
    /// returned discussion is in the planning state; the plan chat shows the
    /// transcription progress and, once done, the transcript review.
    func createUploadAudioDiscussion(form: FormData) async throws -> Discussion {
        try await send("POST", "/api/discussions/upload-audio",
                       body: UploadAudioCreateRequest(form: form))
    }

    /// Mints a short-lived URL for replaying the user's original upload.
    func uploadedAudioPlaybackURL(id: String) async throws -> URL {
        let response: UploadedAudioPlaybackResponse = try await get(
            "/api/discussions/\(id)/uploaded-audio"
        )
        return response.url
    }

    /// Downloads the original upload once and reuses the purgeable on-disk copy
    /// for later segment replays, including after the transcript sheet reopens.
    func cachedUploadedAudioURL(id: String) async throws -> URL {
        try await UploadedAudioFileCache.shared.localURL(discussionID: id) { [self] in
            try await uploadedAudioPlaybackURL(id: id)
        }
    }

    /// Saves a direct user correction to one uploaded-audio transcript segment.
    func updateTranscriptSegment(id: String, index: Int,
                                 segment: TranscriptSegmentDTO) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/transcript/segments/\(index)",
            body: segment
        )
    }

    /// Saves uploaded-audio caption corrections. A single edit uses the direct
    /// indexed route; multiple edits share one batch request and plan update.
    func updateTranscriptSegments(id: String,
                                  updates: [TranscriptSegmentUpdate]) async throws -> Discussion {
        guard let first = updates.first else {
            throw APIError.invalidRequest("At least one transcript segment update is required.")
        }
        if updates.count == 1 {
            return try await updateTranscriptSegment(
                id: id,
                index: first.index,
                segment: first.segment
            )
        }
        return try await send(
            "PATCH",
            "/api/discussions/\(id)/transcript/segments",
            body: TranscriptSegmentBatchUpdateRequest(updates: updates)
        )
    }

    func addUploadedAudioSpeaker(id: String, name: String) async throws -> Discussion {
        try await send(
            "POST",
            "/api/discussions/\(id)/transcript/speakers",
            body: UploadedAudioSpeakerAddRequest(name: name)
        )
    }

    func renameUploadedAudioSpeaker(id: String, from: String, to: String) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/transcript/speakers",
            body: UploadedAudioSpeakerRenameRequest(from: from, to: to)
        )
    }

    /// Streaming plan generation into an existing placeholder discussion: emits
    /// progress steps, finishing with a `.done(Discussion)` event carrying the
    /// persisted plan. The discussion is already saved server-side, so a dropped
    /// stream leaves a recoverable record instead of losing the work.
    func planDiscussionStream(id: String, _ req: PlanRequest) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(path: "/api/discussions/\(id)/plan/stream", body: req)
    }

    /// Streaming plan edit: revises the plan while emitting progress steps,
    /// finishing with a `.done(Discussion)` event carrying the updated plan.
    func improveDiscussionStream(id: String, instruction: String,
                                 attachments: [Attachment] = []) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(
            path: "/api/discussions/\(id)/improve/stream",
            body: DiscussionImproveRequest(instruction: instruction,
                                           attachments: attachments.isEmpty ? nil : attachments)
        )
    }

    /// Streaming re-research: reads the given links, folds them into the plan,
    /// and emits the updated discussion as a terminal `.done` event.
    func addDiscussionSourcesStream(id: String, urls: [String]) -> AsyncThrowingStream<PlanStreamEvent, Error> {
        streamPlan(path: "/api/discussions/\(id)/sources/stream", body: AddSourcesRequest(urls: urls))
    }

}
