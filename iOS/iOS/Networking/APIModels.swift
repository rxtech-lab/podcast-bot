import Foundation
import JSONSchemaForm

/// DTOs mirroring the debate-bot engine JSON. Field names match the Go structs'
/// json tags (snake_case).

struct EmptyRequest: Codable, Sendable {}

/// POST/DELETE /api/push-tokens. Environment is persisted server-side so
/// sandbox and production APNs tokens never get mixed.
struct PushTokenRequest: Codable, Sendable {
    var token: String
    var environment: String
    var platform: String = "ios"
}

struct AgentDTO: Codable, Hashable, Sendable {
    var name: String
    var model: String?
    var aspect: String?
}

struct AudioBookSpeakerDTO: Codable, Hashable, Sendable {
    var name: String
    var gender: String?
    var description: String?
}

struct AudioBookChapterDTO: Codable, Hashable, Sendable {
    var title: String
    var summary: String
    var mode: String?
    var speakers: [String]?
}

struct SourceDTO: Codable, Hashable, Sendable {
    var title: String
    var url: String
    var snippet: String?
    var markdown: String?
}

/// One selectable LLM from GET /api/models (config.ModelInfo). The roster is
/// fetched live from the gateway and cached server-side; `label` defaults to the
/// raw id when the gateway gives no friendlier name.
struct ModelInfoDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var label: String
    var provider: String?

    var displayLabel: String { label.isEmpty ? id : label }
}

/// Body of GET /api/models.
struct ModelsResponseDTO: Codable, Sendable {
    var models: [ModelInfoDTO]?
}

/// One content type selectable in the new-discussion sheet. The backend
/// currently returns only `discussion`, but the response is an array so the UI
/// does not need to change when more plan types become available.
struct DiscussionTypeDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var label: String

    var displayLabel: String { label.isEmpty ? id : label }
}

/// Body of GET /api/discussion-types.
struct DiscussionTypesResponseDTO: Codable, Sendable {
    var types: [DiscussionTypeDTO]?
}

/// One selectable plan template from GET /api/templates. The backend also
/// returns the raw JSON schema, which Codable ignores here because the app only
/// needs display metadata.
struct PlanTemplateDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var name: String
    var description: String?

    var displayName: String { name.isEmpty ? id : name }
}

/// Body of GET /api/templates.
struct PlanTemplatesResponseDTO: Codable, Sendable {
    var templates: [PlanTemplateDTO]?
}

struct PrecheckResponseDTO: Codable, Sendable {
    var newDiscussion: PrecheckNewDiscussionDTO

    enum CodingKeys: String, CodingKey {
        case newDiscussion = "new_discussion"
    }
}

struct PrecheckNewDiscussionDTO: Codable, Sendable {
    var form: PrecheckFormDTO
}

struct PrecheckFormDTO: Codable, Sendable {
    var title: String
    var description: String?
    var submitTitle: String
    var cancelTitle: String
    var loadingTitle: String
    var schema: [String: AnyCodable]
    var uiSchema: [String: AnyCodable]?
    var initialData: [String: AnyCodable]?
    var actions: [PrecheckFormActionDTO]?

    enum CodingKeys: String, CodingKey {
        case title
        case description
        case submitTitle = "submit_title"
        case cancelTitle = "cancel_title"
        case loadingTitle = "loading_title"
        case schema
        case uiSchema = "ui_schema"
        case initialData = "initial_data"
        case actions
    }
}

struct PrecheckFormActionDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var kind: String
    var title: String
    var description: String?
    var systemImage: String?
    var deepLink: String?

    enum CodingKeys: String, CodingKey {
        case id
        case kind
        case title
        case description
        case systemImage = "system_image"
        case deepLink = "deep_link"
    }
}

/// Body of PATCH /api/discussions/{id}/speaker-model.
struct SpeakerModelRequest: Codable, Sendable {
    var speaker: String
    var model: String
}

/// The discussion script (config.DebateTopic). Only the discussion-relevant
/// fields are modeled; the engine fills defaults for anything omitted.
struct ScriptDTO: Codable, Hashable, Sendable {
    var title: String
    var type: String
    var language: String
    var channel: String?
    var total_minutes: Int?
    var segment_max_seconds: Int?
    var tts_provider: String?
    var resolution: String?
    var storage: String?
    var host: AgentDTO?
    var discussants: [AgentDTO]?
    var commander: AgentDTO?
    var audioBookHost: AgentDTO?
    var audioBookStyle: String?
    var audioBookSpeakers: [AudioBookSpeakerDTO]?
    var audioBookChapters: [AudioBookChapterDTO]?
    var background: String?
    var surface: String?
    var sources: [SourceDTO]?

    enum CodingKeys: String, CodingKey {
        case title, type, language, channel
        case total_minutes, segment_max_seconds, tts_provider, resolution, storage
        case host, discussants, commander, background, surface, sources
        case audioBookHost = "audio_book_host"
        case audioBookStyle = "audio_book_style"
        case audioBookSpeakers = "audio_book_speakers"
        case audioBookChapters = "audio_book_chapters"
    }
}

/// A user-uploaded reference file. Documents carry markdown parsed by
/// markitdown; images carry a URL so the engine can pass them to the model.
struct Attachment: Codable, Hashable, Sendable {
    var filename: String
    var markdown: String?
    var url: String?
    var mimeType: String?

    enum CodingKeys: String, CodingKey {
        case filename
        case markdown
        case url
        case mimeType = "mime_type"
    }
}

/// A podcast selected as context for a follow-up discussion.
struct PodcastReference: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var topic: String

    var displayTitle: String {
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedTitle.isEmpty { return trimmedTitle }
        let trimmedTopic = topic.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmedTopic.isEmpty ? String(localized: "Podcast", comment: "Fallback reference podcast title") : trimmedTopic
    }

    var subtitle: String {
        topic.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// POST /api/discussions request body: creates an empty placeholder discussion
/// (status "planning") so the client gets an id before streaming the plan.
///
/// `form` is the raw JSONSchemaForm output for the new-discussion form (served by
/// GET /api/precheck), posted verbatim. The server owns every form key, so the
/// client never reads or transforms field values — it just hands the form back.
/// `referenceDiscussionID` is contextual (set when planning from an existing
/// podcast), not a form field.
struct DiscussionCreateRequest: Codable, Sendable {
    var form: FormData
    var referenceDiscussionID: String?

    enum CodingKeys: String, CodingKey {
        case form
        case referenceDiscussionID = "reference_discussion_id"
    }
}

/// POST /api/plan request body.
struct PlanRequest: Codable, Sendable {
    var type: String = "discussion"
    var topic: String
    var language: String = "en-US"
    var discussants: Int = 3
    var template: String?
    var research: Bool = true
    var attachments: [Attachment]?
    var reference: PodcastReference?
}

/// POST /api/discussions/{id}/planning/stream request body: the user's message
/// plus any document attachments to ground the conversational plan.
struct PlanningStreamRequest: Codable, Sendable {
    var prompt: String = ""
    var language: String?
    var attachments: [Attachment]?
    var resume: Bool?
}

/// POST /api/discussions/{id}/planning/answer request body: answers a pending
/// question (or skips it with action "rejected"), resuming the agent loop.
struct PlanningAnswerRequest: Codable, Sendable {
    var questionId: String
    var action: String
    var language: String?
    var answers: [[String: AnyCodable]]

    enum CodingKeys: String, CodingKey {
        case questionId = "question_id"
        case action
        case language
        case answers
    }
}

/// POST /api/uploads response body: the original filename, either parsed
/// markdown or a direct image URL, and the content type.
struct UploadResponse: Codable, Sendable {
    var filename: String
    var key: String?
    var markdown: String?
    var url: String
    var mimeType: String?

    enum CodingKeys: String, CodingKey {
        case filename
        case key
        case markdown
        case url
        case mimeType = "mime_type"
    }
}

struct UploadPresignRequest: Codable, Sendable {
    var filename: String
    var mimeType: String

    enum CodingKeys: String, CodingKey {
        case filename
        case mimeType = "mime_type"
    }
}

struct UploadPresignResponse: Codable, Sendable {
    var key: String
    var uploadURL: URL
    var method: String
    var headers: [String: String]

    enum CodingKeys: String, CodingKey {
        case key
        case uploadURL = "upload_url"
        case method
        case headers
    }
}

struct UploadCompleteRequest: Codable, Sendable {
    var key: String
    var filename: String
    var mimeType: String

    enum CodingKeys: String, CodingKey {
        case key
        case filename
        case mimeType = "mime_type"
    }
}

struct NotionStatusResponse: Codable, Sendable {
    var connected: Bool
    var workspaceID: String?
    var workspaceName: String?
    var workspaceIcon: String?

    enum CodingKeys: String, CodingKey {
        case connected
        case workspaceID = "workspace_id"
        case workspaceName = "workspace_name"
        case workspaceIcon = "workspace_icon"
    }
}

struct NotionAuthURLResponse: Codable, Sendable {
    var authURL: String

    enum CodingKeys: String, CodingKey {
        case authURL = "auth_url"
    }
}

struct NotionPageDTO: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var url: String?
    var lastEditedTime: String?

    enum CodingKeys: String, CodingKey {
        case id
        case title
        case url
        case lastEditedTime = "last_edited_time"
    }
}

struct NotionPageSearchRequest: Codable, Sendable {
    var query: String
    var pageSize: Int = 25

    enum CodingKeys: String, CodingKey {
        case query
        case pageSize = "page_size"
    }
}

struct NotionPageSearchResponse: Codable, Sendable {
    var pages: [NotionPageDTO]
}

struct NotionPageAttachmentRequest: Codable, Sendable {
    var pageID: String

    enum CodingKeys: String, CodingKey {
        case pageID = "page_id"
    }
}

/// POST /api/discussions/{id}/summary/notion request — exports a podcast's
/// generated summary into the user's Notion workspace. When `parentPageID` is
/// nil, the server creates a private root-level page.
struct NotionExportRequest: Codable, Sendable {
    var parentPageID: String?
    var docType: String?

    enum CodingKeys: String, CodingKey {
        case parentPageID = "parent_page_id"
        case docType = "doc_type"
    }
}

/// Response carrying the URL of the newly-created Notion page.
struct NotionExportResponse: Codable, Sendable {
    var url: String
    var pageID: String

    enum CodingKeys: String, CodingKey {
        case url
        case pageID = "page_id"
    }
}

struct DiscussionUIActionsResponse: Codable, Sendable {
    var id: String
    var items: [DiscussionUIActionItem]
}

struct DiscussionUIActionItem: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var loadingTitle: String?
    var systemImage: String?
    var role: String?
    var enabled: Bool
    var action: DiscussionUIAction

    enum CodingKeys: String, CodingKey {
        case id
        case title
        case loadingTitle = "loading_title"
        case systemImage = "system_image"
        case role
        case enabled
        case action
    }
}

struct DiscussionUIAction: Codable, Hashable, Sendable {
    var type: String
    var link: String
}

/// POST /api/transcribe request body — asks the server to transcribe an
/// already-uploaded voice message (gateway whisper) when the device couldn't do
/// it on-device.
struct TranscribeRequest: Codable, Sendable {
    var audioKey: String

    enum CodingKeys: String, CodingKey {
        case audioKey = "audio_key"
    }
}

struct TranscribeResponse: Codable, Sendable {
    var text: String
}

/// POST /api/discussions/{id}/sources request body — links the user added in
/// the sources sheet for the agent to research and fold into the plan.
struct AddSourcesRequest: Codable, Sendable {
    var urls: [String]
}

struct DiscussionVisibilityRequest: Codable, Sendable {
    var visibility: DiscussionVisibility
    var cover: DiscussionCover?
}

struct CoverGenerateRequest: Codable, Sendable {
    var prompt: String
}

struct CoverGenerateResponse: Codable, Sendable {
    var cover: DiscussionCover
}

/// PATCH /api/discussions/{id}/cover request body: persists a cover on a
/// discussion without changing its visibility.
struct CoverUpdateRequest: Codable, Sendable {
    var cover: DiscussionCover
}

/// POST /api/discussions/{id}/shares request body: how long the minted private
/// share link stays valid.
struct ShareCreateRequest: Codable, Sendable {
    var ttlSeconds: Int

    enum CodingKeys: String, CodingKey {
        case ttlSeconds = "ttl_seconds"
    }
}

/// A private share link returned by the share endpoints. Dates arrive as RFC3339
/// strings; `ShareLink` parses them into `Date` for display.
struct ShareLinkDTO: Codable, Sendable {
    var token: String
    var url: String
    var createdAt: String
    var expiresAt: String

    enum CodingKeys: String, CodingKey {
        case token
        case url
        case createdAt = "created_at"
        case expiresAt = "expires_at"
    }
}

/// POST /api/discussions/{id}/join request body. The token authorizes a
/// non-owner to join a private discussion via a share link.
struct DiscussionJoinRequest: Codable, Sendable {
    var token: String?
}

struct SourceSearchRequest: Codable, Sendable {
    var query: String
}

struct SourceSearchResponse: Codable, Sendable {
    var sources: [SourceDTO]
}

/// One coarse progress step streamed (SSE) while the planner drafts or revises a
/// plan — e.g. "Searching the web…", "Reading example.com", "Writing the plan".
struct PlanProgressEvent: Decodable, Sendable {
    var phase: String
    var text: String
}

/// Events surfaced by a streaming plan endpoint. The terminal `done` carries the
/// persisted discussion; `error` carries a human-readable message.
enum PlanStreamEvent: Sendable {
    case progress(PlanProgressEvent)
    case done(Discussion)
    case failed(String)
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

/// Shared numeric formatting for points labels.
enum UsageSummary {
    static func formatInt(_ value: Int) -> String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .decimal
        return formatter.string(from: NSNumber(value: value)) ?? "\(value)"
    }
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
