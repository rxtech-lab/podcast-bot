import Foundation
import SwiftData

/// Lifecycle of a discussion: plan it, generate the podcast, then play it back.
enum DiscussionStatus: String, Codable, Sendable {
    case planning   // plan drafted/edited, not yet generating
    case generating // a job is producing the audio
    case ready      // audio + transcript available
    case failed
}

/// A planned + generated audio discussion. This is the app's source of truth,
/// synced across the user's devices via SwiftData + CloudKit. The backend job
/// is transient; everything durable lives here.
///
/// CloudKit constraints honored: every stored property has a default, there are
/// no unique constraints, and relationships are optional with explicit inverses.
@Model
final class Discussion {
    var id: UUID = UUID()
    var topic: String = ""
    var title: String = ""
    var background: String = ""
    var language: String = "en-US"
    var commanderName: String = "Commander"
    var commanderModel: String = ""
    var statusRaw: String = DiscussionStatus.planning.rawValue
    var createdAt: Date = Date()
    var updatedAt: Date = Date()

    // Generation / playback.
    var jobID: String?
    var audioURLString: String?
    var downloadURLString: String?
    var durationSeconds: Double = 0

    @Relationship(deleteRule: .cascade, inverse: \Person.discussion)
    var people: [Person]? = []
    @Relationship(deleteRule: .cascade, inverse: \SourceRef.discussion)
    var sources: [SourceRef]? = []
    @Relationship(deleteRule: .cascade, inverse: \TranscriptLine.discussion)
    var lines: [TranscriptLine]? = []

    init(topic: String, title: String = "", background: String = "", language: String = "en-US") {
        self.topic = topic
        self.title = title
        self.background = background
        self.language = language
    }

    var status: DiscussionStatus {
        get { DiscussionStatus(rawValue: statusRaw) ?? .planning }
        set { statusRaw = newValue.rawValue }
    }

    var sortedPeople: [Person] { (people ?? []).sorted { $0.ordinal < $1.ordinal } }
    var sortedSources: [SourceRef] { (sources ?? []).sorted { $0.ordinal < $1.ordinal } }
    var sortedLines: [TranscriptLine] { (lines ?? []).sorted { $0.ordinal < $1.ordinal } }
}

/// One panelist (host or discussant) in a discussion.
@Model
final class Person {
    var id: UUID = UUID()
    var ordinal: Int = 0
    var name: String = ""
    var aspect: String = ""
    var model: String = ""
    var isHost: Bool = false
    var discussion: Discussion?

    init(ordinal: Int, name: String, aspect: String = "", model: String = "", isHost: Bool = false) {
        self.ordinal = ordinal
        self.name = name
        self.aspect = aspect
        self.model = model
        self.isHost = isHost
    }
}

/// A researched reference attached to a discussion plan.
@Model
final class SourceRef {
    var id: UUID = UUID()
    var ordinal: Int = 0
    var title: String = ""
    var urlString: String = ""
    var snippet: String = ""
    var discussion: Discussion?

    init(ordinal: Int, title: String, urlString: String, snippet: String = "") {
        self.ordinal = ordinal
        self.title = title
        self.urlString = urlString
        self.snippet = snippet
    }

    var url: URL? { URL(string: urlString) }
}

/// One spoken utterance in the generated podcast, persisted for replay.
@Model
final class TranscriptLine {
    var id: UUID = UUID()
    var ordinal: Int = 0
    var speaker: String = ""
    var role: String = ""
    var text: String = ""
    var startMs: Int = 0
    var isUser: Bool = false
    var discussion: Discussion?

    init(ordinal: Int, speaker: String, role: String, text: String, startMs: Int = 0, isUser: Bool = false) {
        self.ordinal = ordinal
        self.speaker = speaker
        self.role = role
        self.text = text
        self.startMs = startMs
        self.isUser = isUser
    }
}
