//
//  ContentView.swift
//  iOS
//
//  Root view: gates on rxlab auth, then shows the discussion library.
//

import RxAuthSwift
import RxAuthSwiftUI
import SwiftUI

struct RootView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(LaunchFlowStore.self) private var launchFlow
    @Environment(DeepLinkRouter.self) private var deepLinks
    @Environment(PushNotificationManager.self) private var push

    @State private var launchPlan: LaunchPlanPresentation?
    @State private var didRunLaunchFlow = false

    /// Wraps the captured steps so the launch sheet is presented via `item:`
    /// (data travels with the presentation, avoiding a stale-state race where
    /// the sheet renders before the steps array propagates).
    private struct LaunchPlanPresentation: Identifiable {
        let id = UUID()
        let steps: [LaunchStep]
    }

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
                    .task { await purchases.signOut() }
            case .authenticated:
                LibraryView()
                    // Attribute RevenueCat purchases to this user (re-runs if the
                    // signed-in subject changes) so the webhook credits the right
                    // account.
                    .task(id: auth.currentUser?.id) {
                        if let subject = auth.currentUser?.id {
                            await purchases.identify(userID: subject)
                        }
                    }
                    .task { await startLaunchFlow() }
                    .task { await push.requestAuthorizationIfNeeded() }
                    .task(id: pushSyncKey) {
                        await push.syncRegisteredToken(api: APIClient(tokens: auth),
                                                       userID: auth.currentUser?.id)
                    }
                    .sheet(item: $launchPlan) { plan in
                        LaunchFlowView(
                            steps: plan.steps,
                            onWelcomeSeen: { launchFlow.markWelcomeSeen() },
                            onFeaturesSeen: { launchFlow.markFeaturesSeen($0) },
                            onFinished: { launchPlan = nil }
                        )
                    }
                    // Resolve a deep link captured before/at sign-in, then again
                    // whenever a new one arrives while signed in.
                    .task(id: deepLinkPendingKey) { await resolveDeepLink() }
                    .fullScreenCover(item: Bindable(deepLinks).opened) { opened in
                        NavigationStack {
                            PodcastPlayerView(discussion: opened.discussion,
                                              shareToken: opened.shareToken)
                                .toolbar {
                                    ToolbarItem(placement: .topBarLeading) {
                                        Button("Close") { deepLinks.opened = nil }
                                    }
                                }
                        }
                    }
                    .alert("Couldn't open link", isPresented: Binding(
                        get: { deepLinks.error != nil },
                        set: { if !$0 { deepLinks.error = nil } }
                    )) {
                        Button("OK", role: .cancel) { deepLinks.error = nil }
                    } message: {
                        Text(deepLinks.error ?? "")
                    }
            }
        }
    }

    /// Changes when a new deep link is captured, retriggering resolution.
    private var deepLinkPendingKey: String {
        switch deepLinks.pending {
        case .none: return ""
        case let .publicDiscussion(id): return "d:\(id)"
        case let .sharedDiscussion(token): return "s:\(token)"
        }
    }

    private var pushSyncKey: String {
        "\(auth.currentUser?.id ?? ""):\(push.registrationKey)"
    }

    private func resolveDeepLink() async {
        guard deepLinks.pending != nil else { return }
        await deepLinks.resolvePending(api: APIClient(tokens: auth))
    }

    /// Computes the launch flow once per process, after subscription status is
    /// settled (so `isPro` is accurate). Captures the steps into a stable batch
    /// before presenting.
    private func startLaunchFlow() async {
        guard !didRunLaunchFlow else { return }
        // The E2E harness drives the library/player directly; the welcome,
        // what's-new, and paywall sheets would cover the UI and break the tests.
        if AppConfig.isE2E {
            didRunLaunchFlow = true
            return
        }
        // Wait until RevenueCat has loaded customer info (or purchases are
        // disabled) so `isPro` reflects the real entitlement state. Bounded so a
        // failed load doesn't block the flow forever (~5s).
        var attempts = 0
        while purchases.isConfigured && purchases.customerInfo == nil && attempts < 50 {
            try? await Task.sleep(for: .milliseconds(100))
            attempts += 1
        }
        didRunLaunchFlow = true

        let steps = LaunchFlowPlan.steps(
            hasSeenWelcome: launchFlow.hasSeenWelcome,
            unseenFeatures: launchFlow.unseenFeatures,
            isPro: purchases.isPro
        )
        guard !steps.isEmpty else { return }
        launchPlan = LaunchPlanPresentation(steps: steps)
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
                    title: AppStringLiteral.appTitle,
                    subtitle: "Sign in with rxlab to plan and generate AI \(AppStringLiteral.appTitleRaw).",
                    accentColor: Theme.accent
                ),
                style: .native,
                onAuthSuccess: {},
                onAuthFailed: { _ in }
            )
        }
    }
}
