//
//  iOSApp.swift
//  iOS
//
//  Debate-bot podcast app: plan a discussion, generate it, and play the
//  audio-only podcast live with synced captions and a per-agent transcript.
//

import SwiftUI
import UIKit

@main
struct iOSApp: App {
    @State private var auth = AuthManager()

    init() {
        UIScrollView.appearance().keyboardDismissMode = .interactive
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(auth)
                .tint(Theme.accent)
                .scrollDismissesKeyboard(.interactively)
        }
    }
}
