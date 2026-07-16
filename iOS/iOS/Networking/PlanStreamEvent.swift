import Foundation

/// Events surfaced by a streaming plan endpoint. The terminal `done` carries the
/// persisted discussion; `error` carries a human-readable message.
enum PlanStreamEvent: Sendable {
    case progress(PlanProgressEvent)
    case done(Discussion)
    case failed(String)
}
