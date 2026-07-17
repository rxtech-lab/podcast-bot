import SwiftUI

/// Grouped global semantic-search results: one section per podcast, each with
/// its best-matching passages (original chunk text + similarity score).
struct SemanticSearchResultsView: View {
    let groups: [SemanticSearchGroup]
    var onSelect: (Discussion) -> Void

    var body: some View {
        List {
            ForEach(groups) { group in
                Section {
                    Button {
                        onSelect(group.discussion)
                    } label: {
                        DiscussionRow(discussion: group.discussion, isSelected: false)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("search.result.\(group.discussion.id)")

                    ForEach(group.matches) { match in
                        Button {
                            onSelect(group.discussion)
                        } label: {
                            SemanticMatchRow(match: match)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .listRowInsets(.init(top: 4, leading: 16, bottom: 4, trailing: 16))
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .scrollDismissesKeyboard(.interactively)
    }
}

/// One matched passage: kind icon, the original chunk text, anchoring info,
/// and the similarity score badge.
struct SemanticMatchRow: View {
    let match: SemanticMatch

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: match.kind == "source" ? "link" : "waveform")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 18)
                .padding(.top, 2)
            VStack(alignment: .leading, spacing: 4) {
                Text(match.text)
                    .font(.footnote)
                    .foregroundStyle(.primary)
                    .lineLimit(3)
                    .multilineTextAlignment(.leading)
                HStack(spacing: 6) {
                    Text(anchorText)
                        .font(.caption2)
                        .foregroundStyle(Theme.secondaryText)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    similarityBadge
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.agentBubble.opacity(0.55), in: .rect(cornerRadius: 14))
    }

    private var anchorText: String {
        if match.kind == "source" {
            let title = match.sourceTitle ?? ""
            return title.isEmpty
                ? String(localized: "Source", comment: "Search match anchor label for a source document passage")
                : title
        }
        var pieces: [String] = []
        if let speakers = match.speakers, !speakers.isEmpty {
            pieces.append(speakers.joined(separator: ", "))
        }
        if let start = match.startMS {
            pieces.append(formatMS(start))
        }
        return pieces.isEmpty
            ? String(localized: "Transcript", comment: "Search match anchor label for a transcript passage")
            : pieces.joined(separator: " · ")
    }

    private var similarityBadge: some View {
        Text("\(Int((match.similarity * 100).rounded()))%")
            .font(.caption2.weight(.semibold))
            .foregroundStyle(Theme.accent)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(Theme.accent.opacity(0.12), in: .capsule)
            .accessibilityLabel(String(localized: "\(Int((match.similarity * 100).rounded())) percent match",
                                       comment: "Accessibility label for a search similarity score"))
    }

    private func formatMS(_ ms: Int64) -> String {
        let total = ms / 1000
        return String(format: "%d:%02d", total / 60, total % 60)
    }
}
