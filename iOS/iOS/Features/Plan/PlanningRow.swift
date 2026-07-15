import SwiftUI

struct PlanningRow: Identifiable, MessageListItem {
    enum Content {
        case part(PlanningPart)
        case loading
        case transcribing(String)
    }

    let id: String
    let content: Content

    var isUserMessage: Bool {
        // Planning rows keep user text visually right-aligned in `textBubble`,
        // but should not opt into MessageList's chat turn-pinning spacer. That
        // spacer can leave the finished plan above an empty bottom region.
        return false
    }

    var isMessageListAccessory: Bool {
        switch content {
        case .loading, .transcribing:
            return true
        case .part:
            return false
        }
    }
}
