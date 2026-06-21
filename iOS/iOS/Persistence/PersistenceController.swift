import Foundation
import SwiftData

/// Builds the shared SwiftData container with CloudKit sync enabled, so a
/// user's discussions and transcripts follow them across devices. Falls back to
/// a local-only store if CloudKit isn't available (e.g. no iCloud account in the
/// simulator) so the app still works.
enum PersistenceController {
    static let schema = Schema([
        Discussion.self,
        Person.self,
        SourceRef.self,
        TranscriptLine.self,
    ])

    static func makeContainer() -> ModelContainer {
        let cloud = ModelConfiguration(
            schema: schema,
            isStoredInMemoryOnly: false,
            cloudKitDatabase: .automatic
        )
        if let container = try? ModelContainer(for: schema, configurations: [cloud]) {
            return container
        }
        // CloudKit unavailable — fall back to a local store.
        let local = ModelConfiguration(schema: schema, isStoredInMemoryOnly: false, cloudKitDatabase: .none)
        if let container = try? ModelContainer(for: schema, configurations: [local]) {
            return container
        }
        // Last resort: in-memory so the app launches rather than crashing.
        let mem = ModelConfiguration(schema: schema, isStoredInMemoryOnly: true)
        // swiftlint:disable:next force_try
        return try! ModelContainer(for: schema, configurations: [mem])
    }
}
