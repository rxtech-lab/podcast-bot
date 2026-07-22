import Kingfisher
import SwiftUI
import TipKit
#if canImport(UIKit)
import UIKit
#endif

@MainActor
final class IdleSleepPrevention {
    static let shared = IdleSleepPrevention()

    private var activeTokens: Set<UUID> = []
    #if canImport(UIKit)
    private var previousIdleTimerState: Bool?
    #else
    private var sleepActivityToken: NSObjectProtocol?
    #endif

    func begin(token: UUID) {
        guard !activeTokens.contains(token) else { return }
        if activeTokens.isEmpty {
            #if canImport(UIKit)
            previousIdleTimerState = UIApplication.shared.isIdleTimerDisabled
            UIApplication.shared.isIdleTimerDisabled = true
            #else
            sleepActivityToken = ProcessInfo.processInfo.beginActivity(
                options: [.idleDisplaySleepDisabled, .idleSystemSleepDisabled],
                reason: "Podcast playback"
            )
            #endif
        }
        activeTokens.insert(token)
    }

    func end(token: UUID) {
        guard activeTokens.remove(token) != nil else { return }
        if activeTokens.isEmpty {
            #if canImport(UIKit)
            UIApplication.shared.isIdleTimerDisabled = previousIdleTimerState ?? false
            previousIdleTimerState = nil
            #else
            if let sleepActivityToken {
                ProcessInfo.processInfo.endActivity(sleepActivityToken)
                self.sleepActivityToken = nil
            }
            #endif
        }
    }
}
