import Foundation

/// Streams a running job's live events (transcript, agent activity, phase, tick)
/// over the engine's per-job WebSocket, and lets the user inject participation
/// messages. Uses URLSessionWebSocketTask with a bearer Authorization header
/// (native tasks can set headers, unlike browsers).
final class JobSocket: @unchecked Sendable {
    private let api: APIClient
    private let jobID: String
    private var task: URLSessionWebSocketTask?

    init(api: APIClient, jobID: String) {
        self.api = api
        self.jobID = jobID
    }

    /// Opens the socket and yields decoded events until it closes. Cancelling
    /// the consuming task tears the socket down.
    func events() -> AsyncStream<JobEventEnvelope> {
        AsyncStream { continuation in
            Task {
                let token = await api.currentToken()
                var req = URLRequest(url: api.webSocketURL(jobID: jobID))
                if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
                let task = URLSession.shared.webSocketTask(with: req)
                self.task = task
                task.resume()
                await receiveLoop(task: task, continuation: continuation)
            }
            continuation.onTermination = { [weak self] _ in
                self?.task?.cancel(with: .goingAway, reason: nil)
            }
        }
    }

    private func receiveLoop(task: URLSessionWebSocketTask,
                             continuation: AsyncStream<JobEventEnvelope>.Continuation) async {
        while !Task.isCancelled {
            do {
                let message = try await task.receive()
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
                continuation.finish()
                return
            }
        }
        continuation.finish()
    }

    /// Pushes a participant message into the running discussion.
    func send(text: String, username: String) async {
        struct Inbound: Encodable { let type = "message"; let text: String; let username: String }
        guard let data = try? JSONEncoder().encode(Inbound(text: text, username: username)),
              let json = String(data: data, encoding: .utf8) else { return }
        try? await task?.send(.string(json))
    }

    func close() {
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
    }

    private static func decode(_ text: String) -> JobEventEnvelope? {
        guard let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(JobEventEnvelope.self, from: data)
    }
}
