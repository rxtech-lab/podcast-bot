import SwiftUI
import TipKit

struct PlanEditTurn: Identifiable, MessageListItem {
    enum Role: Equatable {
        case user
        case plan
        case loading
        case error
    }

    let id: String
    let role: Role
    let label: String?
    let text: String?
    let snapshot: PlanSnapshot?

    var isUserMessage: Bool { role == .user }
    /// The loading row is an accessory: it must not count as "content after" the
    /// pinned user turn, so the turn stays pinned to the top until the real
    /// updated plan (or an error) replaces it.
    var isMessageListAccessory: Bool { role == .loading }

    static func user(_ text: String, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .user, label: nil, text: text, snapshot: nil)
    }

    static func plan(label: String, snapshot: PlanSnapshot, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .plan, label: label, text: nil, snapshot: snapshot)
    }

    static func loading(id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .loading, label: nil, text: nil, snapshot: nil)
    }

    static func error(_ message: String, id: String = UUID().uuidString) -> PlanEditTurn {
        PlanEditTurn(id: id, role: .error, label: nil, text: message, snapshot: nil)
    }

    var isHistoryBacked: Bool {
        id.hasPrefix("history-")
    }

    /// Displayed label for the fallback "current plan" card. Used both as the
    /// shown text and as the sentinel `isFallbackCurrentPlan` compares against,
    /// so localizing it keeps the comparison correct in every locale.
    static let currentPlanLabel = String(localized: "Current plan",
                                         comment: "Label for the plan card shown when there is no edit history")

    var isFallbackCurrentPlan: Bool {
        role == .plan && !isHistoryBacked && label == PlanEditTurn.currentPlanLabel
    }

    func representsSameVisibleTurn(as other: PlanEditTurn) -> Bool {
        role == other.role
            && normalized(text) == normalized(other.text)
            && normalized(label) == normalized(other.label)
    }

    private func normalized(_ value: String?) -> String {
        (value ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }
}


extension DiscussionEditTurnDTO {
    var planEditTurnID: String {
        if let id { return "history-\(id)" }
        return "history-\(role)-\(createdAt ?? "")-\(text ?? "")"
    }
}


extension PlanSnapshot {
    /// Memberwise initializer for previews/tests (the production type only ships
    /// `init(discussion:)`).
    init(title: String, topic: String, isAudioBook: Bool = false, isUploadedAudio: Bool = false,
         style: String = "", background: String,
         chapters: [PlanChapterSnapshot] = [],
         people: [PlanPersonSnapshot], sources: [PlanSourceSnapshot])
    {
        self.title = title
        self.topic = topic
        self.isAudioBook = isAudioBook
        self.isUploadedAudio = isUploadedAudio
        self.uploadedAudioDurationMs = 0
        self.transcriptSegments = []
        self.uploadedAudioSpeakers = []
        self.style = style
        self.background = background
        self.chapters = chapters
        self.people = people
        self.sources = sources
    }
}

/// Offline harness that exercises the pinned-turn behavior of `MessageList`
/// using the real `PlanEditBubble` rows. Tap send and watch the user message
/// pin to the top while a simulated "Updating plan…" reply streams in, then
/// release to the bottom when the updated plan lands.

