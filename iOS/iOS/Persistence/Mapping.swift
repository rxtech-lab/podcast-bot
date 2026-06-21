import Foundation
import SwiftData

/// Bridges the engine's ScriptDTO/PlanResponse and the SwiftData model graph.
enum Mapping {
    /// Writes a plan response into a Discussion (people, sources, background).
    @MainActor
    static func apply(_ plan: PlanResponse, to discussion: Discussion, in context: ModelContext) {
        let script = plan.script
        discussion.title = script.title
        discussion.background = script.background ?? ""
        discussion.language = script.language
        discussion.commanderName = script.commander?.name ?? "Commander"
        discussion.commanderModel = script.commander?.model ?? ""

        // Replace people.
        for p in discussion.people ?? [] { context.delete(p) }
        var people: [Person] = []
        if let host = script.host, !host.name.isEmpty {
            people.append(Person(ordinal: 0, name: host.name, aspect: "Moderator",
                                 model: host.model ?? "", isHost: true))
        }
        for (i, d) in (script.discussants ?? []).enumerated() {
            people.append(Person(ordinal: i + 1, name: d.name, aspect: d.aspect ?? "",
                                 model: d.model ?? ""))
        }
        discussion.people = people

        // Replace sources (prefer top-level, fall back to script.sources).
        for s in discussion.sources ?? [] { context.delete(s) }
        let dtoSources = plan.sources ?? script.sources ?? []
        discussion.sources = dtoSources.enumerated().map { i, s in
            SourceRef(ordinal: i, title: s.title, urlString: s.url, snippet: s.snippet ?? "")
        }

        discussion.updatedAt = Date()
    }

    /// Builds the ScriptDTO to submit/improve from a Discussion's current state.
    @MainActor
    static func scriptDTO(from discussion: Discussion) -> ScriptDTO {
        let host = discussion.sortedPeople.first(where: { $0.isHost })
        let discussants = discussion.sortedPeople.filter { !$0.isHost }.map {
            AgentDTO(name: $0.name, model: $0.model.isEmpty ? nil : $0.model,
                     aspect: $0.aspect.isEmpty ? nil : $0.aspect)
        }
        let sources = discussion.sortedSources.map {
            SourceDTO(title: $0.title, url: $0.urlString, snippet: $0.snippet.isEmpty ? nil : $0.snippet)
        }
        let commanderModel = discussion.commanderModel.isEmpty
            ? (host?.model.isEmpty == false ? host?.model : discussants.first?.model)
            : discussion.commanderModel
        return ScriptDTO(
            title: discussion.title,
            type: "discussion",
            language: discussion.language,
            channel: "default",
            total_minutes: 30,
            segment_max_seconds: 60,
            tts_provider: "azure",
            resolution: "1080p",
            storage: "plaintext",
            host: host.map { AgentDTO(name: $0.name, model: $0.model.isEmpty ? nil : $0.model, aspect: nil) },
            discussants: discussants,
            commander: commanderModel.map {
                AgentDTO(name: discussion.commanderName.isEmpty ? "Commander" : discussion.commanderName,
                         model: $0,
                         aspect: nil)
            },
            background: discussion.background,
            sources: sources.isEmpty ? nil : sources
        )
    }
}
