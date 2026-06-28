import SwiftUI

struct PlanSnapshot {
    let title: String
    let topic: String
    let background: String
    let people: [PlanPersonSnapshot]
    let sources: [PlanSourceSnapshot]

    init(discussion: Discussion) {
        title = discussion.title
        topic = discussion.topic
        background = discussion.script?.background ?? ""
        people = discussion.sortedPeople
        sources = discussion.sortedSources
    }

    /// Builds a snapshot from a persisted plan edit-turn (the plan as it stood at
    /// that point in the chat). `topic` carries over from the owning discussion,
    /// which a per-turn snapshot doesn't store.
    init(turn: DiscussionEditTurnDTO, topic: String) {
        self.title = turn.script?.title ?? ""
        self.topic = topic
        self.background = turn.script?.background ?? ""
        var people: [PlanPersonSnapshot] = []
        if let host = turn.script?.host, !host.name.isEmpty {
            people.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        people.append(contentsOf: (turn.script?.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        self.people = people
        self.sources = (turn.sources ?? turn.script?.sources ?? []).map {
            PlanSourceSnapshot(
                title: $0.title,
                urlString: $0.url,
                snippet: $0.snippet ?? "",
                markdown: $0.markdown ?? ""
            )
        }
    }
}

struct PlanPersonSnapshot: Identifiable {
    let id = UUID()
    let name: String
    let aspect: String
    let isHost: Bool

    init(name: String, aspect: String, isHost: Bool) {
        self.name = name
        self.aspect = aspect
        self.isHost = isHost
    }
}

struct PlanSourceSnapshot: Identifiable {
    var id: String { urlString.isEmpty ? title : urlString }
    let title: String
    let urlString: String
    let snippet: String
    let markdown: String

    init(title: String, urlString: String, snippet: String, markdown: String = "") {
        self.title = title
        self.urlString = urlString
        self.snippet = snippet
        self.markdown = markdown
    }

    var url: URL? { URL(string: urlString) }
    var displayTitle: String { title.isEmpty ? urlString : title }
    var detailMarkdown: String {
        let content = markdown.trimmingCharacters(in: .whitespacesAndNewlines)
        if !content.isEmpty { return content }
        return snippet.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

struct PlanSnapshotCard: View {
    let label: String
    let snapshot: PlanSnapshot
    var onSourcesTapped: (() -> Void)? = nil
    /// When set, a "Models" button is shown in the Panelists header that opens
    /// the per-speaker model editor. nil hides it (e.g. read-only previews).
    var onEditModels: (() -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 6) {
                Text(label.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                if !snapshot.title.isEmpty {
                    Text(snapshot.title)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(.primary)
                }
                if !snapshot.topic.isEmpty {
                    Text("Topic: \(snapshot.topic)")
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }

            if !snapshot.background.isEmpty {
                MarkdownText(snapshot.background)
                    .font(.body)
                    .foregroundStyle(Theme.secondaryText)
            }

            if !snapshot.people.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        Text("Panelists").font(.headline)
                        if let onEditModels {
                            Spacer()
                            Button(action: onEditModels) {
                                Label("Models", systemImage: "cpu")
                                    .font(.subheadline.weight(.semibold))
                            }
                            .buttonStyle(.plain)
                            .foregroundStyle(Theme.accent)
                            .accessibilityIdentifier("plan.editModels")
                        }
                    }
                    ForEach(snapshot.people) { person in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 8) {
                                Image(systemName: person.isHost ? "person.wave.2.fill" : "person.fill")
                                    .foregroundStyle(Theme.accent)
                                    .frame(width: 20)
                                Text(person.name)
                                    .font(.body.weight(.semibold))
                            }
                            if !person.aspect.isEmpty {
                                Text(person.aspect)
                                    .font(.subheadline)
                                    .foregroundStyle(Theme.secondaryText)
                                    .padding(.leading, 28)
                            }
                        }
                    }
                }
            }

            if !snapshot.sources.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Sources")
                        .font(.headline)
                    if let onSourcesTapped {
                        Button(action: onSourcesTapped) {
                            sourcesSentence
                        }
                        .buttonStyle(.plain)
                    } else {
                        sourcesSentence
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var sourcesSentence: some View {
        HStack(spacing: 8) {
            Image(systemName: "doc.text.magnifyingglass")
                .foregroundStyle(Theme.accent)
            Text(sourceSentenceText)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
            if onSourcesTapped != nil {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
    }

    private var sourceSentenceText: String {
        let count = snapshot.sources.count
        return "Found \(count) source\(count == 1 ? "" : "s") for this plan."
    }
}
