import Foundation
import Observation

@MainActor
@Observable
final class PlayerSession {
    let key: PlayerSessionStore.Key
    let model: PlayerModel
    var isFullPlayerPresented = false
    #if os(macOS)
    /// The podcast screen and the detached player window can outlive one
    /// another. Track the source screen separately so closing the window only
    /// stops playback when no podcast screen still owns the session.
    var isHostPresented = false
    #endif

    init(key: PlayerSessionStore.Key, model: PlayerModel) {
        self.key = key
        self.model = model
    }
}

@MainActor
@Observable
final class PlayerSessionStore {
    struct Key: Hashable {
        var discussionID: String
        var shareToken: String?
    }

    typealias ModelFactory = @MainActor (Discussion, APIClient, String, String, String?) -> PlayerModel

    @ObservationIgnored private var sessions: [Key: PlayerSession] = [:]
    @ObservationIgnored private var pendingReleases: [Key: Task<Void, Never>] = [:]
    @ObservationIgnored private let releaseGracePeriod: Duration
    @ObservationIgnored private let startsModels: Bool
    @ObservationIgnored private let modelFactory: ModelFactory

    #if os(macOS)
    private(set) var presentedPlayerKey: Key?

    var presentedPlayerSession: PlayerSession? {
        guard let presentedPlayerKey else { return nil }
        return sessions[presentedPlayerKey]
    }
    #endif

    var activeSessionCount: Int { sessions.count }

    init(
        releaseGracePeriod: Duration = .milliseconds(750),
        startsModels: Bool = true,
        modelFactory: @escaping ModelFactory = { discussion, api, username, userID, shareToken in
            PlayerModel(discussion: discussion,
                        api: api,
                        username: username,
                        userID: userID,
                        shareToken: shareToken)
        }
    ) {
        self.releaseGracePeriod = releaseGracePeriod
        self.startsModels = startsModels
        self.modelFactory = modelFactory
    }

    func acquire(
        discussion: Discussion,
        api: APIClient,
        username: String,
        userID: String,
        shareToken: String? = nil
    ) -> PlayerSession {
        let key = Key(discussionID: discussion.id, shareToken: shareToken)
        pendingReleases[key]?.cancel()
        pendingReleases[key] = nil

        if let session = sessions[key] {
            session.model.discussion = PlayerModel.mergingLocalDiscussionState(
                current: session.model.discussion,
                fresh: discussion
            )
            return session
        }

        let model = modelFactory(discussion, api, username, userID, shareToken)
        if startsModels {
            model.start()
        }
        let session = PlayerSession(key: key, model: model)
        sessions[key] = session
        return session
    }

    func release(_ session: PlayerSession) {
        let key = session.key
        guard sessions[key] === session else { return }
        pendingReleases[key]?.cancel()
        let releaseGracePeriod = self.releaseGracePeriod

        let task = Task { [weak self, weak session] in
            try? await Task.sleep(for: releaseGracePeriod)
            guard !Task.isCancelled else { return }
            await MainActor.run {
                guard let self else { return }
                self.pendingReleases[key] = nil
                guard let session,
                      self.sessions[key] === session,
                      !session.isFullPlayerPresented else { return }
                self.stopSession(for: key)
            }
        }
        pendingReleases[key] = task
    }

    #if os(macOS)
    /// Selects the session rendered by the app's single detached player
    /// window. Reusing one scene prevents repeated mini-player taps from
    /// creating duplicate player windows.
    func presentPlayerWindow(for session: PlayerSession) {
        if let currentKey = presentedPlayerKey,
           currentKey != session.key,
           let currentSession = sessions[currentKey] {
            currentSession.isFullPlayerPresented = false
            if !currentSession.isHostPresented {
                release(currentSession)
            }
        }
        pendingReleases[session.key]?.cancel()
        pendingReleases[session.key] = nil
        session.isFullPlayerPresented = true
        presentedPlayerKey = session.key
    }

    /// Clears the detached window only when it is still showing this session;
    /// an old window-content disappearance must not clear a newly selected
    /// player during a session switch.
    func dismissPlayerWindow(for session: PlayerSession) {
        guard presentedPlayerKey == session.key else { return }
        presentedPlayerKey = nil
        session.isFullPlayerPresented = false
        if !session.isHostPresented {
            release(session)
        }
    }
    #endif

    func stopSession(for key: Key) {
        pendingReleases[key]?.cancel()
        pendingReleases[key] = nil
        #if os(macOS)
        if presentedPlayerKey == key {
            presentedPlayerKey = nil
        }
        #endif
        guard let session = sessions.removeValue(forKey: key) else { return }
        session.model.stop()
    }

    func stopAll() {
        pendingReleases.values.forEach { $0.cancel() }
        pendingReleases.removeAll()
        let active = sessions
        sessions.removeAll()
        #if os(macOS)
        presentedPlayerKey = nil
        #endif
        active.values.forEach { $0.model.stop() }
    }
}
