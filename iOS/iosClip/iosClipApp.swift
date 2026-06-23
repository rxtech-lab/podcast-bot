//
//  iosClipApp.swift
//  iosClip
//
//  App Clip entry. Captures the invoking deep link and routes it through the
//  clip's own auth/share resolver instead of handing the user back to Safari.
//

import OSLog
import SwiftUI

@main
struct iosClipApp: App {
    @State private var auth: AuthManager
    @State private var purchases: PurchaseManager
    @State private var router = ClipShareRouter()

    init() {
        let auth = AuthManager()
        _auth = State(initialValue: auth)
        _purchases = State(initialValue: PurchaseManager(tokens: auth))
    }

    var body: some Scene {
        WindowGroup {
            ClipRootView()
                .environment(auth)
                .environment(purchases)
                .environment(router)
                .onContinueUserActivity(NSUserActivityTypeBrowsingWeb) { activity in
                    if let url = activity.webpageURL {
                        clipLog.info("App Clip received browsing activity URL: \(url.absoluteString, privacy: .public)")
                        router.handle(url: url)
                    } else {
                        clipLog.error("App Clip browsing activity had no webpageURL")
                    }
                }
                .onAppear {
                    if let raw = ProcessInfo.processInfo.environment["_XCAppClipURL"],
                       let url = URL(string: raw) {
                        clipLog.info("App Clip received _XCAppClipURL: \(raw, privacy: .public)")
                        router.handle(url: url)
                    } else {
                        clipLog.info("App Clip appeared without _XCAppClipURL")
                    }
                }
                .onOpenURL { url in
                    clipLog.info("App Clip received openURL: \(url.absoluteString, privacy: .public)")
                    router.handle(url: url)
                }
        }
    }
}
