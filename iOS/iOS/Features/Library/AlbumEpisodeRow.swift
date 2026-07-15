import Kingfisher
import SwiftUI

struct AlbumEpisodeRow: View {
    let episode: Discussion
    let number: Int

    var body: some View {
        HStack(spacing: 12) {
            Text("\(number)")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
                .frame(width: 26)
            DiscussionCoverThumbnail(discussion: episode, size: 44)
                .accessibilityIdentifier("album.episode.cover.\(episode.id)")
            VStack(alignment: .leading, spacing: 3) {
                Text(episode.displayTitle)
                    .font(.body.weight(.medium))
                    .lineLimit(2)
                Text(subtitle)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer(minLength: 0)
            trailing
        }
        .padding(.vertical, 4)
    }

    @ViewBuilder
    private var trailing: some View {
        switch episode.status {
        case .generating:
            ProgressView().controlSize(.small).tint(Theme.accent)
        case .ready:
            Image(systemName: "play.circle")
                .font(.title3)
                .foregroundStyle(Theme.accent)
        case .planning:
            Image(systemName: "pencil.and.list.clipboard")
                .foregroundStyle(Theme.secondaryText)
        case .failed:
            Image(systemName: "exclamationmark.triangle")
                .foregroundStyle(.orange)
        }
    }

    private var subtitle: String {
        var parts: [String] = []
        if let range = chapterRangeLabel {
            parts.append(range)
        }
        switch episode.status {
        case .generating:
            parts.append(String(localized: "Generating…"))
        case .planning:
            parts.append(String(localized: "Planning"))
        case .failed:
            parts.append(String(localized: "Failed"))
        case .ready:
            if let duration = durationLabel {
                parts.append(duration)
            }
        }
        return parts.isEmpty ? String(localized: "Episode") : parts.joined(separator: " · ")
    }

    /// "Chapters 6-8" for audiobook batch episodes, from the recorded global
    /// chapter indices.
    private var chapterRangeLabel: String? {
        guard let indices = episode.script?.audioBookChapterIndices, !indices.isEmpty else { return nil }
        let sorted = indices.sorted()
        if sorted.count == 1 { return String(localized: "Chapter \(sorted[0])") }
        let contiguous = zip(sorted, sorted.dropFirst()).allSatisfy { $1 == $0 + 1 }
        if contiguous { return String(localized: "Chapters \(sorted.first!)-\(sorted.last!)") }
        return String(localized: "Chapters \(sorted.map(String.init).joined(separator: ", "))")
    }

    private var durationLabel: String? {
        guard let seconds = episode.durationSeconds, seconds > 0 else { return nil }
        let total = Int(seconds.rounded())
        let minutes = total / 60
        let remainder = total % 60
        return String(format: "%d:%02d", minutes, remainder)
    }
}

/// Renders an album cover (image, gradient, or waveform fallback) — the album
/// counterpart of `DiscussionCoverThumbnail`.


