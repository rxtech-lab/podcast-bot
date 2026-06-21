//
//  ContentView.swift
//  iOS
//
//  Root view: gates on rxlab auth, then shows the discussion library.
//

import SwiftUI
import RxAuthSwift
import RxAuthSwiftUI

struct RootView: View {
    @Environment(AuthManager.self) private var auth

    var body: some View {
        Group {
            switch auth.authState {
            case .unknown:
                ZStack {
                    Theme.background.ignoresSafeArea()
                    ProgressView().tint(Theme.accent)
                }
                .task { await auth.restore() }
            case .unauthenticated:
                SignInScreen()
            case .authenticated:
                LibraryView()
            }
        }
    }
}

/// rxlab sign-in, styled to match the app.
private struct SignInScreen: View {
    @Environment(AuthManager.self) private var auth

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            RxSignInView(
                manager: auth.manager,
                appearance: RxSignInAppearance(
                    title: "Debate Podcasts",
                    subtitle: "Sign in with rxlab to plan and generate AI discussions.",
                    accentColor: Theme.accent
                ),
                onAuthSuccess: {},
                onAuthFailed: { _ in }
            )
        }
    }
}
