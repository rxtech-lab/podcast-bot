import Foundation
import JSONSchemaForm

struct PlanProgressEvent: Decodable, Sendable {
    var phase: String
    var text: String
}

/// POST /api/plan and /api/plan/improve response body.
struct PlanResponse: Codable, Sendable {
    var script: ScriptDTO
    var markdown: String?
    var sources: [SourceDTO]?
    var researched: Bool?
}

/// POST /api/plan/improve request body.
struct PlanImproveRequest: Codable, Sendable {
    var previousScript: ScriptDTO
    var instruction: String
    var attachments: [Attachment]?
}

struct DiscussionImproveRequest: Codable, Sendable {
    var instruction: String
    var attachments: [Attachment]?
}

/// videoConfig portion of POST /api/jobs/json (audio-only feed).
struct VideoConfigDTO: Codable, Sendable {
    var audio_only: Bool = true
    var soft_subs: Bool = true
    var burn_subs: Bool = false
    var resolution: String = "1080p"
}

/// POST /api/jobs/json request body.
struct JobSubmitRequest: Codable, Sendable {
    var script: ScriptDTO
    var videoConfig: VideoConfigDTO = VideoConfigDTO()
}

/// POST /api/jobs/json response.
struct JobSubmitResponse: Codable, Sendable {
    var id: String
}

struct JobMessageRequest: Codable, Sendable {
    var text: String
    var username: String
    var discussionID: String
    /// Set when a shared participant comments on a private discussion's live job.
    var shareToken: String?
    /// Playback URL and durable storage key for a voice message. The orchestrator
    /// receives only `text` (the on-device transcript); audio is persisted so
    /// other participants can replay it.
    var audioURL: String?
    var audioKey: String?

    enum CodingKeys: String, CodingKey {
        case text
        case username
        case discussionID = "discussion_id"
        case shareToken = "share_token"
        case audioURL = "audio_url"
        case audioKey = "audio_key"
    }
}

struct DiscussionGenerateRequest: Codable, Sendable {
    var videoConfig: VideoConfigDTO = VideoConfigDTO()
    var language: String?
    /// Audiobook chapter batch selection (1-based indices into the plan's full
    /// chapter list, max 5 per batch). nil lets the server default to the
    /// first pending chapters.
    var chapters: [Int]?
}

struct DiscussionLanguageRequest: Codable, Sendable {
    var language: String
}

// MARK: - Audiobook chapter batches

/// One chapter of the root plan annotated with its generation progress
/// (GET /api/discussions/{id}/chapters).
struct ChapterStatusDTO: Codable, Hashable, Sendable, Identifiable {
    var index: Int
    var title: String
    var summary: String
    var mode: String?
    var speakers: [String]?
    /// "done" | "generating" | "pending"
    var status: String
    var discussionID: String?

    var id: Int { index }
    var isDone: Bool { status == "done" }
    var isGenerating: Bool { status == "generating" }
    var isPending: Bool { status == "pending" }

    enum CodingKeys: String, CodingKey {
        case index, title, summary, mode, speakers, status
        case discussionID = "discussion_id"
    }
}

/// GET /api/discussions/{id}/chapters response.
struct ChaptersResponse: Codable, Sendable {
    var rootID: String
    var albumID: String?
    var maxBatchSize: Int
    var chapters: [ChapterStatusDTO]

    var pendingChapters: [ChapterStatusDTO] { chapters.filter(\.isPending) }

    enum CodingKeys: String, CodingKey {
        case rootID = "root_id"
        case albumID = "album_id"
        case maxBatchSize = "max_batch_size"
        case chapters
    }
}

/// POST /api/discussions/{id}/chapters/generate request body.
struct ChaptersGenerateRequest: Codable, Sendable {
    var chapters: [Int]
    var videoConfig: VideoConfigDTO = VideoConfigDTO()
    var language: String?
}

// MARK: - Albums

/// Compact album descriptor attached to discussion list rows (`album` field)
/// so the home screen can group linked podcasts.
struct AlbumSummaryDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var kind: String?
    var cover: DiscussionCover?
    var episodeCount: Int?

    enum CodingKeys: String, CodingKey {
        case id, title, kind, cover
        case episodeCount = "episode_count"
    }
}

/// A full album row from GET /api/albums / GET /api/albums/{id}.
struct AlbumDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var kind: String?
    var rootDiscussionID: String?
    var cover: DiscussionCover?
    var episodeCount: Int?
    var isOwner: Bool?
    var createdAt: String?
    var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case id, title, kind, cover
        case rootDiscussionID = "root_discussion_id"
        case episodeCount = "episode_count"
        case isOwner = "is_owner"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}

/// GET /api/albums/{id} response: the album plus its episodes in album order.
struct AlbumDetailResponse: Codable, Sendable {
    var album: AlbumDTO
    var episodes: [Discussion]
}

/// POST /api/albums request body.
struct AlbumCreateRequest: Codable, Sendable {
    var title: String
    var discussionIDs: [String]

    enum CodingKeys: String, CodingKey {
        case title
        case discussionIDs = "discussion_ids"
    }
}

/// PATCH /api/albums/{id} request body.
struct AlbumRenameRequest: Codable, Sendable {
    var title: String
}

/// POST /api/albums/{id}/discussions request body.
struct AlbumAddMembersRequest: Codable, Sendable {
    var discussionIDs: [String]

    enum CodingKeys: String, CodingKey {
        case discussionIDs = "discussion_ids"
    }
}

/// POST /api/albums/{id}/publish request body.
struct AlbumPublishRequest: Codable, Sendable {
    var mode: String
    var discussionIDs: [String]
    var cover: DiscussionCover

    enum CodingKeys: String, CodingKey {
        case mode
        case discussionIDs = "discussion_ids"
        case cover
    }
}

/// GET /api/jobs/{id} response.
struct JobLogDTO: Codable, Hashable, Sendable {
    var ts: Int
    var kind: String
    var text: String
}

struct JobStatusDTO: Codable, Sendable {
    var id: String
    var status: String          // pending | running | done | error
    var title: String?
    var type: String?
    var error: String?
    var has_audio: Bool?
    var audio_only: Bool?
    var download_url: String?
    var phase: String?
    var phase_label: String?
    var elapsed_ms: Int?
    var remaining_ms: Int?
    var prompt_tokens: Int?
    var completion_tokens: Int?
    var total_tokens: Int?
    var llm_cost_usd: Double?
    var llm_cost_known: Bool?
    var tts_cost_usd: Double?
    var music_cost_usd: Double?
    var logs: [JobLogDTO]?

    var isDone: Bool { status == "done" }
    var isError: Bool { status == "error" }

}

/// Response of GET /api/points/balance.
struct PointsBalanceResponse: Decodable, Sendable {
    var balance: Int
}

/// One ledger entry in the points-usage history (signed change + running balance).
struct PointsLedgerEntry: Decodable, Identifiable, Sendable {
    var id: Int64
    var delta: Int
    var reason: String
    var balanceAfter: Int
    /// Unix milliseconds.
    var createdAt: Int64

    enum CodingKeys: String, CodingKey {
        case id
        case delta
        case reason
        case balanceAfter = "balance_after"
        case createdAt = "created_at"
    }

    var date: Date { Date(timeIntervalSince1970: Double(createdAt) / 1000) }
}

/// Response of GET /api/points/history.
struct PointsHistoryResponse: Decodable, Sendable {
    var balance: Int
    var entries: [PointsLedgerEntry]
    var hasMore: Bool

    enum CodingKeys: String, CodingKey {
        case balance
        case entries
        case hasMore = "has_more"
    }
}

/// Body of a 402 response when the user lacks the points to start an action.
struct InsufficientPointsResponse: Decodable, Sendable {
    var error: String
    var requiredPoints: Int
    var balance: Int

    enum CodingKeys: String, CodingKey {
        case error
        case requiredPoints = "required_points"
        case balance
    }
}

/// Describes an active scheduled-maintenance window. The engine returns this in
/// the 503 body while the app is paused (`{ "maintenance": { ... } }`) and inline
/// in GET /api/config and GET /api/precheck. Surfaced to the user as a blocking
/// alert so they see the operator's message instead of the raw 503 JSON.
struct MaintenanceInfo: Codable, Equatable, Sendable {
    /// Identifies the window so the client can show a scheduled heads-up only once.
    var id: Int
    var title: String?
    var message: String
    var startAt: Date?
    var endAt: Date?
    /// True when the app is currently paused by this window; false for an upcoming
    /// scheduled window. Active windows always alert; scheduled ones alert once.
    var active: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case title
        case message
        case startAt = "start_at"
        case endAt = "end_at"
        case active
    }

    init(id: Int, title: String?, message: String, startAt: Date?, endAt: Date?, active: Bool) {
        self.id = id
        self.title = title
        self.message = message
        self.startAt = startAt
        self.endAt = endAt
        self.active = active
    }

    // Custom decoder so the shared `JSONDecoder()` in mapHTTPError (which has no
    // date strategy configured) still parses the RFC3339 timestamps the engine
    // sends (e.g. "2026-07-07T04:35:00+08:00").
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decodeIfPresent(Int.self, forKey: .id) ?? 0
        title = try container.decodeIfPresent(String.self, forKey: .title)
        message = try container.decode(String.self, forKey: .message)
        startAt = try container.decodeIfPresent(String.self, forKey: .startAt).flatMap(Self.parseDate)
        endAt = try container.decodeIfPresent(String.self, forKey: .endAt).flatMap(Self.parseDate)
        active = try container.decodeIfPresent(Bool.self, forKey: .active) ?? false
    }

    private static func parseDate(_ raw: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: raw) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: raw)
    }

    /// The alert title, falling back to a generic heading when the operator left
    /// the title blank.
    var displayTitle: String {
        let trimmed = title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty
            ? String(localized: "Scheduled maintenance",
                     comment: "Default title of the maintenance alert when the operator set no title")
            : trimmed
    }

    /// The operator's message plus a time hint: when service is expected back
    /// (active window) or when the pause is scheduled to begin (upcoming window).
    var displayMessage: String {
        var parts: [String] = []
        let trimmed = message.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty { parts.append(trimmed) }
        if active {
            if let endAt {
                let when = endAt.formatted(date: .abbreviated, time: .shortened)
                parts.append(String(localized: "Service should be back by \(when).",
                                    comment: "Appended to an active maintenance message; 'when' is a formatted date/time"))
            }
        } else if let startAt {
            let start = startAt.formatted(date: .abbreviated, time: .shortened)
            if let endAt {
                let end = endAt.formatted(date: .abbreviated, time: .shortened)
                parts.append(String(localized: "Scheduled from \(start) to \(end).",
                                    comment: "Appended to an upcoming maintenance message; start and end are formatted date/times"))
            } else {
                parts.append(String(localized: "Scheduled to begin \(start).",
                                    comment: "Appended to an upcoming maintenance message with no end time; 'start' is a formatted date/time"))
            }
        }
        return parts.isEmpty
            ? String(localized: "The app is temporarily unavailable for scheduled maintenance. Please try again later.",
                     comment: "Fallback maintenance message when the server sent no message text")
            : parts.joined(separator: "\n\n")
    }
}

/// Wrapper for the 503 maintenance body: `{ "maintenance": { ... } }`.
struct MaintenanceResponse: Decodable, Sendable {
    var maintenance: MaintenanceInfo
}

/// One persisted transcript line returned by GET /api/jobs/{id}/transcript.
struct TranscriptDTO: Codable, Hashable, Sendable {
    var speaker: String
    var role: String
    var side: String?
    var text: String
    var at: String?
    var sources: [SourceDTO]? = nil
    var judgementComment: String? = nil
    var imageURL: String? = nil
    /// Audio-timeline position of this line in milliseconds (audiobook
    /// illustration lines only). Drives time-synced artwork switching.
    var audioOffsetMS: Int? = nil

    enum CodingKeys: String, CodingKey {
        case speaker
        case role
        case side
        case text
        case at
        case sources
        case judgementComment = "judgement_comment"
        case imageURL = "image_url"
        case audioOffsetMS = "audio_offset_ms"
    }
}

/// One entry of the canonical audiobook illustration timeline returned by
/// GET /api/jobs/{id}/illustrations: the image on screen from `startMS` until
/// the next cue. The backend owns all timing; the player consumes it verbatim.
struct IllustrationCueDTO: Codable, Hashable, Sendable {
    var startMS: Int
    var imageURL: String
    var caption: String? = nil

    enum CodingKeys: String, CodingKey {
        case startMS = "start_ms"
        case imageURL = "image_url"
        case caption
    }
}

struct IllustrationsResponseDTO: Decodable, Sendable {
    var illustrations: [IllustrationCueDTO]
}

struct CaptionDownloadFormatsResponse: Decodable, Sendable {
    var formats: [CaptionDownloadFormat]
}

/// One caption export option supplied by the backend. The app deliberately
/// does not switch on `id`, so newly registered server formats appear in the
/// download sheet without an iOS release.
struct CaptionDownloadFormat: Decodable, Sendable, Identifiable, Hashable {
    var id: String
    var displayName: String
    var fileExtension: String
    var contentType: String

    enum CodingKeys: String, CodingKey {
        case id
        case displayName = "display_name"
        case fileExtension = "file_extension"
        case contentType = "content_type"
    }
}

/// One event from GET /api/jobs/{id}/ws: `{ "event": ..., "data": {...} }`.
struct JobEventEnvelope: Decodable, Sendable {
    var event: String
    var data: JobEventData?
}

struct JobEventData: Decodable, Sendable {
    var channel_id: String?
    var speaker: String?
    var role: String?
    var text: String?
    var done: Bool?
    var isUserMessage: Bool?
    var sender_user_id: String?
    var audio_url: String?
    /// Carried by an audiobook's image-only transcript event: a generated
    /// illustration to render inline in the chat at this point in the stream.
    var image_url: String?
    /// Audio-timeline position (ms) of an image-only transcript event, so the
    /// player can switch artwork in sync with playback.
    var audio_offset_ms: Int?
    var sources: [SourceDTO]?
    var judgement_comment: String?
    var agent: String?
    var activity: String?
    var detail: String?
    var phase: String?
    var label: String?
    var elapsed_ms: Int?
    var remaining_ms: Int?
    /// Carried by the `summary_ready` event so the client can react to the
    /// matching document kind; the body is fetched separately on refresh.
    var doc_type: String?
    var status: String?
    /// Carried by `resource_updated`; clients use it as an invalidation signal
    /// and re-fetch the resource by id.
    var action: String?
    var resource_type: String?
    var resource_id: String?
    var deep_link: String?
    var id: String?
    var changes: [String]?
}

struct DiscussionLineRequest: Codable, Sendable {
    var speaker: String
    var role: String
    var side: String?
    var text: String
    var startMS: Int?
    var isUser: Bool
    /// Playback URL and durable storage key for a voice message. The server keeps
    /// the key so it can re-sign the URL on later reads; the agent sees only text.
    var audioURL: String? = nil
    var audioKey: String? = nil

    enum CodingKeys: String, CodingKey {
        case speaker
        case role
        case side
        case text
        case startMS = "start_ms"
        case isUser = "is_user"
        case audioURL = "audio_url"
        case audioKey = "audio_key"
    }
}
