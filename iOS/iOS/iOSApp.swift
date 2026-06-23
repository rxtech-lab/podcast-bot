//
//  iOSApp.swift
//  iOS
//
//  Debate-bot podcast app: plan a discussion, generate it, and play the
//  audio-only podcast live with synced captions and a per-agent transcript.
//

import SwiftUI
import UIKit
import RevenueCat

@main
struct iOSApp: App {
    @State private var auth = AuthManager()
    @State private var purchases: PurchaseManager
    @State private var launchFlow = LaunchFlowStore()

    init() {
        UIScrollView.appearance().keyboardDismissMode = .interactive
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
                .tint(Theme.accent)
                .scrollDismissesKeyboard(.interactively)
        }
    }
}
