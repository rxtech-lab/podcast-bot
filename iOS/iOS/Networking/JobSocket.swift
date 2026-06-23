import Foundation

/// Streams a running job's live events (transcript, agent activity, phase, tick)
/// over the engine's per-job WebSocket, and lets the user inject participation
/// messages. Uses URLSessionWebSocketTask with a bearer Authorization header
/// (native tasks can set headers, unlike browsers).
final class JobSocket: @unchecked Sendable {
    private let api: APIClient
    private let jobID: String
    private var task: URLSessionWebSocketTask?
    /// Set by `close()` to permanently stop the reconnect loop. Without it,
    /// cancelling the underlying `task` only ends the current connection and the
    /// loop would immediately reconnect — so a caller closing a finished job's
    /// socket could never actually stop it.
    private var closed = false

    /// Synthetic event yielded after the socket drops and is about to be
    /// re-opened. The consumer (PlayerModel) treats it as a cue to re-fetch the
    /// job transcript and backfill any lines that streamed while the connection
    /// was down — e.g. while the app was backgrounded and iOS tore the socket
    /// down. It is not a server event, so it carries no `data`.
    static let reconnectEvent = "__reconnected__"

    init(api: APIClient, jobID: String) {
        self.api = api
        self.jobID = jobID
    }

    /// Opens the socket and yields decoded events, transparently reconnecting if
    /// the connection drops while the job is still live. Cancelling the consuming
    /// task (which terminates the stream) tears the socket down for good.
    func events() -> AsyncStream<JobEventEnvelope> {
        AsyncStream { continuation in
            let consumer = Task { [weak self] in
                guard let self else { continuation.finish(); return }
                var failures = 0
                while !Task.isCancelled && !self.closed {
                    let connected = await self.openAndStream(continuation: continuation)
                    if Task.isCancelled || self.closed { break }
                    // The socket closed but the consumer didn't cancel — the job
                    // may still be producing events. Tell the model to backfill,
                    // then back off (capped) before re-opening. A clean spell of
                    // streaming resets the backoff so a fresh drop reconnects fast.
                    failures = connected ? 0 : failures + 1
                    continuation.yield(JobEventEnvelope(event: Self.reconnectEvent, data: nil))
                    let backoffMS = min(5_000, 250 * (1 << min(failures, 4)))
                    try? await Task.sleep(nanoseconds: UInt64(backoffMS) * 1_000_000)
                }
                continuation.finish()
            }
            continuation.onTermination = { [weak self] _ in
                consumer.cancel()
                self?.close()
            }
        }
    }

    /// Opens one socket and pumps decoded events into `continuation` until it
    /// errors or the consuming task is cancelled. Returns whether the connection
    /// ever delivered a message, so the caller can scale the reconnect backoff
    /// (a connection that never came up shouldn't reconnect as aggressively).
    private func openAndStream(continuation: AsyncStream<JobEventEnvelope>.Continuation) async -> Bool {
        let token = await api.currentToken()
        var req = URLRequest(url: api.webSocketURL(jobID: jobID))
        req.setValue(AcceptLanguage.headerValue, forHTTPHeaderField: "Accept-Language")
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let task = URLSession.shared.webSocketTask(with: req)
        self.task = task
        task.resume()
        var received = false
        while !Task.isCancelled && !closed {
            do {
                let message = try await task.receive()
                received = true
                switch message {
                case let .string(text):
                    if let env = Self.decode(text) { continuation.yield(env) }
                case let .data(data):
                    if let env = Self.decode(String(decoding: data, as: UTF8.self)) {
                        continuation.yield(env)
                    }
                @unknown default:
                    break
                }
            } catch {
                break
            }
        }
        task.cancel(with: .goingAway, reason: nil)
        return received
    }

    /// Pushes a participant message into the running discussion.
    func send(text: String, username: String) async {
        struct Inbound: Encodable { let type = "message"; let text: String; let username: String }
        guard let data = try? JSONEncoder().encode(Inbound(text: text, username: username)),
              let json = String(data: data, encoding: .utf8) else { return }
        try? await task?.send(.string(json))
    }

    func close() {
        closed = true
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
    }

    private static func decode(_ text: String) -> JobEventEnvelope? {
        guard let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(JobEventEnvelope.self, from: data)
    }
}
