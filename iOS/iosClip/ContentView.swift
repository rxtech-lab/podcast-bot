//
//  ContentView.swift
//  iosClip
//
//  App Clip landing. Parses the invoking share link, validates it with the
//  backend, then gates the join call behind rxlab sign-in.
//

import Foundation
import Observation
import OSLog
import RxAuthSwift
import RxAuthSwiftUI
import SwiftUI

let clipLog = Logger(subsystem: "app.rxlab.debate-bot.Clip", category: "Share")
private let clipAccent = Color(red: 0.0, green: 0.48, blue: 1.0)

struct ClipRootView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(PurchaseManager.self) private var purchases
    @Environment(ClipShareRouter.self) private var router

    var body: some View {
        ZStack {
            Color(.systemBackground).ignoresSafeArea()
            content
        }
        .tint(clipAccent)
        .task { await restoreAuth() }
        .task(id: auth.currentUser?.id) {
            if let subject = auth.currentUser?.id {
                await purchases.identify(userID: subject)
            }
        }
        .task(id: router.pendingKey) { await router.resolveIfNeeded() }
        .task(id: joinKey) { await joinIfReady() }
    }

    @ViewBuilder private var content: some View {
        switch router.state {
        case .idle:
            ClipStatusView(message: "Open a shared link to listen and join a discussion.")
        case .checking:
            ProgressView("Opening...")
        case let .failed(message):
            ClipStatusView(message: message)
        case .requiresSignIn:
            signedOutOrJoiningContent(title: "this discussion")
        case let .resolved(metadata):
            signedOutOrJoiningContent(title: metadata.title)
        }
    }

    @ViewBuilder private func signedOutOrJoiningContent(title: String) -> some View {
        switch auth.authState {
        case .unknown:
            ProgressView()
        case .unauthenticated:
            RxSignInView(
                manager: auth.manager,
                appearance: RxSignInAppearance(
                    icon: .systemImage("waveform.circle.fill"),
                    title: "PanelFM",
                    subtitle: "Sign in with rxlab to join \(title).",
                    accentColor: clipAccent,
                    secondaryColor: .cyan
                ),
                onAuthSuccess: {
                    clipLog.info("App Clip sign-in succeeded")
                    Task { await restoreAuth() }
                },
                onAuthFailed: { error in
                    clipLog.error("App Clip sign-in failed: \(error.localizedDescription, privacy: .public)")
                }
            )
        case .authenticated:
            joinedContent(title: title)
        }
    }

    @ViewBuilder private func joinedContent(title: String) -> some View {
        switch router.joinState {
        case .idle, .joining:
            ProgressView("Joining...")
        case let .joined(discussion):
            NavigationStack {
                PodcastPlayerView(discussion: discussion,
                                  shareToken: router.resolvedToken)
            }
        case let .failed(message):
            ClipStatusView(message: message)
        }
    }

    private var joinKey: String {
        guard auth.authState == .authenticated else { return "signed-out" }
        let token = router.resolvedToken ?? "none"
        return "\(token):\(router.canAttemptJoin ? "ready" : "waiting")"
    }

    private func restoreAuth() async {
        clipLog.info("App Clip auth restore started")
        await auth.restore()
        clipLog.info("App Clip auth restore finished: \(String(describing: self.auth.authState), privacy: .public)")
    }

    private func joinIfReady() async {
        guard auth.authState == .authenticated else { return }
        await router.joinIfNeeded(auth: auth)
    }
}

private struct ClipStatusView: View {
    let message: String

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "waveform.circle.fill")
                .font(.system(size: 56))
                .foregroundStyle(clipAccent)
            Text("PanelFM")
                .font(.title2.weight(.semibold))
            Text(message)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
        }
        .padding(32)
    }
}

@MainActor
@Observable
final class ClipShareRouter {
    enum ResolveState: Equatable {
        case idle
        case checking
        case requiresSignIn
        case resolved(ClipShareMetadata)
        case failed(String)
    }

    enum JoinState: Equatable {
        case idle
        case joining
        case joined(Discussion)
        case failed(String)
    }

    var pending: ClipDeepLink?
    var state: ResolveState = .idle
    var joinState: JoinState = .idle

    private let api = ClipShareAPI()

    var pendingKey: String {
        switch pending {
        case .none: return ""
        case let .shared(token): return "s:\(token)"
        }
    }

    var resolvedToken: String? {
        if case let .shared(token) = pending {
            return token
        }
        return nil
    }

    var canAttemptJoin: Bool {
        state.canAttemptJoin
    }

    func handle(url: URL) {
        clipLog.info("App Clip handling URL: scheme=\(url.scheme ?? "", privacy: .public) host=\(url.host ?? "", privacy: .public) path=\(url.path, privacy: .public)")
        guard let link = ClipDeepLink(url: url) else {
            clipLog.error("App Clip rejected unsupported URL: \(url.absoluteString, privacy: .public)")
            state = .failed("This link is not a PanelFM share link.")
            joinState = .idle
            return
        }
        clipLog.info("App Clip accepted share URL")
        pending = link
        state = .idle
        joinState = .idle
    }

    func resolveIfNeeded() async {
        guard case let .shared(token) = pending else { return }
        if case .resolved = state { return }
        if case .requiresSignIn = state { return }
        clipLog.info("App Clip resolving share metadata")
        state = .checking
        do {
            let metadata = try await api.resolve(token: token)
            clipLog.info("App Clip share resolve succeeded: discussion=\(metadata.id, privacy: .public)")
            state = .resolved(metadata)
        } catch ClipShareError.unauthorized {
            clipLog.warning("App Clip share resolve returned 401; showing sign-in without metadata")
            state = .requiresSignIn
        } catch {
            clipLog.error("App Clip share resolve failed: \(error.localizedDescription, privacy: .public)")
            state = .failed(error.localizedDescription)
        }
    }

    func joinIfNeeded(auth: AuthManager) async {
        guard case let .shared(token) = pending else { return }
        guard state.canAttemptJoin else { return }
        guard joinState != .joining else { return }
        if case .joined = joinState { return }
        clipLog.info("App Clip joining share")
        joinState = .joining
        do {
            guard await auth.refreshedAccessToken() != nil else {
                clipLog.error("App Clip join skipped: no bearer token")
                joinState = .failed("Sign in again to join this discussion.")
                return
            }
            let discussion = try await APIClient(tokens: auth).joinViaShare(token: token)
            clipLog.info("App Clip share join succeeded: discussion=\(discussion.id, privacy: .public)")
            joinState = .joined(discussion)
        } catch {
            clipLog.error("App Clip share join failed: \(error.localizedDescription, privacy: .public)")
            joinState = .failed(error.localizedDescription)
        }
    }
}

private extension ClipShareRouter.ResolveState {
    var canAttemptJoin: Bool {
        switch self {
        case .resolved, .requiresSignIn:
            return true
        case .idle, .checking, .failed:
            return false
        }
    }
}

enum ClipDeepLink: Equatable {
    case shared(String)

    init?(url: URL) {
        let components = url.pathComponents.filter { $0 != "/" }
        if url.scheme == "debatepod", url.host == "s", let token = components.first {
            clipLog.info("App Clip parsed custom-scheme share token")
            self = .shared(token)
            return
        }
        guard components.count >= 2, components[0] == "s" else {
            clipLog.error("App Clip URL parse failed: components=\(components.joined(separator: "/"), privacy: .public)")
            return nil
        }
        clipLog.info("App Clip parsed universal-link share token")
        self = .shared(components[1])
    }
}

struct ClipShareMetadata: Decodable, Equatable {
    let id: String
    let title: String
}

private struct ClipShareAPI {
    private let baseURL = AppConfig.apiBaseURL
    private let session: URLSession = .shared

    func resolve(token: String) async throws -> ClipShareMetadata {
        let url = shareURL(token: token)
        clipLog.info("App Clip GET \(url.absoluteString, privacy: .public)")
        let (data, response) = try await session.data(from: url)
        try validate(data: data, response: response)
        return try JSONDecoder().decode(ClipShareMetadata.self, from: data)
    }

    private func validate(data: Data, response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else {
            throw ClipShareError.message("The server did not return a valid response.")
        }
        clipLog.info("App Clip HTTP status \(http.statusCode, privacy: .public) for \(http.url?.path ?? "", privacy: .public)")
        guard (200..<300).contains(http.statusCode) else {
            let body = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            switch http.statusCode {
            case 401:
                throw ClipShareError.unauthorized
            case 409:
                throw ClipShareError.message("This discussion is full.")
            case 410:
                throw ClipShareError.message("This share link has expired or was revoked.")
            default:
                throw ClipShareError.message(body.isEmpty ? "Request failed (\(http.statusCode))." : body)
            }
        }
    }

    private func shareURL(token: String, join: Bool = false) -> URL {
        var url = baseURL
        url.appendPathComponent("api")
        url.appendPathComponent("share")
        url.appendPathComponent(token)
        if join {
            url.appendPathComponent("join")
        }
        return url
    }
}

private enum ClipShareError: LocalizedError {
    case unauthorized
    case message(String)

    var errorDescription: String? {
        switch self {
        case .unauthorized: return "Sign in to join this discussion."
        case let .message(message): return message
        }
    }
}

#Preview {
    ClipRootView()
        .environment(AuthManager())
        .environment(PurchaseManager(tokens: AuthManager()))
        .environment(ClipShareRouter())
}
