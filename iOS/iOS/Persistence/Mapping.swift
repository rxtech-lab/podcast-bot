import Foundation

/// Bridges server discussion snapshots and the engine ScriptDTO shape.
enum Mapping {
    static func scriptDTO(from discussion: Discussion) -> ScriptDTO {
        if let script = discussion.script {
            return script
        }
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
            host: nil,
            discussants: nil,
            commander: nil,
            background: nil,
            sources: nil
        )
    }
}
