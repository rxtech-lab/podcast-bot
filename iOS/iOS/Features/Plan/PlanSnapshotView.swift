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
    let id = UUID()
    let title: String
    let urlString: String
    let snippet: String

    init(title: String, urlString: String, snippet: String) {
        self.title = title
        self.urlString = urlString
        self.snippet = snippet
    }

    var url: URL? { URL(string: urlString) }
}

struct PlanSnapshotCard: View {
    let label: String
    let snapshot: PlanSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 6) {
                Text(label.uppercased())
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(Theme.accent)
                if !snapshot.title.isEmpty {
                    Text(snapshot.title)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(.white)
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
                    Text("Panelists").font(.headline)
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
                VStack(alignment: .leading, spacing: 8) {
                    Text("Sources").font(.headline)
                    ForEach(snapshot.sources) { source in
                        sourceRow(source)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func sourceRow(_ source: PlanSourceSnapshot) -> some View {
        if let url = source.url {
            Link(destination: url) {
                sourceContent(source)
            }
        } else {
            sourceContent(source)
        }
    }

    private func sourceContent(_ source: PlanSourceSnapshot) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(source.title.isEmpty ? source.urlString : source.title)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.white)
            if !source.snippet.isEmpty {
                Text(source.snippet)
                    .font(.caption)
                    .foregroundStyle(Theme.secondaryText)
                    .lineLimit(3)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(Color.white.opacity(0.05), in: .rect(cornerRadius: 14))
    }
}
