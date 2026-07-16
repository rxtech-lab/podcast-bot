import Foundation
import JSONSchemaForm
import OSLog

extension APIClient {
    // MARK: - Albums

    func albums() async throws -> [AlbumDTO] {
        try await get("/api/albums")
    }

    func album(id: String) async throws -> AlbumDetailResponse {
        try await get("/api/albums/\(id)")
    }

    func publicAlbum(id: String) async throws -> AlbumDetailResponse {
        try await get("/api/market/albums/\(id)")
    }

    func createAlbum(title: String, discussionIDs: [String]) async throws -> AlbumDTO {
        try await send("POST", "/api/albums",
                       body: AlbumCreateRequest(title: title, discussionIDs: discussionIDs))
    }

    /// Creates an album from the raw new-album form values (the JSONSchemaForm
    /// output for GET /api/precheck's `new_album` form). The server owns every
    /// form key, so the values are posted verbatim.
    func createAlbum(form: FormData) async throws -> AlbumDTO {
        try await send("POST", "/api/albums", body: form)
    }

    func addToAlbum(id: String, discussionIDs: [String]) async throws -> AlbumDTO {
        try await send("POST", "/api/albums/\(id)/discussions",
                       body: AlbumAddMembersRequest(discussionIDs: discussionIDs))
    }

    func publishAlbum(id: String, mode: String, discussionIDs: [String], cover: DiscussionCover) async throws -> AlbumDetailResponse {
        try await send("POST", "/api/albums/\(id)/publish",
                       body: AlbumPublishRequest(mode: mode, discussionIDs: discussionIDs, cover: cover))
    }

    func removeFromAlbum(id: String, discussionID: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/albums/\(id)/discussions/\(discussionID)"))
    }

    /// Server-rendered album toolbar menu (same shape as the podcast toolbars).
    func albumUIActions(id: String) async throws -> DiscussionUIActionsResponse {
        try await get("/api/albums/\(id)/ui-actions")
    }

    func renameAlbum(id: String, title: String) async throws -> AlbumDTO {
        try await send("PATCH", "/api/albums/\(id)", body: AlbumRenameRequest(title: title))
    }

    /// Generates AI cover art for an album; the result is persisted separately
    /// via `updateAlbumCover`, mirroring the discussion cover flow.
    func generateAlbumCover(id: String, prompt: String) async throws -> DiscussionCover {
        let response: CoverGenerateResponse = try await send(
            "POST",
            "/api/albums/\(id)/cover/generate",
            body: CoverGenerateRequest(prompt: prompt)
        )
        return response.cover
    }

    /// Persists a cover on an album (gradient, uploaded image, or generated AI art).
    func updateAlbumCover(id: String, cover: DiscussionCover) async throws -> AlbumDTO {
        try await send("PATCH", "/api/albums/\(id)/cover", body: CoverUpdateRequest(cover: cover))
    }

    /// Removes the album grouping; the member podcasts are kept.
    func deleteAlbum(id: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/albums/\(id)"))
    }

    func deleteDiscussion(id: String) async throws {
        _ = try await perform(request(method: "DELETE", path: "/api/discussions/\(id)"))
    }

    func renameDiscussion(id: String, title: String) async throws -> Discussion {
        try await send("PATCH", "/api/discussions/\(id)", body: DiscussionRenameRequest(title: title))
    }

    func updateDiscussionVisibility(id: String,
                                    visibility: DiscussionVisibility,
                                    cover: DiscussionCover? = nil) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/visibility",
            body: DiscussionVisibilityRequest(visibility: visibility, cover: cover)
        )
    }

    /// Persists a cover on a discussion without publishing it, so any discussion
    /// can carry cover art (set from the new-discussion sheet or cover editor).
    func updateDiscussionCover(id: String, cover: DiscussionCover) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/cover",
            body: CoverUpdateRequest(cover: cover)
        )
    }

    /// The models available for per-speaker assignment, fetched live from the
    /// gateway and cached server-side (24h). Empty when the gateway exposes no
    /// model listing.
    func availableModels() async throws -> [ModelInfoDTO] {
        let response: ModelsResponseDTO = try await get("/api/models")
        return response.models ?? []
    }

    /// The caller's resolved subscription permissions, cached server-side (60s).
    /// Gates the app's native model/voice pickers, AI cover, and studio types.
    func entitlements() async throws -> Entitlements {
        try await get("/api/entitlements")
    }

    /// The Azure TTS voices available for per-speaker assignment, fetched from
    /// Azure's voices/list endpoint and cached server-side (24h).
    func availableVoices() async throws -> [VoiceInfoDTO] {
        let response: VoicesResponseDTO = try await get("/api/voices")
        return response.voices ?? []
    }

    /// A playable URL for a short localized sample of one Azure voice. The
    /// server owns the sample text for each supported plan language.
    func previewVoice(voice: String, language: String) async throws -> URL {
        let response: VoicePreviewResponseDTO = try await send(
            "POST",
            "/api/voices/preview",
            body: VoicePreviewRequest(voice: voice, language: language)
        )
        guard let url = URL(string: response.url) else {
            throw APIError.decoding("invalid preview url")
        }
        return url
    }

    /// The content types currently supported by the planner. Today this is a
    /// single `discussion` entry, but the backend owns the list so clients do
    /// not hard-code future options.
    func discussionTypes() async throws -> [DiscussionTypeDTO] {
        let response: DiscussionTypesResponseDTO = try await get("/api/discussion-types")
        return response.types ?? []
    }

    /// Plan templates available for the selected content type. The server owns
    /// the available list so clients pick up new templates without an app update.
    func templates(type: String) async throws -> [PlanTemplateDTO] {
        let query = [URLQueryItem(name: "type", value: type)]
        let url = request(method: "GET", path: "/api/templates", query: query).url?.absoluteString ?? "/api/templates"
        apiLog.debug("templates request type=\(type, privacy: .public) url=\(url, privacy: .public)")
        do {
            let response: PlanTemplatesResponseDTO = try await get("/api/templates", query: query)
            let templates = response.templates ?? []
            let templateIDs = templates.map(\.id).joined(separator: ",")
            apiLog.debug("templates response type=\(type, privacy: .public) count=\(templates.count, privacy: .public) ids=\(templateIDs, privacy: .public)")
            return templates
        } catch {
            apiLog.error("templates request failed type=\(type, privacy: .public) url=\(url, privacy: .public) error=\(error.localizedDescription, privacy: .public)")
            throw error
        }
    }

    /// Server-owned client bootstrap metadata. The new discussion form schema,
    /// localized labels, and supported native actions are negotiated here.
    func precheck() async throws -> PrecheckResponseDTO {
        let response: PrecheckResponseDTO = try await get("/api/precheck")
        // /api/precheck is reachable during maintenance and carries the active
        // window, so the bootstrap call surfaces the message proactively — before
        // any blocked (503) request forces it.
        if let info = response.maintenance {
            MaintenanceMonitor.report(info)
        }
        return response
    }

    // MARK: - Push

    func registerPushToken(_ token: String, environment: String) async throws {
        try await sendNoContent("POST", "/api/push-tokens",
                                body: PushTokenRequest(token: token,
                                                       environment: environment))
    }

    func deletePushToken(_ token: String, environment: String) async throws {
        try await sendNoContent("DELETE", "/api/push-tokens",
                                body: PushTokenRequest(token: token,
                                                       environment: environment))
    }

    /// Changes the LLM model for one speaker (host or discussant, by name) in a
    /// discussion's plan and returns the updated discussion.
    func updateSpeakerModel(id: String, speaker: String, model: String) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/speaker-model",
            body: SpeakerModelRequest(speaker: speaker, model: model)
        )
    }

    /// Changes the TTS voice for one speaker (host, discussant, or audiobook
    /// narrator, by name) in a discussion's plan and returns the updated
    /// discussion. An empty voice clears the override (back to automatic).
    func updateSpeakerVoice(id: String, speaker: String, voice: String) async throws -> Discussion {
        try await send(
            "PATCH",
            "/api/discussions/\(id)/speaker-voice",
            body: SpeakerVoiceRequest(speaker: speaker, voice: voice)
        )
    }

    func generateDiscussionCover(id: String, prompt: String) async throws -> DiscussionCover {
        let response: CoverGenerateResponse = try await send(
            "POST",
            "/api/discussions/\(id)/cover/generate",
            body: CoverGenerateRequest(prompt: prompt)
        )
        return response.cover
    }

    func appendDiscussionLine(id: String, line: DiscussionLineRequest, shareToken: String? = nil) async throws {
        let payload = try JSONEncoder().encode(line)
        var req = request(method: "POST", path: "/api/discussions/\(id)/lines", body: payload)
        if let shareToken, !shareToken.isEmpty {
            req.setValue(shareToken, forHTTPHeaderField: "X-Share-Token")
        }
        _ = try await perform(req)
    }

    // MARK: - Sharing

    /// Mints an expiring private share link for an owned discussion.
    func createShare(discussionID: String, ttlSeconds: Int) async throws -> DiscussionShareLink {
        let dto: ShareLinkDTO = try await send(
            "POST",
            "/api/discussions/\(discussionID)/shares",
            body: ShareCreateRequest(ttlSeconds: ttlSeconds)
        )
        guard let link = DiscussionShareLink(dto: dto) else {
            throw APIError.decoding("invalid share link url")
        }
        return link
    }

    /// Lists the active (non-expired, non-revoked) share links for a discussion.
    func listShares(discussionID: String) async throws -> [DiscussionShareLink] {
        let dtos: [ShareLinkDTO] = try await get("/api/discussions/\(discussionID)/shares")
        return dtos.compactMap(DiscussionShareLink.init(dto:))
    }

    /// Revokes a share token so its link stops working immediately.
    func revokeShare(discussionID: String, token: String) async throws {
        _ = try await perform(request(method: "DELETE",
                                      path: "/api/discussions/\(discussionID)/shares/\(pathComponent(token))"))
    }

    /// Records the caller as a participant, enforcing the per-discussion cap.
    /// Pass the share token when joining a private discussion via a link.
    /// Maps HTTP 409 to `APIError.participantCapReached`.
    func joinDiscussion(id: String, token: String?) async throws {
        do {
            try await sendNoContent("POST", "/api/discussions/\(id)/join",
                                    body: DiscussionJoinRequest(token: token))
        } catch APIError.http(409, _) {
            throw APIError.participantCapReached
        }
    }

    /// Resolves a private share token, joins (enforcing the cap), and returns the
    /// discussion with its transcript so the client can open the player. Maps
    /// HTTP 409 to `.participantCapReached` and 410 to a clear "link expired".
    func joinViaShare(token: String, language: String? = nil) async throws -> Discussion {
        do {
            let payload = try JSONEncoder().encode(EmptyRequest())
            let query = language.map { [URLQueryItem(name: "language", value: $0)] } ?? []
            let (data, _) = try await perform(request(method: "POST",
                                                      path: "/api/share/\(pathComponent(token))/join",
                                                      body: payload,
                                                      query: query))
            return try decode(data)
        } catch APIError.http(409, _) {
            throw APIError.participantCapReached
        } catch APIError.http(410, _) {
            throw APIError.invalidRequest(String(localized: "This share link has expired or was revoked.",
                                                 comment: "Shown when opening an expired/revoked share link"))
        }
    }

    // MARK: - Marketplace

    func marketStations(limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/stations", limit: limit, offset: offset, query: query)
    }

    func likedMarketStations(limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/stations/liked", limit: limit, offset: offset, query: query)
    }

    func marketStation(id: String, language: String? = nil) async throws -> Discussion {
        let query = language.map { [URLQueryItem(name: "language", value: $0)] } ?? []
        return try await get("/api/market/stations/\(id)", query: query)
    }

    func likeMarketStation(id: String) async throws -> Discussion {
        try await send("POST", "/api/market/stations/\(id)/like", body: EmptyRequest())
    }

    func unlikeMarketStation(id: String) async throws -> Discussion {
        let (data, _) = try await perform(request(method: "DELETE", path: "/api/market/stations/\(id)/like"))
        return try decode(data)
    }

    func marketProfile() async throws -> MarketProfile {
        try await get("/api/market/profile")
    }

    func creatorProfile(id: String) async throws -> CreatorProfile {
        try await get("/api/market/creators/\(pathComponent(id))")
    }

    func creatorStations(id: String, limit: Int = 20, offset: Int = 0, query: String? = nil) async throws -> [Discussion] {
        try await marketList(path: "/api/market/creators/\(pathComponent(id))/stations", limit: limit, offset: offset, query: query)
    }

    func followedCreators(limit: Int = 20, offset: Int = 0) async throws -> [CreatorProfile] {
        try await get("/api/market/creators/following", query: [
            URLQueryItem(name: "limit", value: String(limit)),
            URLQueryItem(name: "offset", value: String(offset)),
        ])
    }

    func followCreator(id: String) async throws -> CreatorProfile {
        try await send("POST", "/api/market/creators/\(pathComponent(id))/follow", body: EmptyRequest())
    }

    func unfollowCreator(id: String) async throws -> CreatorProfile {
        let (data, _) = try await perform(request(method: "DELETE", path: "/api/market/creators/\(pathComponent(id))/follow"))
        return try decode(data)
    }

}
