import Kingfisher
import SwiftUI
import TipKit
import UIKit

@MainActor
final class IdleSleepPrevention {
    static let shared = IdleSleepPrevention()

    private var activeTokens: Set<UUID> = []
    private var previousIdleTimerState: Bool?

    func begin(token: UUID) {
        guard !activeTokens.contains(token) else { return }
        if activeTokens.isEmpty {
            previousIdleTimerState = UIApplication.shared.isIdleTimerDisabled
            UIApplication.shared.isIdleTimerDisabled = true
        }
        activeTokens.insert(token)
    }

    func end(token: UUID) {
        guard activeTokens.remove(token) != nil else { return }
        if activeTokens.isEmpty {
            UIApplication.shared.isIdleTimerDisabled = previousIdleTimerState ?? false
            previousIdleTimerState = nil
        }
    }
}
