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
    var markdown: String?
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

/// A user-uploaded reference file. Documents carry markdown parsed by
/// markitdown; images carry a URL so the engine can pass them to the model.
struct Attachment: Codable, Sendable {
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

/// POST /api/discussions request body: creates an empty placeholder discussion
/// (status "planning") so the client gets an id before streaming the plan.
struct DiscussionCreateRequest: Codable, Sendable {
    var topic: String
    var language: String
}

/// POST /api/plan request body.
struct PlanRequest: Codable, Sendable {
    var type: String = "discussion"
    var topic: String
    var language: String = "en-US"
    var discussants: Int = 3
    var research: Bool = true
    var attachments: [Attachment]?
}

/// POST /api/uploads response body: the original filename, either parsed
/// markdown or a direct image URL, and the content type.
struct UploadResponse: Codable, Sendable {
    var filename: String
    var markdown: String?
    var url: String
    var mimeType: String?

    enum CodingKeys: String, CodingKey {
        case filename
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

/// POST /api/discussions/{id}/sources request body — links the user added in
/// the sources sheet for the agent to research and fold into the plan.
struct AddSourcesRequest: Codable, Sendable {
    var urls: [String]
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
    var tts_cost_usd: Double?
    var music_cost_usd: Double?
    var logs: [JobLogDTO]?

    var isDone: Bool { status == "done" }
    var isError: Bool { status == "error" }

    /// Structured cost/token breakdown for the "Generation summary" card.
    var usageSummary: UsageSummary? {
        guard let total = total_tokens, total > 0 else { return nil }
        return UsageSummary(
            totalTokens: total,
            promptTokens: prompt_tokens ?? 0,
            completionTokens: completion_tokens ?? 0,
            llmCostUSD: llm_cost_known == true ? llm_cost_usd : nil,
            ttsCostUSD: tts_cost_usd ?? 0,
            musicCostUSD: music_cost_usd ?? 0
        )
    }

    /// Single-line fallback (now-playing / status text). Prefers a server "usage" log line.
    var usageSummaryText: String? {
        if let log = logs?.last(where: { $0.kind == "usage" }),
           !log.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return log.text
        }
        return usageSummary?.singleLineText
    }
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

/// Itemized token + cost breakdown rendered by the "Generation summary" card.
/// Costs are sub-cent, so values are formatted with enough precision to stay non-zero.
struct UsageSummary: Equatable, Sendable {
    var totalTokens: Int
    var promptTokens: Int
    var completionTokens: Int
    /// `nil` when the LLM price for the model is unknown (cost can't be totalled).
    var llmCostUSD: Double?
    var ttsCostUSD: Double
    var musicCostUSD: Double

    var costKnown: Bool { llmCostUSD != nil }

    /// LLM + Azure TTS + Lyria music. `nil` when the LLM price is unknown.
    var totalCostUSD: Double? {
        guard let llm = llmCostUSD else { return nil }
        return llm + ttsCostUSD + musicCostUSD
    }

    /// Collapsed one-line form for now-playing info and status text.
    var singleLineText: String {
        var text = "Token usage: \(Self.formatInt(totalTokens)) total "
            + "(\(Self.formatInt(promptTokens)) input, \(Self.formatInt(completionTokens)) output)"
        if let total = totalCostUSD {
            text += " · total cost \(Self.formatUSD(total))"
        } else {
            text += " · total cost unavailable"
        }
        return text
    }

    static func formatInt(_ value: Int) -> String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .decimal
        return formatter.string(from: NSNumber(value: value)) ?? "\(value)"
    }

    /// Sub-cent costs need more than 2 decimals; pad small values so they don't read as "$0.00".
    static func formatUSD(_ value: Double) -> String {
        if value > 0, value < 0.01 {
            return String(format: "$%.6f", value)
        }
        return String(format: "$%.4f", value)
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
