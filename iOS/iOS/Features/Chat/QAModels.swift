import Foundation

// MARK: - Q&A conversation scope

/// Which conversation a QAConversationView drives: one podcast's Q&A thread,
/// or the user's single global library chat.
enum QAScope: Hashable {
    case podcast(Discussion)
    case global

    var discussionID: String? {
        if case let .podcast(d) = self { return d.id }
        return nil
    }
}

// MARK: - Cards (dedicated tool views)

/// Structured payload behind Q&A presentation tools. The singular podcast
/// field remains for histories created before batch podcast cards.
/// tool calls. Mirrors the Go qa.Card JSON.
struct QACard: Codable, Sendable, Hashable {
    var kind: String // podcast(s) | highlights | transcript | sources | mindmap | ppt | document
    var podcast: QAPodcastCard?
    var podcasts: [QAPodcastCard]?
    var highlights: [QAPodcastHighlightGroup]?
    var transcript: QATranscriptCard?
    var sources: [QASourceCard]?
    var document: QADocumentCard?
    var agentDocument: QAAgentDocumentCard?

    enum CodingKeys: String, CodingKey {
        case kind, podcast, podcasts, highlights, transcript, sources, document
        case agentDocument = "agent_document"
    }
}

struct QAAgentDocumentCard: Codable, Sendable, Hashable, Identifiable {
    var id: String
    var title: String
    var discussionID: String?
    var podcastTitle: String?

    enum CodingKeys: String, CodingKey {
        case id, title
        case discussionID = "discussion_id"
        case podcastTitle = "podcast_title"
    }
}

struct QADocumentCard: Codable, Sendable, Hashable, Identifiable {
    var discussionID: String
    var title: String

    var id: String { discussionID }

    enum CodingKeys: String, CodingKey {
        case discussionID = "discussion_id"
        case title
    }
}

struct QAPodcastCard: Codable, Sendable, Hashable {
    var id: String
    var title: String
    var topic: String?
    var status: String?
    var language: String?
    var durationSeconds: Double?
    var cover: DiscussionCover?

    enum CodingKeys: String, CodingKey {
        case id, title, topic, status, language, cover
        case durationSeconds = "duration_seconds"
    }
}

struct QAPodcastHighlightGroup: Codable, Sendable, Hashable, Identifiable {
    var podcast: QAPodcastCard
    var lines: [QATranscriptLine]

    var id: String { podcast.id }
}

struct QATranscriptCard: Codable, Sendable, Hashable {
    var discussionID: String
    var title: String?
    var startMS: Int64
    var endMS: Int64
    var lines: [QATranscriptLine]

    enum CodingKeys: String, CodingKey {
        case discussionID = "discussion_id"
        case title
        case startMS = "start_ms"
        case endMS = "end_ms"
        case lines
    }
}

struct QATranscriptLine: Codable, Sendable, Hashable {
    var speaker: String
    var text: String
    var startMS: Int64

    enum CodingKeys: String, CodingKey {
        case speaker, text
        case startMS = "start_ms"
    }
}

struct QASourceCard: Codable, Sendable, Hashable {
    var title: String
    var url: String
    var snippet: String?
}

// MARK: - Conversation parts (mirror of the backend QAPart JSON)

/// One display item in a Q&A conversation: a text bubble, a compaction summary
/// chip, or a tool card (generic or one of the dedicated card kinds).
struct QAPart: Codable, Sendable, Hashable, Identifiable {
    var kind: String // "text" | "tool" | "summary"
    var id: String
    var role: String? = nil
    var text: String? = nil

    var toolCallID: String? = nil
    var toolName: String? = nil
    var status: String? = nil // running | completed | failed
    var input: AnyCodable? = nil
    var inputText: String? = nil
    var resultText: String? = nil
    var card: QACard? = nil

    enum CodingKeys: String, CodingKey {
        case kind, id, role, text
        case toolCallID = "tool_call_id"
        case toolName = "tool_name"
        case status, input
        case resultText = "result_text"
        case card
    }
}

extension QAPart {
    var isCard: Bool { card != nil }

    var isTransientRunningTool: Bool {
        kind == "tool" && status == "running" && card == nil
    }

    /// Bridges a generic Q&A tool part onto the plan chat's reusable tool card
    /// + detail sheet, which take a PlanningPart.
    var asPlanningPart: PlanningPart {
        PlanningPart(kind: "tool",
                     id: id,
                     toolCallID: toolCallID,
                     toolName: toolName,
                     status: status,
                     input: input,
                     inputText: inputText,
                     resultText: resultText)
    }
}

struct QAConversationMeta: Codable, Sendable, Hashable {
    var id: String?
    var status: String?
    var pointsCharged: Int?

    enum CodingKeys: String, CodingKey {
        case id, status
        case pointsCharged = "points_charged"
    }
}

struct QAConversationViewDTO: Codable, Sendable {
    var conversation: QAConversationMeta?
    var parts: [QAPart]
    var needsRun: Bool?
    var isRunning: Bool?
    var activeStreamID: String?

    enum CodingKeys: String, CodingKey {
        case conversation, parts
        case needsRun = "needs_run"
        case isRunning = "is_running"
        case activeStreamID = "active_stream_id"
    }
}

struct QADonePayload: Codable, Sendable {
    var conversation: QAConversationViewDTO
}

/// SSE payload for the dedicated card frames (event name = card kind).
struct QACardPayload: Codable, Sendable {
    let toolCallId: String
    let toolName: String?
    let card: QACard
}

/// Events streamed by the Q&A endpoints.
enum QAStreamEvent: Sendable {
    case textDelta(String)
    case toolInputStart(PlanningToolInputStartPayload)
    case toolInputDelta(PlanningToolInputDeltaPayload)
    case toolCall(PlanningToolCallPayload)
    case toolResult(PlanningToolResultPayload)
    case card(QACardPayload)
    case progress(PlanProgressEvent)
    case done(QADonePayload)
    case failed(String)
}

// MARK: - Rows

struct QARow: Identifiable, MessageListItem {
    enum Content {
        case part(QAPart)
        case loading
    }

    let id: String
    let content: Content

    var isUserMessage: Bool {
        guard case let .part(part) = content else { return false }
        return part.role == "user"
    }

    var isMessageListAccessory: Bool {
        if case .loading = content { return true }
        return false
    }
}

// MARK: - Semantic search DTOs

struct SemanticMatch: Codable, Sendable, Hashable, Identifiable {
    var kind: String // "transcript" | "source"
    var text: String
    var similarity: Double
    var startMS: Int64?
    var endMS: Int64?
    var speakers: [String]?
    var sourceURL: String?
    var sourceTitle: String?

    var id: String { "\(kind)-\(startMS ?? 0)-\(text.hashValue)" }

    enum CodingKeys: String, CodingKey {
        case kind, text, similarity, speakers
        case startMS = "start_ms"
        case endMS = "end_ms"
        case sourceURL = "source_url"
        case sourceTitle = "source_title"
    }
}

struct SemanticSearchGroup: Codable, Sendable, Identifiable {
    var discussion: Discussion
    var matches: [SemanticMatch]

    var id: String { discussion.id }
}

struct SemanticSearchResponse: Codable, Sendable {
    var enabled: Bool
    var results: [SemanticSearchGroup]
}

struct DiscussionSemanticSearchResponse: Codable, Sendable {
    var enabled: Bool
    var matches: [SemanticMatch]
}

struct SemanticSearchRequest: Codable, Sendable {
    var query: String
    var limit: Int?
}
