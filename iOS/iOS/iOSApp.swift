//
//  iOSApp.swift
//  iOS
//
//  Debate-bot podcast app: plan a discussion, generate it, and play the
//  audio-only podcast live with synced captions and a per-agent transcript.
//

import SwiftUI
import TipKit
import UIKit
import UserNotifications
import RevenueCat
import Observation

@main
struct iOSApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var auth = AuthManager()
    @State private var purchases: PurchaseManager
    @State private var launchFlow = LaunchFlowStore()
    @State private var deepLinks = DeepLinkRouter()
    @State private var push = PushNotificationManager()
    @State private var playerSessions = PlayerSessionStore()

    init() {
        UIScrollView.appearance().keyboardDismissMode = .interactive
        // In E2E mode, leave TipKit unconfigured so no `.popoverTip` ever
        // displays — an onboarding tip popover would cover the UI (e.g. the
        // new-plan topic field) and make elements non-hittable for the tests.
        if !AppConfig.isE2E {
            try? Tips.configure([
                .datastoreLocation(.applicationDefault),
                .displayFrequency(.immediate)
            ])
        }
        // Configure RevenueCat before anything reads Purchases.shared. Guarded so
        // a missing key disables purchases instead of crashing.
        if AppConfig.hasRevenueCat {
            #if DEBUG
            Purchases.logLevel = .debug
            #endif
            Purchases.configure(withAPIKey: AppConfig.revenueCatAPIKey)
        }
        let auth = AuthManager()
        _auth = State(initialValue: auth)
        _purchases = State(initialValue: PurchaseManager(tokens: auth))

        // E2E: preset the injected deep link before the first render so the
        // resolver's `.task(id:)` in RootView picks it up immediately, avoiding a
        // race with the onAppear hook below.
        if AppConfig.isE2E, let url = AppConfig.e2eDeepLink {
            let router = DeepLinkRouter()
            router.handle(url: url)
            _deepLinks = State(initialValue: router)
        }
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(auth)
                .environment(purchases)
                .environment(launchFlow)
                .environment(deepLinks)
                .environment(push)
                .environment(playerSessions)
                .tint(Theme.accent)
                .scrollDismissesKeyboard(.interactively)
                .onAppear {
                    appDelegate.configure(deepLinks: deepLinks, push: push)
                    // E2E: route a launch-provided deep link through the normal
                    // resolver so deep-link flows are deterministic under XCUITest
                    // (no Safari/simctl round-trip).
                    if AppConfig.isE2E, let url = AppConfig.e2eDeepLink {
                        deepLinks.handle(url: url)
                    }
                }
                // Universal links (https://podcast.rxlab.app/d|s/...) arrive as a
                // browsing user activity; custom-scheme links via onOpenURL.
                .onContinueUserActivity(NSUserActivityTypeBrowsingWeb) { activity in
                    if let url = activity.webpageURL { deepLinks.handle(url: url) }
                }
                .onOpenURL { url in deepLinks.handle(url: url) }
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    private weak var deepLinks: DeepLinkRouter?
    private weak var push: PushNotificationManager?
    private var pendingNotificationURL: URL?

    func configure(deepLinks: DeepLinkRouter, push: PushNotificationManager) {
        self.deepLinks = deepLinks
        self.push = push
        UNUserNotificationCenter.current().delegate = self
        if let pendingNotificationURL {
            self.pendingNotificationURL = nil
            deepLinks.handle(url: pendingNotificationURL)
        }
    }

    func application(_ application: UIApplication,
                     didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Task { @MainActor [weak self] in
            self?.push?.updateDeviceToken(deviceToken)
        }
    }

    func application(_ application: UIApplication,
                     didFailToRegisterForRemoteNotificationsWithError error: Error) {
        Task { @MainActor [weak self] in
            self?.push?.registrationError = error.localizedDescription
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                didReceive response: UNNotificationResponse) async {
        guard let raw = response.notification.request.content.userInfo["url"] as? String,
              let url = URL(string: raw) else { return }
        await MainActor.run {
            if let deepLinks {
                deepLinks.handle(url: url)
            } else {
                pendingNotificationURL = url
            }
        }
    }
}

@Observable
@MainActor
final class PushNotificationManager {
    var deviceToken: String?
    var registrationError: String?

    private var didRequestAuthorization = false
    private var lastRegisteredKey: String?

    var registrationKey: String {
        [AppConfig.apnsEnvironment, deviceToken ?? ""].joined(separator: ":")
    }

    func requestAuthorizationIfNeeded() async {
        // The E2E harness must not trigger the system notification-permission
        // alert, which would cover the UI and block the tests.
        guard !AppConfig.isE2E else { return }
        guard !didRequestAuthorization else { return }
        didRequestAuthorization = true
        do {
            let granted = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .badge, .sound])
            guard granted else { return }
            UIApplication.shared.registerForRemoteNotifications()
        } catch {
            registrationError = error.localizedDescription
        }
    }

    func updateDeviceToken(_ tokenData: Data) {
        deviceToken = tokenData.map { String(format: "%02x", $0) }.joined()
    }

    func syncRegisteredToken(api: APIClient, userID: String?) async {
        guard let userID, let deviceToken, !deviceToken.isEmpty else { return }
        let key = "\(userID):\(registrationKey)"
        guard lastRegisteredKey != key else { return }
        do {
            try await api.registerPushToken(deviceToken, environment: AppConfig.apnsEnvironment)
            lastRegisteredKey = key
            registrationError = nil
        } catch {
            registrationError = error.localizedDescription
        }
    }
}
