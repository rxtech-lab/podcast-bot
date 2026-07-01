import SwiftUI

struct PlanSnapshot {
    let title: String
    let topic: String
    let isAudioBook: Bool
    let style: String
    let background: String
    let chapters: [PlanChapterSnapshot]
    let people: [PlanPersonSnapshot]
    let sources: [PlanSourceSnapshot]

    init(discussion: Discussion) {
        title = discussion.title
        topic = discussion.topic
        isAudioBook = discussion.script?.type == "audio-book"
        style = PlanSnapshot.displayStyle(for: discussion.script)
        background = PlanSnapshot.displayBackground(for: discussion.script)
        chapters = PlanSnapshot.displayChapters(for: discussion.script)
        people = PlanSnapshot.displayPeople(for: discussion.script)
        sources = discussion.sortedSources
    }

    /// Builds a snapshot from a persisted plan edit-turn (the plan as it stood at
    /// that point in the chat). `topic` carries over from the owning discussion,
    /// which a per-turn snapshot doesn't store.
    init(turn: DiscussionEditTurnDTO, topic: String) {
        self.title = turn.script?.title ?? ""
        self.topic = topic
        self.isAudioBook = turn.script?.type == "audio-book"
        self.style = PlanSnapshot.displayStyle(for: turn.script)
        self.background = PlanSnapshot.displayBackground(for: turn.script)
        self.chapters = PlanSnapshot.displayChapters(for: turn.script)
        self.people = PlanSnapshot.displayPeople(for: turn.script)
        self.sources = (turn.sources ?? turn.script?.sources ?? []).map {
            PlanSourceSnapshot(
                title: $0.title,
                urlString: $0.url,
                snippet: $0.snippet ?? "",
                markdown: $0.markdown ?? ""
            )
        }
    }

    private static func displayBackground(for script: ScriptDTO?) -> String {
        guard let script else { return "" }
        if script.type == "audio-book" {
            let summary = script.background ?? ""
            return summary
        }
        return script.background ?? ""
    }

    private static func displayStyle(for script: ScriptDTO?) -> String {
        guard script?.type == "audio-book" else { return "" }
        let raw = script?.audioBookStyle?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !raw.isEmpty else { return "" }
        return raw
            .split(separator: "-")
            .map { $0.prefix(1).uppercased() + $0.dropFirst() }
            .joined(separator: " ")
    }

    private static func displayChapters(for script: ScriptDTO?) -> [PlanChapterSnapshot] {
        guard script?.type == "audio-book" else { return [] }
        return (script?.audioBookChapters ?? []).enumerated().compactMap { idx, chapter in
            let title = chapter.title.trimmingCharacters(in: .whitespacesAndNewlines)
            let summary = chapter.summary.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !title.isEmpty || !summary.isEmpty else { return nil }
            return PlanChapterSnapshot(number: idx + 1, title: title, summary: summary)
        }
    }

    private static func displayPeople(for script: ScriptDTO?) -> [PlanPersonSnapshot] {
        var people: [PlanPersonSnapshot] = []
        if script?.type == "audio-book" {
            var seenNames = Set<String>()
            if let host = script?.audioBookHost, !host.name.isEmpty {
                people.append(PlanPersonSnapshot(name: host.name, aspect: "Narrator", isHost: true))
                seenNames.insert(normalizedPersonName(host.name))
            }
            for speaker in script?.audioBookSpeakers ?? [] {
                let key = normalizedPersonName(speaker.name)
                guard !key.isEmpty, !seenNames.contains(key) else { continue }
                people.append(PlanPersonSnapshot(
                    name: speaker.name,
                    aspect: speaker.description ?? speaker.gender ?? "",
                    isHost: false
                ))
                seenNames.insert(key)
            }
            return people
        }
        if let host = script?.host, !host.name.isEmpty {
            people.append(PlanPersonSnapshot(name: host.name, aspect: "Moderator", isHost: true))
        }
        people.append(contentsOf: (script?.discussants ?? []).map {
            PlanPersonSnapshot(name: $0.name, aspect: $0.aspect ?? "", isHost: false)
        })
        return people
    }

    private static func normalizedPersonName(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
    }
}

struct PlanChapterSnapshot: Identifiable, Hashable {
    let number: Int
    let title: String
    let summary: String

    var id: Int { number }
}

struct PlanChaptersPresentation: Identifiable {
    let id = UUID()
    let title: String
    let chapters: [PlanChapterSnapshot]
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
    var onChaptersTapped: (() -> Void)? = nil
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

            if snapshot.isAudioBook {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Style")
                        .font(.headline)
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: "slider.horizontal.3")
                            .foregroundStyle(Theme.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(snapshot.style.isEmpty ? "Not selected" : snapshot.style)
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(.primary)
                            Text(styleDetailText)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                }
            }

            if !snapshot.background.isEmpty {
                MarkdownText(snapshot.background)
                    .font(.body)
                    .foregroundStyle(Theme.secondaryText)
            }

            if !snapshot.chapters.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Chapters")
                        .font(.headline)
                    if let onChaptersTapped {
                        Button(action: onChaptersTapped) {
                            chaptersSentence
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("plan.chapters")
                    } else {
                        chaptersSentence
                    }
                }
            }

            if !snapshot.people.isEmpty {
                VStack(alignment: .leading, spacing: 10) {
                    HStack {
                        Text(snapshot.chapters.isEmpty ? "Panelists" : "Voices").font(.headline)
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

    private var chaptersSentence: some View {
        HStack(spacing: 8) {
            Image(systemName: "book.closed")
                .foregroundStyle(Theme.accent)
            Text(chapterSentenceText)
                .font(.subheadline)
                .foregroundStyle(Theme.secondaryText)
            if onChaptersTapped != nil {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
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

    private var chapterSentenceText: String {
        let count = snapshot.chapters.count
        return "\(count) chapter section\(count == 1 ? "" : "s") in this audiobook plan."
    }

    private var styleDetailText: String {
        switch snapshot.style.lowercased() {
        case "news":
            return "A main presenter leads with supporting voices."
        case "conversational":
            return "One main speaker leads while others ask, clarify, or respond."
        case "podcast":
            return "A host-led podcast format with supporting speakers."
        case "meeting":
            return "A facilitator guides participant questions and discussion."
        case "audiobook":
            return "A classic narrator-led audiobook format."
        default:
            return "The agent should choose the production format for this audiobook."
        }
    }

    private var sourceSentenceText: String {
        let count = snapshot.sources.count
        return "Found \(count) source\(count == 1 ? "" : "s") for this plan."
    }
}

struct AudioBookChaptersSheet: View {
    @Environment(\.dismiss) private var dismiss

    let presentation: PlanChaptersPresentation

    var body: some View {
        NavigationStack {
            List {
                ForEach(presentation.chapters) { chapter in
                    VStack(alignment: .leading, spacing: 8) {
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text("\(chapter.number)")
                                .font(.caption.weight(.bold))
                                .foregroundStyle(.white)
                                .frame(width: 24, height: 24)
                                .background(Theme.accent, in: .circle)
                            Text(chapter.title)
                                .font(.body.weight(.semibold))
                                .foregroundStyle(.primary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if !chapter.summary.isEmpty {
                            Text(chapter.summary)
                                .font(.subheadline)
                                .foregroundStyle(Theme.secondaryText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(.vertical, 6)
                }
            }
            .navigationTitle(presentation.title.isEmpty ? "Chapters" : presentation.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }
}
