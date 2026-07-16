import Foundation

struct DiscussionTranslationsResponse: Codable, Sendable {
    var mainLanguage: String
    var translations: [DiscussionTranslationMeta]

    enum CodingKeys: String, CodingKey {
        case mainLanguage = "main_language"
        case translations
    }
}

struct DiscussionTranslationRequest: Codable, Sendable {
    var targetLanguage: String

    enum CodingKeys: String, CodingKey {
        case targetLanguage = "target_language"
    }
}
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
    /// Azure TTS voice ShortName override; nil/empty means the engine
    /// auto-assigns a voice at generation time.
    var voice: String?
}

struct AudioBookSpeakerDTO: Codable, Hashable, Sendable {
    var name: String
    var gender: String?
    var description: String?
    var model: String?
    var voice: String?
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
    /// Optional so older servers without album support still decode.
    var newAlbum: PrecheckNewAlbumDTO?
    /// Present only when the upload-own-audio feature is enabled for this user
    /// (global admin toggle + subscription tier).
    var uploadAudio: PrecheckUploadAudioDTO?
    /// Present only while a scheduled maintenance window is active. /api/precheck
    /// stays reachable during maintenance, so the bootstrap call carries the
    /// window here and the client can show the message proactively at launch.
    var maintenance: MaintenanceInfo?

    enum CodingKeys: String, CodingKey {
        case newDiscussion = "new_discussion"
        case newAlbum = "new_album"
        case uploadAudio = "upload_audio"
        case maintenance
    }
}

struct PrecheckNewDiscussionDTO: Codable, Sendable {
    var form: PrecheckFormDTO
}

struct PrecheckNewAlbumDTO: Codable, Sendable {
    var form: PrecheckFormDTO
}

struct PrecheckUploadAudioDTO: Codable, Sendable {
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

/// Bridges the wire form (AnyCodable trees) into what `JSONSchemaForm` needs.
extension PrecheckFormDTO {
    /// The schema re-encoded as a JSON string for `JSONSchema` parsing.
    var schemaJSONString: String? {
        guard let data = try? JSONEncoder().encode(schema) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    /// The ui_schema as a Foundation dictionary.
    var uiSchemaDictionary: [String: Any]? {
        guard let uiSchema,
              let data = try? JSONEncoder().encode(uiSchema),
              let object = try? JSONSerialization.jsonObject(with: data),
              let dictionary = object as? [String: Any]
        else {
            return nil
        }
        return dictionary
    }

    /// The initial_data decoded into form values.
    var decodedInitialData: FormData {
        guard let initialData,
              let data = try? JSONEncoder().encode(initialData),
              let decoded = try? JSONDecoder().decode(FormData.self, from: data)
        else {
            return .object(properties: [:])
        }
        return decoded
    }
}

/// Body of PATCH /api/discussions/{id}/speaker-model.
struct SpeakerModelRequest: Codable, Sendable {
    var speaker: String
    var model: String
}

/// Body of PATCH /api/discussions/{id}/speaker-voice. An empty voice clears
/// the override back to automatic assignment.
struct SpeakerVoiceRequest: Codable, Sendable {
    var speaker: String
    var voice: String
}

/// One selectable Azure TTS voice from GET /api/voices.
struct VoiceInfoDTO: Codable, Hashable, Sendable, Identifiable {
    var name: String
    var locale: String
    var localeName: String?
    var gender: String?
    var voiceType: String?
    var styles: [String]?

    var id: String { name }

    /// "en-US-AvaMultilingualNeural" → "Ava Multilingual". Falls back to the
    /// raw name when it doesn't follow Azure's {locale}-{Name}Neural shape.
    var displayName: String {
        var trimmed = name
        if trimmed.hasPrefix(locale + "-") { trimmed.removeFirst(locale.count + 1) }
        if trimmed.hasSuffix("Neural") { trimmed.removeLast("Neural".count) }
        guard !trimmed.isEmpty else { return name }
        var out = ""
        let chars = Array(trimmed)
        for i in chars.indices {
            let ch = chars[i]
            if i > 0, ch.isUppercase {
                let prev = chars[i - 1]
                let nextIsLower = i + 1 < chars.count && chars[i + 1].isLowercase
                // "AvaMultilingual" → "Ava Multilingual", "HDLatest" → "HD Latest"
                if prev.isLowercase || (prev.isUppercase && nextIsLower) {
                    out.append(" ")
                }
            }
            out.append(ch)
        }
        return out
    }

    enum CodingKeys: String, CodingKey {
        case name, locale, gender, styles
        case localeName = "locale_name"
        case voiceType = "voice_type"
    }
}

/// Body of GET /api/voices.
struct VoicesResponseDTO: Codable, Sendable {
    var voices: [VoiceInfoDTO]?
}

/// Body of POST /api/voices/preview.
struct VoicePreviewRequest: Codable, Sendable {
    var voice: String
    var language: String
}

/// Response of POST /api/voices/preview: a short-lived playback URL for the
/// cached sample MP3.
struct VoicePreviewResponseDTO: Codable, Sendable {
    var url: String
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
    /// 1-based positions (in the root plan's full chapter list) this script
    /// narrates. Set on derived chapter-batch scripts; nil/empty means the
    /// script narrates all of its chapters.
    var audioBookChapterIndices: [Int]?
    var background: String?
    var surface: String?
    var sources: [SourceDTO]?
    /// Uploaded-audio plans: the durable storage key of the user's original
    /// upload, the transcribed duration, and the sentence-level transcript.
    var uploadedAudioKey: String?
    var uploadedAudioDurationMs: Int64?
    var uploadedAudioSpeakers: [String]?
    var transcriptSegments: [TranscriptSegmentDTO]?

    enum CodingKeys: String, CodingKey {
        case title, type, language, channel
        case total_minutes, segment_max_seconds, tts_provider, resolution, storage
        case host, discussants, commander, background, surface, sources
        case audioBookHost = "audio_book_host"
        case audioBookStyle = "audio_book_style"
        case audioBookSpeakers = "audio_book_speakers"
        case audioBookChapters = "audio_book_chapters"
        case audioBookChapterIndices = "audio_book_chapter_indices"
        case uploadedAudioKey = "uploaded_audio_key"
        case uploadedAudioDurationMs = "uploaded_audio_duration_ms"
        case uploadedAudioSpeakers = "uploaded_audio_speakers"
        case transcriptSegments = "transcript_segments"
    }
}

/// One sentence-level piece of an uploaded-audio transcript.
struct TranscriptSegmentDTO: Codable, Hashable, Sendable {
    var speaker: String
    var offsetMs: Int64
    var durationMs: Int64
    var text: String

    enum CodingKeys: String, CodingKey {
        case speaker
        case offsetMs = "offset_ms"
        case durationMs = "duration_ms"
        case text
    }
}

struct UploadedAudioSpeakerAddRequest: Codable, Sendable {
    var name: String
}

struct UploadedAudioSpeakerRenameRequest: Codable, Sendable {
    var from: String
    var to: String
}

/// A user-uploaded reference file. Documents carry markdown parsed by
/// markitdown; images carry a URL so the engine can pass them to the model.
/// `key` is the storage key from the upload response; the server uses it to
/// re-sign a fresh image URL whenever the planning conversation is replayed.
struct Attachment: Codable, Hashable, Sendable {
    var filename: String
    var markdown: String?
    var url: String?
    var mimeType: String?
    var key: String?

    enum CodingKeys: String, CodingKey {
        case filename
        case markdown
        case url
        case mimeType = "mime_type"
        case key
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

/// PATCH /api/discussions/{id} request body.
struct DiscussionRenameRequest: Codable, Sendable {
    var title: String
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
    /// Upload kind ("podcast-audio" for the upload-own-audio flow); nil keeps
    /// the default reference-document rules.
    var kind: String?

    enum CodingKeys: String, CodingKey {
        case filename
        case mimeType = "mime_type"
        case kind
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
    var kind: String?

    enum CodingKeys: String, CodingKey {
        case key
        case filename
        case mimeType = "mime_type"
        case kind
    }
}

/// POST /api/discussions/upload-audio request body: the upload-audio form
/// values posted verbatim (same contract as DiscussionCreateRequest).
struct UploadAudioCreateRequest: Codable, Sendable {
    var form: FormData
}

/// Short-lived playback URL for the original audio behind an uploaded-audio
/// discussion. The durable storage key is never exposed to the client.
struct UploadedAudioPlaybackResponse: Codable, Sendable {
    var url: URL
}

struct TranscriptSegmentUpdate: Hashable, Sendable {
    var index: Int
    var segment: TranscriptSegmentDTO
}

struct TranscriptSegmentBatchUpdateRequest: Codable, Sendable {
    var updates: [TranscriptSegmentBatchUpdateItem]

    init(updates: [TranscriptSegmentUpdate]) {
        self.updates = updates.map(TranscriptSegmentBatchUpdateItem.init)
    }
}

struct TranscriptSegmentBatchUpdateItem: Codable, Sendable {
    var index: Int
    var speaker: String
    var offsetMs: Int64
    var durationMs: Int64
    var text: String

    init(update: TranscriptSegmentUpdate) {
        index = update.index
        speaker = update.segment.speaker
        offsetMs = update.segment.offsetMs
        durationMs = update.segment.durationMs
        text = update.segment.text
    }

    enum CodingKeys: String, CodingKey {
        case index, speaker
        case offsetMs = "offset_ms"
        case durationMs = "duration_ms"
        case text
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
    var language: String? = nil

    enum CodingKeys: String, CodingKey {
        case parentPageID = "parent_page_id"
        case docType = "doc_type"
        case language
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
    var toolbars: [DiscussionUIActionItem]

    enum CodingKeys: String, CodingKey {
        case id
        case items
        case toolbars
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        items = try container.decodeIfPresent([DiscussionUIActionItem].self, forKey: .items) ?? []
        toolbars = try container.decodeIfPresent([DiscussionUIActionItem].self, forKey: .toolbars) ?? []
    }

    init(id: String, items: [DiscussionUIActionItem] = [], toolbars: [DiscussionUIActionItem] = []) {
        self.id = id
        self.items = items
        self.toolbars = toolbars
    }
}

struct DiscussionUIActionItem: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var loadingTitle: String?
    var systemImage: String?
    var role: String?
    var placement: String?
    var enabled: Bool
    var action: DiscussionUIAction
    var children: [DiscussionUIActionItem]

    enum CodingKeys: String, CodingKey {
        case id
        case title
        case loadingTitle = "loading_title"
        case systemImage = "system_image"
        case role
        case placement
        case enabled
        case action
        case children
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        title = try container.decode(String.self, forKey: .title)
        loadingTitle = try container.decodeIfPresent(String.self, forKey: .loadingTitle)
        systemImage = try container.decodeIfPresent(String.self, forKey: .systemImage)
        role = try container.decodeIfPresent(String.self, forKey: .role)
        placement = try container.decodeIfPresent(String.self, forKey: .placement)
        enabled = try container.decode(Bool.self, forKey: .enabled)
        action = try container.decode(DiscussionUIAction.self, forKey: .action)
        children = try container.decodeIfPresent([DiscussionUIActionItem].self, forKey: .children) ?? []
    }

    var isDivider: Bool { action.type == "divider" }
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
