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
import RevenueCat

@main
struct iOSApp: App {
    @State private var auth = AuthManager()
    @State private var purchases: PurchaseManager
    @State private var launchFlow = LaunchFlowStore()
    @State private var deepLinks = DeepLinkRouter()

    init() {
        UIScrollView.appearance().keyboardDismissMode = .interactive
        try? Tips.configure([
            .datastoreLocation(.applicationDefault),
            .displayFrequency(.immediate)
        ])
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
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(auth)
                .environment(purchases)
                .environment(launchFlow)
                .environment(deepLinks)
                .tint(Theme.accent)
                .scrollDismissesKeyboard(.interactively)
                // Universal links (https://podcast.rxlab.app/d|s/...) arrive as a
                // browsing user activity; custom-scheme links via onOpenURL.
                .onContinueUserActivity(NSUserActivityTypeBrowsingWeb) { activity in
                    if let url = activity.webpageURL { deepLinks.handle(url: url) }
                }
                .onOpenURL { url in deepLinks.handle(url: url) }
        }
    }
}
