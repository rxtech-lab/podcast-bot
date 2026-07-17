import SwiftUI

// MARK: - Podcast card (show_podcast)

/// Tappable podcast card the global chat agent shows when its answer
/// references a specific podcast.
struct QAPodcastCardView: View {
    let podcast: QAPodcastCard
    var onTap: () -> Void = {}

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 12) {
                StationCoverArt(cover: podcast.cover, title: podcast.title)
                    .frame(width: 52, height: 52)
                VStack(alignment: .leading, spacing: 3) {
                    Text(podcast.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(2)
                    HStack(spacing: 6) {
                        if let duration = podcast.durationSeconds, duration > 0 {
                            Label(formattedDuration(duration), systemImage: "clock")
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                        if let topic = podcast.topic, !topic.isEmpty {
                            Text(topic)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                                .lineLimit(1)
                        }
                    }
                }
                Spacer(minLength: 6)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Theme.secondaryText.opacity(0.7))
            }
            .padding(12)
            .frame(maxWidth: 300, alignment: .leading)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .strokeBorder(Theme.accent.opacity(0.18), lineWidth: 1)
            )
        }
        .buttonStyle(PlanningToolCardButtonStyle())
        .accessibilityIdentifier("qa.card.podcast.\(podcast.id)")
    }

    private func formattedDuration(_ seconds: Double) -> String {
        let total = Int(seconds)
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}

// MARK: - Podcast grid (display_podcasts)

/// One batch tool result rendered as an adaptive cover-art grid. Each cell
/// remains independently tappable even though all podcasts share one card.
struct QAPodcastGridView: View {
    let podcasts: [QAPodcastCard]
    var onTap: (String) -> Void = { _ in }

    private let columns = [GridItem(.adaptive(minimum: 126), spacing: 12, alignment: .top)]

    var body: some View {
        LazyVGrid(columns: columns, alignment: .leading, spacing: 14) {
            ForEach(podcasts, id: \.id) { podcast in
                Button { onTap(podcast.id) } label: {
                    VStack(alignment: .leading, spacing: 7) {
                        StationCoverArt(cover: podcast.cover, title: podcast.title)
                            .aspectRatio(1, contentMode: .fit)
                        Text(podcast.title)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.primary)
                            .lineLimit(2)
                        if let topic = podcast.topic, !topic.isEmpty {
                            Text(topic)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                                .lineLimit(2)
                        } else if let duration = podcast.durationSeconds, duration > 0 {
                            Label(formatDuration(duration), systemImage: "clock")
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("qa.card.podcast.\(podcast.id)")
            }
        }
        .padding(12)
        .frame(maxWidth: 360, alignment: .leading)
        .background(Theme.agentBubble, in: .rect(cornerRadius: 18))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .strokeBorder(Theme.accent.opacity(0.16), lineWidth: 1)
        )
        .accessibilityIdentifier("qa.card.podcasts")
    }
}

// MARK: - Podcast highlights (show_podcasts / show_highlight_lines)

/// Multiple podcasts and their canonical transcript quotes, grouped inside a
/// single presentation card so the agent needs only one tool call.
struct QAPodcastHighlightsView: View {
    let groups: [QAPodcastHighlightGroup]
    var onTapPodcast: (String) -> Void = { _ in }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(groups.enumerated()), id: \.offset) { index, group in
                if index > 0 {
                    Divider()
                        .overlay(Theme.secondaryText.opacity(0.14))
                        .padding(.vertical, 12)
                }
                podcastHeader(group.podcast)
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(Array(group.lines.enumerated()), id: \.offset) { _, line in
                        highlightLine(line)
                    }
                }
                .padding(.top, group.lines.isEmpty ? 0 : 12)
            }
        }
        .padding(14)
        .frame(maxWidth: 360, alignment: .leading)
        .background(Theme.agentBubble, in: .rect(cornerRadius: 18))
        .overlay(
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .strokeBorder(Theme.accent.opacity(0.16), lineWidth: 1)
        )
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("qa.card.highlights")
    }

    func podcastHeader(_ podcast: QAPodcastCard) -> some View {
        Button { onTapPodcast(podcast.id) } label: {
            HStack(spacing: 10) {
                StationCoverArt(cover: podcast.cover, title: podcast.title)
                    .frame(width: 48, height: 48)
                VStack(alignment: .leading, spacing: 3) {
                    Text(podcast.title)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .lineLimit(2)
                    if let topic = podcast.topic, !topic.isEmpty {
                        Text(topic)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(1)
                    }
                }
                Spacer(minLength: 6)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Theme.secondaryText.opacity(0.7))
            }
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("qa.card.podcast.\(podcast.id)")
    }

    func highlightLine(_ line: QATranscriptLine) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "quote.opening")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 5) {
                Text(line.text)
                    .font(.callout)
                    .foregroundStyle(.primary)
                    .textSelection(.enabled)
                Text("\(line.speaker) · \(formatMS(line.startMS))")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("qa.card.highlight-line")
    }
}

// MARK: - Transcript card (show_transcript)

/// Compact transcript excerpt; tapping opens the full excerpt sheet.
struct QATranscriptCardView: View {
    let transcript: QATranscriptCard
    var onTap: () -> Void = {}

    private var previewLines: [QATranscriptLine] {
        Array(transcript.lines.prefix(4))
    }

    var body: some View {
        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    Image(systemName: "text.quote")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.accent)
                    Text(String(localized: "Transcript · \(formatMS(transcript.startMS))–\(formatMS(transcript.endMS))",
                                comment: "Transcript excerpt card header; values are start/end timestamps"))
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.secondaryText)
                    Spacer(minLength: 4)
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(Theme.secondaryText.opacity(0.7))
                }
                ForEach(Array(previewLines.enumerated()), id: \.offset) { _, line in
                    VStack(alignment: .leading, spacing: 1) {
                        Text("\(line.speaker) · \(formatMS(line.startMS))")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(Theme.secondaryText)
                        Text(line.text)
                            .font(.footnote)
                            .foregroundStyle(.primary)
                            .lineLimit(2)
                    }
                }
                if transcript.lines.count > previewLines.count {
                    Text(String(localized: "+\(transcript.lines.count - previewLines.count) more lines",
                                comment: "Transcript excerpt card footer; value is the hidden line count"))
                        .font(.caption2)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            .padding(12)
            .frame(maxWidth: 300, alignment: .leading)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
        }
        .buttonStyle(PlanningToolCardButtonStyle())
        .accessibilityIdentifier("qa.card.transcript")
    }
}

/// Full transcript excerpt sheet behind a transcript card.
struct QATranscriptDetailSheet: View {
    let card: QATranscriptCard
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(Array(card.lines.enumerated()), id: \.offset) { _, line in
                        VStack(alignment: .leading, spacing: 2) {
                            Text("\(line.speaker) · \(formatMS(line.startMS))")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(Theme.secondaryText)
                            Text(line.text)
                                .font(.body)
                                .textSelection(.enabled)
                        }
                    }
                }
                .padding(20)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(Theme.background.ignoresSafeArea())
            .navigationTitle(card.title ?? String(localized: "Transcript", comment: "Transcript excerpt sheet title"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(String(localized: "Done", comment: "Dismiss the transcript excerpt sheet")) { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }
}

extension QATranscriptCard: Identifiable {
    var id: String { "\(discussionID)-\(startMS)-\(endMS)" }
}

// MARK: - Sources card (show_sources)

/// Source list card; each row opens the source URL.
struct QASourcesCardView: View {
    let sources: [QASourceCard]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Image(systemName: "link")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.accent)
                Text(sources.count == 1
                    ? String(localized: "1 source", comment: "Sources card header for a single source")
                    : String(localized: "\(sources.count) sources", comment: "Sources card header; value is the source count"))
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(.horizontal, 12)
            .padding(.top, 12)
            .padding(.bottom, 6)

            ForEach(Array(sources.enumerated()), id: \.offset) { index, source in
                if index > 0 {
                    Divider().overlay(Theme.secondaryText.opacity(0.12))
                }
                sourceRow(source)
            }
        }
        .frame(maxWidth: 300, alignment: .leading)
        .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
        .accessibilityIdentifier("qa.card.sources")
    }

    @ViewBuilder
    private func sourceRow(_ source: QASourceCard) -> some View {
        let content = VStack(alignment: .leading, spacing: 2) {
            Text(source.title.isEmpty ? source.url : source.title)
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.primary)
                .lineLimit(2)
            if let snippet = source.snippet, !snippet.isEmpty {
                Text(snippet)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(2)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)

        if let url = URL(string: source.url), !source.url.isEmpty {
            Link(destination: url) { content }
                .buttonStyle(.plain)
        } else {
            content
        }
    }
}

// MARK: - Generated documents (display_mindmap / display_ppt)

/// Tappable entry for a generated visual document. The chat keeps the payload
/// content-free and lets the existing mindmap/summary views fetch on demand.
struct QADocumentCardView: View {
    let document: QADocumentCard
    let kind: String
    var onTap: () -> Void = {}

    private var isMindmap: Bool { kind == "mindmap" }

    var body: some View {
        Button(action: onTap) {
            HStack(spacing: 12) {
                Image(systemName: isMindmap ? "brain.head.profile" : "rectangle.on.rectangle")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 40, height: 40)
                    .background(Theme.accent.opacity(0.12), in: .rect(cornerRadius: 10))
                VStack(alignment: .leading, spacing: 3) {
                    Text(isMindmap
                         ? String(localized: "Mindmap", comment: "Q&A generated mindmap card title")
                         : String(localized: "Presentation", comment: "Q&A generated PPT card title"))
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                    Text(document.title)
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(2)
                }
                Spacer(minLength: 6)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(Theme.secondaryText.opacity(0.7))
            }
            .padding(12)
            .frame(maxWidth: 300, alignment: .leading)
            .background(Theme.agentBubble, in: .rect(cornerRadius: 16))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .strokeBorder(Theme.accent.opacity(0.18), lineWidth: 1)
            )
        }
        .buttonStyle(PlanningToolCardButtonStyle())
        .accessibilityIdentifier("qa.card.\(kind).\(document.discussionID)")
    }
}

private func formatMS(_ ms: Int64) -> String {
    let total = ms / 1000
    return String(format: "%d:%02d", total / 60, total % 60)
}

private func formatDuration(_ seconds: Double) -> String {
    let total = Int(seconds)
    return String(format: "%d:%02d", total / 60, total % 60)
}
