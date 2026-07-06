//
//  MaintenanceMonitor.swift
//  iOS
//
//  App-wide sink for scheduled-maintenance windows. Any API response that carries
//  a maintenance window — a 503 while the app is paused, or the allowlisted
//  /api/precheck bootstrap advertising an active/upcoming window — broadcasts it
//  here, and the root view presents a single blocking alert.
//
//  Presentation rules (per the product spec):
//    - Active (ongoing) window: always alert while the app is paused, on every
//      launch. A session guard prevents it re-popping immediately after the user
//      dismisses it, but it returns next launch.
//    - Upcoming (scheduled) window: alert once ever, de-duplicated by id via
//      UserDefaults so the heads-up is not repeated.
//

import Foundation
import Observation

extension Notification.Name {
    // nonisolated so the nonisolated `report(_:)` can post from any thread under
    // the app's default-MainActor isolation.
    nonisolated static let maintenanceDetected = Notification.Name("com.debatebot.maintenanceDetected")
}

@Observable
@MainActor
final class MaintenanceMonitor {
    /// The maintenance window to present, or nil when nothing needs showing.
    /// Bound to the root view's alert.
    var current: MaintenanceInfo?

    /// Persisted ids of scheduled windows already shown, so a heads-up appears
    /// only once across launches.
    private static let seenScheduledKey = "maintenance.seenScheduledIDs"

    /// Active windows dismissed during this session, so the alert does not
    /// re-pop from subsequent failing requests until the app relaunches.
    @ObservationIgnored private var dismissedActiveIDs: Set<Int> = []

    @ObservationIgnored private var observer: NSObjectProtocol?

    init() {
        observer = NotificationCenter.default.addObserver(
            forName: .maintenanceDetected, object: nil, queue: .main
        ) { [weak self] note in
            guard let info = note.object as? MaintenanceInfo else { return }
            MainActor.assumeIsolated { self?.handle(info) }
        }
    }

    deinit {
        if let observer { NotificationCenter.default.removeObserver(observer) }
    }

    /// Broadcasts a maintenance window observed on any API response. Safe to call
    /// from any thread — the monitor applies its presentation rules on the main
    /// actor.
    nonisolated static func report(_ info: MaintenanceInfo) {
        NotificationCenter.default.post(name: .maintenanceDetected, object: info)
    }

    /// Dismisses the current alert, remembering an active window for this session
    /// so it does not immediately reappear.
    func dismiss() {
        if let info = current, info.active {
            dismissedActiveIDs.insert(info.id)
        }
        current = nil
    }

    private func handle(_ info: MaintenanceInfo) {
        if info.active {
            // Always warn while the app is actually paused — but not repeatedly
            // within one session after the user has dismissed it.
            guard !dismissedActiveIDs.contains(info.id) else { return }
            current = info
            return
        }
        // Upcoming scheduled window: show once ever, keyed by id.
        var seen = Self.seenScheduledIDs()
        guard !seen.contains(info.id) else { return }
        seen.insert(info.id)
        UserDefaults.standard.set(Array(seen), forKey: Self.seenScheduledKey)
        current = info
    }

    private static func seenScheduledIDs() -> Set<Int> {
        Set(UserDefaults.standard.array(forKey: seenScheduledKey) as? [Int] ?? [])
    }
}
