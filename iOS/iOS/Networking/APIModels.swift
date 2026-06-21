import Foundation

/// DTOs mirroring the debate-bot engine JSON. Field names match the Go structs'
/// json tags (snake_case).

struct AgentDTO: Codable, Hashable {
    var name: String
    var model: String?
    var aspect: String?
}

struct SourceDTO: Codable, Hashable {
    var title: String
    var url: String
    var snippet: String?
}

/// The discussion script (config.DebateTopic). Only the discussion-relevant
/// fields are modeled; the engine fills defaults for anything omitted.
struct ScriptDTO: Codable, Hashable {
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
    var background: String?
    var sources: [SourceDTO]?
}

/// POST /api/plan request body.
struct PlanRequest: Codable {
    var type: String = "discussion"
    var topic: String
    var language: String = "en-US"
    var discussants: Int = 3
    var research: Bool = true
}

/// POST /api/plan and /api/plan/improve response body.
struct PlanResponse: Codable {
    var script: ScriptDTO
    var markdown: String?
    var sources: [SourceDTO]?
    var researched: Bool?
}

/// POST /api/plan/improve request body.
struct PlanImproveRequest: Codable {
    var previousScript: ScriptDTO
    var instruction: String
}

/// videoConfig portion of POST /api/jobs/json (audio-only feed).
struct VideoConfigDTO: Codable {
    var audio_only: Bool = true
    var soft_subs: Bool = true
    var burn_subs: Bool = false
    var resolution: String = "1080p"
}

/// POST /api/jobs/json request body.
struct JobSubmitRequest: Codable {
    var script: ScriptDTO
    var videoConfig: VideoConfigDTO = VideoConfigDTO()
}

/// POST /api/jobs/json response.
struct JobSubmitResponse: Codable {
    var id: String
}

/// GET /api/jobs/{id} response.
struct JobStatusDTO: Codable {
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

    var isDone: Bool { status == "done" }
    var isError: Bool { status == "error" }
}

/// One persisted transcript line returned by GET /api/jobs/{id}/transcript.
struct TranscriptDTO: Codable, Hashable {
    var speaker: String
    var role: String
    var side: String?
    var text: String
    var at: String?
}

/// One event from GET /api/jobs/{id}/ws: `{ "event": ..., "data": {...} }`.
struct JobEventEnvelope: Decodable {
    var event: String
    var data: JobEventData?
}

struct JobEventData: Decodable {
    var channel_id: String?
    var speaker: String?
    var role: String?
    var text: String?
    var done: Bool?
    var agent: String?
    var activity: String?
    var detail: String?
    var phase: String?
    var label: String?
    var elapsed_ms: Int?
    var remaining_ms: Int?
}
