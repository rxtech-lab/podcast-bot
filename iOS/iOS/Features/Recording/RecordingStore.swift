import AVFoundation
import Foundation
import SwiftUI

/// Local library of on-device recordings. Audio files live in
/// `Application Support/Recordings/` (never evicted, unlike Caches) alongside an
/// atomically written `index.json`. The recorder writes straight into this
/// store's destination URL, so a recording is durable from its first buffer.
@MainActor
@Observable
final class RecordingStore {
    static let shared = RecordingStore()

    struct Recording: Codable, Identifiable, Hashable {
        var id: UUID
        var title: String
        /// File name relative to the recordings directory. The absolute URL is
        /// rebuilt each launch because the app container path can change.
        var filename: String
        var duration: TimeInterval
        var createdAt: Date
        var sizeBytes: Int64
        /// Discussion created from this recording via the upload flow, if any.
        var uploadedDiscussionID: String?
        /// False while the recorder is still writing; flipped on finish. Entries
        /// left unfinalized (app killed mid-recording) are adopted or purged on
        /// the next launch.
        var finalized: Bool
    }

    /// Newest first.
    private(set) var recordings: [Recording] = []

    private var loaded = false

    private init() {}

    // MARK: - Loading

    func loadIfNeeded() {
        guard !loaded else { return }
        loaded = true
        guard let data = try? Data(contentsOf: indexURL),
              let decoded = try? Self.decoder.decode([Recording].self, from: data) else {
            recordings = []
            return
        }
        recordings = sweep(decoded).sorted { $0.createdAt > $1.createdAt }
        saveIndex()
    }

    /// Drops entries whose file vanished and settles unfinalized entries left by
    /// an app kill: adopt them if the file is still playable, delete otherwise.
    /// (An m4a interrupted before its header is finalized may be unreadable.)
    private func sweep(_ entries: [Recording]) -> [Recording] {
        var kept: [Recording] = []
        for var entry in entries {
            let url = url(for: entry)
            guard FileManager.default.fileExists(atPath: url.path) else { continue }
            if !entry.finalized {
                guard let file = try? AVAudioFile(forReading: url),
                      file.processingFormat.sampleRate > 0 else {
                    try? FileManager.default.removeItem(at: url)
                    continue
                }
                let duration = Double(file.length) / file.processingFormat.sampleRate
                guard duration > 0 else {
                    try? FileManager.default.removeItem(at: url)
                    continue
                }
                entry.duration = duration
                entry.sizeBytes = fileSize(at: url)
                entry.finalized = true
            }
            kept.append(entry)
        }
        return kept
    }

    // MARK: - Mutations

    /// Registers a new, still-recording entry and returns it. The recorder
    /// should write to `url(for:)` of the returned entry.
    func createPending(title: String? = nil) -> Recording {
        loadIfNeeded()
        let id = UUID()
        let recording = Recording(
            id: id,
            title: title ?? Self.defaultTitle(),
            filename: "\(id.uuidString).m4a",
            duration: 0,
            createdAt: Date(),
            sizeBytes: 0,
            uploadedDiscussionID: nil,
            finalized: false
        )
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        recordings.insert(recording, at: 0)
        saveIndex()
        return recording
    }

    func finalize(id: UUID, duration: TimeInterval) {
        update(id: id) { entry in
            entry.duration = duration
            entry.sizeBytes = self.fileSize(at: self.url(for: entry))
            entry.finalized = true
        }
    }

    func rename(id: UUID, to title: String) {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        update(id: id) { $0.title = trimmed }
    }

    func setUploadedDiscussion(id: UUID, discussionID: String) {
        update(id: id) { $0.uploadedDiscussionID = discussionID }
    }

    func delete(id: UUID) {
        guard let index = recordings.firstIndex(where: { $0.id == id }) else { return }
        let entry = recordings.remove(at: index)
        try? FileManager.default.removeItem(at: url(for: entry))
        saveIndex()
    }

    // MARK: - Paths

    func url(for recording: Recording) -> URL {
        directory.appendingPathComponent(recording.filename)
    }

    private var directory: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.temporaryDirectory
        return base.appendingPathComponent("Recordings", isDirectory: true)
    }

    private var indexURL: URL { directory.appendingPathComponent("index.json") }

    // MARK: - Helpers

    static func defaultTitle(date: Date = Date()) -> String {
        let stamp = date.formatted(date: .abbreviated, time: .shortened)
        return String(localized: "Recording \(stamp)",
                      comment: "Default title for a new audio recording; parameter is the date/time")
    }

    private func update(id: UUID, _ mutate: (inout Recording) -> Void) {
        guard let index = recordings.firstIndex(where: { $0.id == id }) else { return }
        mutate(&recordings[index])
        saveIndex()
    }

    private func saveIndex() {
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        guard let data = try? Self.encoder.encode(recordings) else { return }
        try? data.write(to: indexURL, options: .atomic)
    }

    private func fileSize(at url: URL) -> Int64 {
        let attributes = try? FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes?[.size] as? NSNumber)?.int64Value ?? 0
    }

    private static let encoder: JSONEncoder = {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }()

    private static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }()
}
