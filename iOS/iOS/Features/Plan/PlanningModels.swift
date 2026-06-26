import Foundation

// MARK: - AnyCodable

/// Lightweight JSON value wrapper used to carry the agent's free-form tool
/// inputs/results and question answers. Ported from linda-assistant's
/// AssistantCore (debate-bot has no such type).
enum AnyCodable: Codable, Sendable, Hashable {
    case null
    case bool(Bool)
    case int(Int)
    case double(Double)
    case string(String)
    case array([AnyCodable])
    case object([String: AnyCodable])

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let bool = try? container.decode(Bool.self) {
            self = .bool(bool)
        } else if let int = try? container.decode(Int.self) {
            self = .int(int)
        } else if let double = try? container.decode(Double.self) {
            self = .double(double)
        } else if let string = try? container.decode(String.self) {
            self = .string(string)
        } else if let array = try? container.decode([AnyCodable].self) {
            self = .array(array)
        } else if let dict = try? container.decode([String: AnyCodable].self) {
            self = .object(dict)
        } else {
            self = .null
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .null: try container.encodeNil()
        case let .bool(v): try container.encode(v)
        case let .int(v): try container.encode(v)
        case let .double(v): try container.encode(v)
        case let .string(v): try container.encode(v)
        case let .array(v): try container.encode(v)
        case let .object(v): try container.encode(v)
        }
    }

    /// A compact, human-readable rendering for the tool-call detail sheet.
    var prettyString: String {
        switch self {
        case .null: return "null"
        case let .bool(v): return "\(v)"
        case let .int(v): return "\(v)"
        case let .double(v): return "\(v)"
        case let .string(v): return v
        case .array, .object:
            if let data = try? JSONEncoder.prettySorted.encode(self),
               let s = String(data: data, encoding: .utf8) {
                return s
            }
            return ""
        }
    }
}

extension JSONEncoder {
    static let prettySorted: JSONEncoder = {
        let e = JSONEncoder()
        e.outputFormatting = [.prettyPrinted, .sortedKeys]
        return e
    }()
}

// MARK: - Question models (ported from linda-assistant)

struct QuestionOptionItem: Codable, Sendable, Hashable {
    let title: String
    let description: String?

    init(title: String, description: String?) {
        self.title = title
        self.description = description
    }
}

struct QuestionItem: Codable, Sendable, Hashable {
    let title: String
    let description: String?
    let type: String // "boolean" | "single_choice" | "multiple_choice" | "fill_in_blank"
    let options: [QuestionOptionItem]?

    init(title: String, description: String?, type: String, options: [QuestionOptionItem]?) {
        self.title = title
        self.description = description
        self.type = type
        self.options = options
    }
}

struct QuestionPayload: Codable, Sendable, Identifiable, Hashable {
    var id: String { questionId }
    let questionId: String
    let toolCallId: String
    let toolName: String
    let questions: [QuestionItem]

    init(questionId: String, toolCallId: String, toolName: String, questions: [QuestionItem]) {
        self.questionId = questionId
        self.toolCallId = toolCallId
        self.toolName = toolName
        self.questions = questions
    }
}

// MARK: - Conversation parts (mirror of the backend PlanningPart JSON)

/// One display item in the conversation: a text bubble or a tool card. The
/// backend flattens assistant tool_calls + their results into one card each, so
/// the client just renders this ordered list (linda-assistant conversation
/// design). Snake-cased keys mirror the Go JSON tags.
struct PlanningPart: Codable, Sendable, Hashable, Identifiable {
    var kind: String          // "text" | "tool"
    var id: String
    var role: String? = nil   // text parts: "user" | "assistant"
    var text: String? = nil
    var attachments: [Attachment]? = nil
    var references: [PodcastReference]? = nil

    var toolCallID: String? = nil
    var toolName: String? = nil
    var status: String? = nil // running | completed | failed | pending_question | rejected
    var input: AnyCodable? = nil
    var inputText: String? = nil
    var resultText: String? = nil

    var script: ScriptDTO? = nil
    var sources: [SourceDTO]? = nil
    var markdown: String? = nil

    var questionID: String? = nil
    var questions: [QuestionItem]? = nil
    var answers: AnyCodable? = nil

    enum CodingKeys: String, CodingKey {
        case kind, id, role, text, attachments, references
        case toolCallID = "tool_call_id"
        case toolName = "tool_name"
        case status, input
        case inputText = "input_text"
        case resultText = "result_text"
        case script, sources, markdown
        case questionID = "question_id"
        case questions, answers
    }
}

extension PlanningPart {
    var displayText: String {
        guard role == "user" else {
            return text ?? ""
        }
        return PlanningPart.userDisplayText(text ?? "")
    }

    var isPlanCard: Bool {
        toolName == "show_plan" && script != nil
    }

    var isTransientRunningTool: Bool {
        kind == "tool" && status == "running" && !isPlanCard && !isQuestionCard
    }

    var isQuestionCard: Bool {
        questions != nil && (status == "pending_question" || status == "completed" || status == "rejected")
    }

    /// Reconstructs a QuestionPayload from a question card so it can drive the
    /// bottom sheet.
    func questionPayload() -> QuestionPayload? {
        guard let questions, let questionID, let toolCallID else { return nil }
        return QuestionPayload(questionId: questionID, toolCallId: toolCallID,
                               toolName: toolName ?? "ask_question", questions: questions)
    }

    private static func userDisplayText(_ text: String) -> String {
        var trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if let range = trimmed.range(of: "\n\nCurrent plan settings:") {
            return String(trimmed[..<range.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if let range = trimmed.range(of: "\n\nThe user uploaded these reference documents;") {
            trimmed = String(trimmed[..<range.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if let range = trimmed.range(of: "\n\nReferenced podcast context:") {
            trimmed = String(trimmed[..<range.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        guard trimmed.contains("Plan settings:") else {
            return trimmed
        }
        for line in trimmed.components(separatedBy: .newlines) {
            let line = line.trimmingCharacters(in: .whitespacesAndNewlines)
            if line.hasPrefix("Topic:") {
                return String(line.dropFirst("Topic:".count)).trimmingCharacters(in: .whitespacesAndNewlines)
            }
        }
        return trimmed
    }
}

struct PlanningConversationMeta: Codable, Sendable, Hashable {
    var id: String?
    var status: String?
    var pointsCharged: Int?

    enum CodingKeys: String, CodingKey {
        case id, status
        case pointsCharged = "points_charged"
    }
}

struct PlanningConversationView: Codable, Sendable {
    var conversation: PlanningConversationMeta?
    var parts: [PlanningPart]
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

struct PlanningDonePayload: Codable, Sendable {
    var discussion: Discussion?
    var conversation: PlanningConversationView
}

// MARK: - SSE payloads (richer than PlanStreamEvent)

struct PlanningTextDeltaPayload: Codable, Sendable {
    let text: String
}

struct PlanningToolInputStartPayload: Codable, Sendable {
    let toolCallId: String?
    let toolName: String
}

struct PlanningToolInputDeltaPayload: Codable, Sendable {
    let toolCallId: String?
    let toolName: String?
    let delta: String
}

struct PlanningToolCallPayload: Codable, Sendable {
    let toolCallId: String
    let toolName: String
    let input: AnyCodable?
}

struct PlanningToolResultPayload: Codable, Sendable {
    let toolCallId: String
    let toolName: String
    let output: AnyCodable?
    let isError: Bool?
}

struct PlanningPlanPayload: Codable, Sendable {
    let toolCallId: String
    let toolName: String?
    let script: ScriptDTO?
    let sources: [SourceDTO]?
    let markdown: String?
}

/// Events streamed by the conversational planning endpoints.
enum PlanningStreamEvent: Sendable {
    case textDelta(String)
    case toolInputStart(PlanningToolInputStartPayload)
    case toolInputDelta(PlanningToolInputDeltaPayload)
    case toolCall(PlanningToolCallPayload)
    case toolResult(PlanningToolResultPayload)
    case plan(PlanningPlanPayload)
    case question(QuestionPayload)
    case progress(PlanProgressEvent)
    case done(PlanningDonePayload)
    case failed(String)
}

struct PlanLanguageOption: Identifiable, Hashable {
    let id: String
    let label: String

    static func initialCode(_ code: String) -> String {
        let trimmed = code.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? "en-US" : trimmed
    }

    static func pickerOptions(selected: String, options: [PlanLanguageOption]) -> [PlanLanguageOption] {
        let selected = initialCode(selected)
        guard !options.contains(where: { $0.id == selected }) else { return options }
        return [PlanLanguageOption(id: selected, label: displayName(for: selected))] + options
    }

    static func label(for code: String, options: [PlanLanguageOption]) -> String {
        let code = initialCode(code)
        return options.first(where: { $0.id == code })?.label ?? displayName(for: code)
    }

    private static func displayName(for code: String) -> String {
        Locale.current.localizedString(forIdentifier: code) ?? code
    }
}

extension PrecheckFormDTO {
    var languageOptions: [PlanLanguageOption] {
        guard let languageSchema = languageSchema else {
            return []
        }
        if case let .array(values)? = languageSchema["x-options"] {
            return values.compactMap { value in
                guard case let .object(option) = value,
                      let id = precheckString(option["id"]) else {
                    return nil
                }
                return PlanLanguageOption(id: id, label: precheckString(option["label"]) ?? id)
            }
        }
        if case let .array(values)? = languageSchema["enum"] {
            return values.compactMap { value in
                guard case let .string(id) = value else { return nil }
                return PlanLanguageOption(id: id, label: PlanLanguageOption.label(for: id, options: []))
            }
        }
        return []
    }

    private var languageSchema: [String: AnyCodable]? {
        guard case let .object(properties)? = schema["properties"] else {
            return nil
        }
        if case let .object(languageSchema)? = properties["language"] {
            return languageSchema
        }
        guard case let .object(settingsSchema)? = properties["settings"],
              case let .object(settingsProperties)? = settingsSchema["properties"],
              case let .object(languageSchema)? = settingsProperties["language"] else {
            return nil
        }
        return languageSchema
    }
}

private func precheckString(_ value: AnyCodable?) -> String? {
    guard case let .string(string)? = value else { return nil }
    return string
}
