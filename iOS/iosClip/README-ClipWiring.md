# App Clip wiring (one-time Xcode step)

The clip's Swift implementation (`iosClipApp.swift`, `ContentView.swift` →
`ClipRootView`) and entitlements (`iosClip.entitlements`: `appclips:` +
`applinks:podcast.rxlab.app`, `webcredentials:rxlab.app`) are in
place. Because the project uses **Xcode 16+ synchronized folders**, sharing the
main app's code with the clip target is a target-membership operation done in
Xcode (safer than hand-editing `project.pbxproj`).

## 1. Add shared source to the `iosClip` target

In Xcode, select these files and check **iosClip** in the File Inspector →
Target Membership (or add the folders to the clip target's synchronized groups):

- `iOS/Networking/` — `APIClient.swift`, `APIModels.swift`, `ShareModels.swift`,
  `DeepLinkRouter.swift`, `AcceptLanguage.swift`, `JobSocket.swift`
- `iOS/Persistence/DataModels.swift`
- `iOS/Auth/AuthManager.swift`
- `iOS/Support/AppConfig.swift`, `iOS/Config.swift`, `iOS/Theme.swift`
- `iOS/Features/Player/PlayerModel.swift`, `FullScreenPlayerView.swift`,
  `CoverPalette.swift`, and the `PodcastActionsMenu` / `DownloadProgressSheet`
  structs (currently in `PodcastPlayerView.swift`).

## 2. Keep RevenueCat OUT of the clip (size budget)

`FullScreenPlayerView` reuses `PodcastActionsMenu` and `DownloadProgressSheet`,
which today live in `PodcastPlayerView.swift` — a file whose `PodcastPlayerView`
struct depends on `PurchaseManager` (RevenueCat). Two options:

- **Recommended:** extract `PodcastActionsMenu` and `DownloadProgressSheet` into
  their own file (e.g. `PlayerMenus.swift`) and add only that + the player files
  to the clip. Do **not** add `PodcastPlayerView.swift` or `PurchaseManager` to
  the clip, and do not link the RevenueCat package to the clip target.
- Or: link RevenueCat into the clip and provide a `PurchaseManager` — larger clip.

## 3. Package dependencies for the clip target

Link **RxAuthSwift** and **RxAuthSwiftUI** to the `iosClip` target (used by
`AuthManager` and `RxSignInView`). AVFoundation/MediaPlayer come with the SDK.

## 4. Remove boilerplate

`iosClip/Item.swift` (SwiftData template) is unused — remove it from the clip.

## 5. Build settings

- Set the clip's `AppWebsiteBaseURL` / `AppAPIBaseURL` Info.plist values to match
  the app (or rely on the compiled defaults in `AppConfig`).
- Confirm the clip bundle id is `app.rxlab.debate-bot.Clip` (matches the AASA
  `appclips` entry and the website's `apple-itunes-app` meta tag).

## Full-participate clip implementation (drop in after step 1–3)

The active `iosClipApp.swift` / `ContentView.swift` are currently a **minimal,
self-contained** version so the project keeps building before the shared code is
wired into the clip target. Once steps 1–3 above are done, replace them with the
full implementation below (reuses `PlayerModel` + `FullScreenPlayerView`):

```swift
// iosClipApp.swift
import SwiftUI

@main
struct iosClipApp: App {
    @State private var auth = AuthManager()
    @State private var deepLinks = DeepLinkRouter()

    var body: some Scene {
        WindowGroup {
            ClipRootView()
                .environment(auth)
                .environment(deepLinks)
                .tint(Theme.accent)
                .onContinueUserActivity(NSUserActivityTypeBrowsingWeb) { activity in
                    if let url = activity.webpageURL { deepLinks.handle(url: url) }
                }
                .onOpenURL { url in deepLinks.handle(url: url) }
        }
    }
}
```

```swift
// ContentView.swift
import SwiftUI
import RxAuthSwift
import RxAuthSwiftUI

struct ClipRootView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(DeepLinkRouter.self) private var deepLinks

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            switch auth.authState {
            case .unknown:
                ProgressView().tint(Theme.accent).task { await auth.restore() }
            case .unauthenticated:
                RxSignInView(
                    manager: auth.manager,
                    appearance: RxSignInAppearance(
                        title: AppStringLiteral.appTitle,
                        subtitle: "Sign in with rxlab to join this discussion.",
                        accentColor: Theme.accent),
                    onAuthSuccess: {}, onAuthFailed: { _ in })
            case .authenticated:
                content
            }
        }
        .task(id: resolveKey) { await resolveIfNeeded() }
        .alert("Couldn't open link", isPresented: Binding(
            get: { deepLinks.error != nil }, set: { if !$0 { deepLinks.error = nil } })) {
            Button("OK", role: .cancel) { deepLinks.error = nil }
        } message: { Text(deepLinks.error ?? "") }
    }

    @ViewBuilder private var content: some View {
        if let opened = deepLinks.opened {
            ClipPlayerView(discussion: opened.discussion, shareToken: opened.shareToken)
        } else if deepLinks.isResolving {
            ProgressView("Opening…").tint(Theme.accent)
        } else {
            Text("Open a shared link to listen and join.")
                .foregroundStyle(Theme.secondaryText).padding(32)
        }
    }

    private var resolveKey: String {
        guard auth.authState == .authenticated else { return "unauth" }
        switch deepLinks.pending {
        case .none: return "auth"
        case let .publicDiscussion(id): return "d:\(id)"
        case let .sharedDiscussion(token): return "s:\(token)"
        }
    }

    private func resolveIfNeeded() async {
        guard auth.authState == .authenticated, deepLinks.pending != nil else { return }
        await deepLinks.resolvePending(api: APIClient(tokens: auth))
    }
}

private struct ClipPlayerView: View {
    let discussion: Discussion
    let shareToken: String?
    @Environment(AuthManager.self) private var auth
    @State private var model: PlayerModel?
    @State private var message = ""

    var body: some View {
        ZStack {
            if let model {
                FullScreenPlayerView(model: model)
                    .safeAreaInset(edge: .bottom) { composer(model) }
            } else { ProgressView().tint(Theme.accent) }
        }
        .task {
            if model == nil {
                let m = PlayerModel(discussion: discussion, api: APIClient(tokens: auth),
                                    username: auth.currentUser?.name ?? "You", shareToken: shareToken)
                m.start(); model = m
            }
        }
        .onDisappear { model?.stop() }
    }

    private func composer(_ model: PlayerModel) -> some View {
        HStack(spacing: 8) {
            TextField("Add a comment…", text: $message, axis: .vertical)
                .textFieldStyle(.roundedBorder).lineLimit(1...3)
            Button { let t = message; message = ""; model.send(t) } label: {
                Image(systemName: "arrow.up.circle.fill").font(.title2)
            }.disabled(message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .padding(.horizontal, 16).padding(.vertical, 10).background(.ultraThinMaterial)
    }
}
```

## Testing the invocation

Run the `iosClip` scheme with the env var
`_XCAppClipURL=https://podcast.rxlab.app/s/<token>` (Scheme → Run → Arguments)
to simulate opening a shared link.
