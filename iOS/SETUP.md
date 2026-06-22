# iOS app setup

The Swift sources, xcconfig, `Info.plist`, and entitlements are all written. A few
one-time Xcode wiring steps remain (these touch the project file / signing, which
is safer to do in Xcode than by hand-editing `project.pbxproj`).

## 1. Add Swift Package dependencies
In Xcode → the `iOS` project → **Package Dependencies** → **+**, add:

| URL | Products to add to the `iOS` target |
|-----|--------------------------------------|
| `https://github.com/rxtech-lab/RxAuthSwift` | `RxAuthSwift`, `RxAuthSwiftUI` |
| `https://github.com/sirily11/SwiftStreamingMarkdown` | `SwiftStreamingMarkdown` |

> If SwiftStreamingMarkdown's public view isn't `MarkdownView(text:)`, fix the one
> call site in `iOS/iOS/Design/MarkdownText.swift`.

## 2. Point the target at the xcconfig files
Project → **Info** tab → **Configurations**. For the `iOS` target set:
- **Debug** → `Config/Debug.xcconfig`
- **Release** → `Config/Release.xcconfig`

`AppAuthClientID` is committed in the Debug/Release xcconfig files because the
OAuth client id is public app configuration, not a secret.

## 3. Use the committed Info.plist + entitlements
In the `iOS` target → **Build Settings**:
- `INFOPLIST_FILE` = `Config/Info.plist`
- `GENERATE_INFOPLIST_FILE` = `NO`
- `CODE_SIGN_ENTITLEMENTS` = `Config/iOS.entitlements`

## 4. Enable capabilities (Signing & Capabilities tab)
- **iCloud** → CloudKit, container `iCloud.app.rxlab.debate-bot` (matches the entitlement).
- **Background Modes** → Audio, AirPlay, and Picture in Picture + Remote notifications.
- **Associated Domains** → `webcredentials:rxlab.app` (already in the entitlements file).

The OAuth redirect scheme (`debatepod://oauth-callback`) and background `audio` mode
are already declared in `Config/Info.plist`.

## 5. Register the rxlab OAuth client
Register a client in rxlab-auth (auth.rxlab.app) with:
- redirect URI `debatepod://oauth-callback`
- the app's `teamID.bundleID` (`P9KK452K8P.app.rxlab.debate-bot`) for passkey/associated-domain
Put the resulting client id in `Config/Debug.xcconfig` and
`Config/Release.xcconfig`.

## 6. Backend env (engine)
Run the engine with these env vars so the app can authenticate and research:
- `AUTH_ISSUER=https://auth.rxlab.app` — enables per-user bearer auth (validates the
  RxAuthSwift access token against the issuer's `/api/oauth/userinfo`).
- `SEARCH_API_KEY=...` (and optionally `SEARCH_API_URL`) — enables researched sources
  in the planner (Tavily-compatible). Without it, plans still work but carry no sources.

Local dev: `go run ./cmd/debate-bot --addr :8000` (matches `Config/Debug.xcconfig`).

## Architecture (engine endpoints the app uses)
- `POST /api/plan` / `POST /api/plan/improve` — plan + chat-edit (returns `sources`).
- `POST /api/jobs/json` with `videoConfig.audio_only=true` — start generation.
- `GET /api/jobs/{id}/hls/stream.m3u8` — **live** audio HLS (AVPlayer).
- `GET /api/jobs/{id}/ws` — live per-agent transcript + phase events.
- `GET /api/jobs/{id}/subtitles/live` — incremental WebVTT captions.
- `GET /api/jobs/{id}` — status + `download_url` when done.
- `GET /api/jobs/{id}/audio` — final MP3 (S3 redirect).
