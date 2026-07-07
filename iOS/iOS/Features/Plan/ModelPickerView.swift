import SwiftUI

/// Lets the user pick an LLM model for one speaker. Pushed from
/// `SpeakerModelsSheet`; the catalog (GET /api/models) is grouped by the
/// company prefix of the model id ("google/gemini-2.5-pro" → Google). Ids
/// without a "company/" prefix (e.g. "gpt-4o") are collected under "Others",
/// which always sorts last.
struct ModelPickerView: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(EntitlementsManager.self) private var entitlements

    let speakerName: String
    let currentModel: String?
    let models: [ModelInfoDTO]
    let isLoading: Bool
    /// Called with the picked model id.
    let onSelect: (String) -> Void

    @State private var searchText = ""

    var body: some View {
        List {
            if models.isEmpty {
                Section {
                    Text(isLoading ? "Loading models…" : "No models available.")
                        .foregroundStyle(Theme.secondaryText)
                }
            } else {
                ForEach(sections, id: \.company) { section in
                    Section(section.title) {
                        ForEach(section.models) { model in
                            row(for: model)
                        }
                    }
                }
            }
        }
        .navigationTitle(speakerName)
        .navigationBarTitleDisplayMode(.inline)
        .searchable(text: $searchText, prompt: Text("Search models"))
    }

    private func row(for model: ModelInfoDTO) -> some View {
        // Models outside the subscription's allowlist are shown but disabled so
        // the user sees what a higher tier unlocks.
        let allowed = entitlements.isModelAllowed(model.id)
        return Button {
            onSelect(model.id)
            dismiss()
        } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(model.displayLabel)
                        .foregroundStyle(allowed ? .primary : Theme.secondaryText)
                    if model.displayLabel != model.id {
                        Text(model.id)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                    }
                }
                Spacer()
                if !allowed {
                    Image(systemName: "lock.fill")
                        .foregroundStyle(Theme.secondaryText)
                } else if model.id == currentModel {
                    Image(systemName: "checkmark")
                        .foregroundStyle(Theme.accent)
                }
            }
        }
        .disabled(!allowed)
        .accessibilityIdentifier("model.\(model.id)")
    }

    // MARK: - Grouping

    private struct ModelSection {
        let company: String
        let title: String
        let models: [ModelInfoDTO]
    }

    /// Sentinel company key for models whose id has no "company/" prefix.
    private static let othersKey = ""

    /// Friendlier casing for company keys the plain `.capitalized` fallback
    /// would get wrong.
    private static let companyNames: [String: String] = [
        "openai": "OpenAI",
        "deepseek": "DeepSeek",
        "xai": "xAI",
        "x-ai": "xAI",
    ]

    private var filteredModels: [ModelInfoDTO] {
        let query = searchText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !query.isEmpty else { return models }
        return models.filter { model in
            model.displayLabel.localizedCaseInsensitiveContains(query)
                || model.id.localizedCaseInsensitiveContains(query)
                || companyTitle(forKey: Self.companyKey(for: model))
                    .localizedCaseInsensitiveContains(query)
        }
    }

    /// Company key from the id prefix ("google/gemini-2.5-pro" → "google").
    /// Ids without a "/" have no company and fall into "Others".
    private static func companyKey(for model: ModelInfoDTO) -> String {
        let parts = model.id.split(separator: "/", maxSplits: 1)
        guard parts.count == 2, !parts[0].isEmpty else { return othersKey }
        return parts[0].lowercased()
    }

    private func companyTitle(forKey key: String) -> String {
        if key == Self.othersKey {
            return String(localized: "Others",
                          comment: "Model picker section for models without a company prefix")
        }
        return Self.companyNames[key] ?? key.capitalized
    }

    /// Sections per company, alphabetical, with "Others" always last.
    private var sections: [ModelSection] {
        let grouped = Dictionary(grouping: filteredModels, by: Self.companyKey(for:))
        return grouped.keys
            .sorted { a, b in
                if (a == Self.othersKey) != (b == Self.othersKey) { return b == Self.othersKey }
                return a.localizedCaseInsensitiveCompare(b) == .orderedAscending
            }
            .map { key in
                ModelSection(
                    company: key,
                    title: companyTitle(forKey: key),
                    models: (grouped[key] ?? []).sorted {
                        $0.displayLabel.localizedCaseInsensitiveCompare($1.displayLabel) == .orderedAscending
                    })
            }
    }
}
