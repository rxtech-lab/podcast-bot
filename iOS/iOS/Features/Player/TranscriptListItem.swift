import AVKit
import Kingfisher
import MarkdownUI
import Photos
import PhotosUI
import RxAuthSwift
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif
import UniformTypeIdentifiers
import os

enum TranscriptListItem: Identifiable, MessageListItem {
    /// `isMine` is the current user's ownership of the line (server-owned identity,
    /// not the broad `LiveLine.isUser` "human-authored" flag). It drives both the
    /// bubble styling and `isUserMessage` pinning, so another participant's message
    /// is never pinned/scrolled as if it were my outgoing turn.
    case line(LiveLine, isMine: Bool)
    case usage(id: UUID, points: String)
    case generateMore(id: UUID, pendingCount: Int)

    var id: UUID {
        switch self {
        case .line(let line, _): return line.id
        case .usage(let id, _): return id
        case .generateMore(let id, _): return id
        }
    }

    var isUserMessage: Bool {
        if case .line(_, let isMine) = self { return isMine }
        return false
    }

    /// The points summary is an accessory — it never participates in user-message
    /// pinning.
    var isMessageListAccessory: Bool {
        switch self {
        case .usage, .generateMore: return true
        case .line: return false
        }
    }
}
