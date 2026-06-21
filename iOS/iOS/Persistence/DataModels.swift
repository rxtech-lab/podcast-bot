import Foundation

/// Lifecycle of a server-owned discussion.
enum DiscussionStatus: String, Codable, Sendable {
    case planning
    case generating
    case ready
    case failed
}

/// A planned + generated audio discussion. Durable storage now lives on the
/// engine side; the app keeps only this in-memory snapshot from the API.
struct Discussion: Identifiable, Codable, Hashable, Sendable {
    var id: String
    var topic: String
    var title: String
    var status: DiscussionStatus
    var language: String
    var jobID: String?
    var downloadURLString: String?
    var durationSeconds: Double?
    var promptTokens: Int?
    var completionTokens: Int?
    var totalTokens: Int?
    var llmCostUSD: Double?
    var llmCostKnown: Bool?
    var script: ScriptDTO?
    var markdown: String?
    var sources: [SourceDTO]?
    var researched: Bool?
    var lines: [DiscussionLineDTO]?
    var createdAt: String?
    var updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case id
        case topic
        case title
        case status
        case language
        case jobID = "job_id"
        case downloadURLString = "download_url"
        case durationSeconds = "duration_seconds"
        case promptTokens = "prompt_tokens"
        case completionTokens = "completion_tokens"
        case totalTokens = "total_tokens"
        case llmCostUSD = "llm_cost_usd"
        case llmCostKnown = "llm_cost_known"
        case script
        case markdown
        case sources
        case researched
        case lines
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    var displayTitle: String {
        if !title.isEmpty { return title }
        if let scriptTitle = script?.title, !scriptTitle.isEmpty { return scriptTitle }
        return topic
    }

    var sortedPeople: [PlanPersonSnapshot] {
        var people: [PlanPersonSnapshot] = []
        if let host = script?.host, !host.name.isEmpty {
            people.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        people.append(contentsOf: (script?.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        return people
    }

    var sortedSources: [PlanSourceSnapshot] {
        (sources ?? script?.sources ?? []).map {
            PlanSourceSnapshot(title: $0.title, urlString: $0.url, snippet: $0.snippet ?? "")
        }
    }

    var sortedLines: [DiscussionLineDTO] { lines ?? [] }

    var usageSummaryText: String? {
        guard let total = totalTokens, total > 0 else { return nil }
        let prompt = promptTokens ?? 0
        let completion = completionTokens ?? 0
        var text = "Token usage: \(Self.format(total)) total (\(Self.format(prompt)) input, \(Self.format(completion)) output)"
        if llmCostKnown == true, let cost = llmCostUSD {
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

struct DiscussionLineDTO: Codable, Hashable, Sendable {
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
