import Foundation

/// DTOs mirroring the debate-bot engine JSON. Field names match the Go structs'
/// json tags (snake_case).

struct EmptyRequest: Codable, Sendable {}

struct AgentDTO: Codable, Hashable, Sendable {
    var name: String
    var model: String?
    var aspect: String?
}

struct SourceDTO: Codable, Hashable, Sendable {
    var title: String
    var url: String
    var snippet: String?
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
    var background: String?
    var sources: [SourceDTO]?
}

/// POST /api/plan request body.
struct PlanRequest: Codable, Sendable {
    var type: String = "discussion"
    var topic: String
    var language: String = "en-US"
    var discussants: Int = 3
    var research: Bool = true
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
}

struct DiscussionImproveRequest: Codable, Sendable {
    var instruction: String
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

    enum CodingKeys: String, CodingKey {
        case text
        case username
        case discussionID = "discussion_id"
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
    var logs: [JobLogDTO]?

    var isDone: Bool { status == "done" }
    var isError: Bool { status == "error" }

    var usageSummaryText: String? {
        if let log = logs?.last(where: { $0.kind == "usage" }),
           !log.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return log.text
        }
        guard let total = total_tokens, total > 0 else { return nil }
        let prompt = prompt_tokens ?? 0
        let completion = completion_tokens ?? 0
        var text = "Token usage: \(Self.format(total)) total (\(Self.format(prompt)) input, \(Self.format(completion)) output)"
        if llm_cost_known == true, let cost = llm_cost_usd {
            text += String(format: " · total cost $%.6f", cost)
        } else {
            text += " · total cost unavailable"
        }
        return text
    }

    private static func format(_ value: Int) -> String {
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
    var agent: String?
    var activity: String?
    var detail: String?
    var phase: String?
    var label: String?
    var elapsed_ms: Int?
    var remaining_ms: Int?
}

struct DiscussionLineRequest: Codable, Sendable {
    var speaker: String
    var role: String
    var side: String?
    var text: String
    var startMS: Int?
    var isUser: Bool

    enum CodingKeys: String, CodingKey {
        case speaker
        case role
        case side
        case text
        case startMS = "start_ms"
        case isUser = "is_user"
    }
}
