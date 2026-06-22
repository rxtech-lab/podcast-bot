import SwiftUI

/// A tappable strip summarising how many external links the plan agent searched
/// ("N external links searched"). Tapping opens the sources sheet.
struct SourcesStrip: View {
    let count: Int

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(Theme.accent)
            Text(count == 0
                 ? "Add web sources"
                 : "\(count) external link\(count == 1 ? "" : "s") searched")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.white)
            Spacer()
            Image(systemName: "chevron.right")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
        }
        .padding(12)
        .glassEffect(in: .rect(cornerRadius: 16))
        .contentShape(.rect)
    }
}

/// Lists every source the plan cites and lets the user add a link. Saving a new
/// link asks the engine to research it and fold it into the plan; the updated
/// discussion is propagated via `onUpdated`.
struct SourcesSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    @State private var discussion: Discussion
    @State private var newLink = ""
    @State private var isResearching = false
    @State private var errorMessage: String?

    var onUpdated: (Discussion) -> Void

    init(discussion: Discussion, onUpdated: @escaping (Discussion) -> Void = { _ in }) {
        _discussion = State(initialValue: discussion)
        self.onUpdated = onUpdated
    }

    private var sources: [PlanSourceSnapshot] { discussion.sortedSources }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                VStack(spacing: 0) {
                    list
                    addBar
                }
            }
            .navigationTitle("Sources")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { Image(systemName: "xmark") }
                        .accessibilityLabel("Close")
                        .disabled(isResearching)
                }
            }
        }
    }

    private var list: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                if sources.isEmpty {
                    Text("No sources yet. Add a link below and the agent will research it and update the plan.")
                        .font(.callout)
                        .foregroundStyle(Theme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    Text("\(sources.count) source\(sources.count == 1 ? "" : "s")")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(Theme.accent)
                    ForEach(sources) { source in
                        sourceRow(source)
                    }
                }
                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }
            }
            .padding(16)
        }
        .scrollDismissesKeyboard(.interactively)
    }

    @ViewBuilder
    private func sourceRow(_ source: PlanSourceSnapshot) -> some View {
        NavigationLink {
            SourceDetailView(source: source)
        } label: {
            HStack(alignment: .center, spacing: 10) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(source.displayTitle)
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(.white)
                    if !source.urlString.isEmpty {
                        Text(source.urlString)
                            .font(.caption2)
                            .foregroundStyle(Theme.accent)
                            .lineLimit(1)
                    }
                    if !source.snippet.isEmpty {
                        Text(source.snippet)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                            .lineLimit(3)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Theme.secondaryText)
            }
            .padding(12)
            .background(Color.white.opacity(0.05), in: .rect(cornerRadius: 14))
        }
        .buttonStyle(.plain)
    }

    private var addBar: some View {
        VStack(spacing: 8) {
            if isResearching {
                HStack(spacing: 8) {
                    ProgressView().tint(Theme.accent)
                    Text("Researching the link & updating the plan…")
                        .font(.footnote)
                        .foregroundStyle(Theme.secondaryText)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(spacing: 10) {
                TextField("Add a link (https://…)", text: $newLink, axis: .vertical)
                    .lineLimit(1...2)
                    .textFieldStyle(.plain)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .keyboardType(.URL)
                Button(action: addLink) {
                    Image(systemName: isResearching ? "ellipsis" : "arrow.up.circle.fill")
                        .font(.title2)
                        .foregroundStyle(Theme.accent)
                }
                .disabled(!canAdd)
            }
            .padding(12)
            .glassEffect(in: .capsule)
        }
        .padding(16)
    }

    private var canAdd: Bool {
        guard !isResearching else { return false }
        let trimmed = newLink.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: trimmed), let scheme = url.scheme else { return false }
        return scheme == "http" || scheme == "https"
    }

    private func addLink() {
        let link = newLink.trimmingCharacters(in: .whitespacesAndNewlines)
        guard canAdd else { return }
        isResearching = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                let updated = try await api.addDiscussionSources(id: discussion.id, urls: [link])
                discussion = updated
                newLink = ""
                isResearching = false
                onUpdated(updated)
            } catch {
                isResearching = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct SourceDetailView: View {
    let source: PlanSourceSnapshot

    var body: some View {
        ZStack {
            Theme.background.ignoresSafeArea()
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(source.displayTitle)
                            .font(.title3.weight(.semibold))
                            .foregroundStyle(.white)
                        if let url = source.url {
                            Link(source.urlString, destination: url)
                                .font(.caption)
                                .foregroundStyle(Theme.accent)
                        } else if !source.urlString.isEmpty {
                            Text(source.urlString)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }

                    if source.detailMarkdown.isEmpty {
                        Text("No readable content was returned for this source.")
                            .font(.callout)
                            .foregroundStyle(Theme.secondaryText)
                    } else {
                        MarkdownText(source.detailMarkdown)
                            .font(.body)
                            .foregroundStyle(Theme.secondaryText)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(16)
            }
        }
        .navigationTitle("Source")
        .navigationBarTitleDisplayMode(.inline)
    }
}
