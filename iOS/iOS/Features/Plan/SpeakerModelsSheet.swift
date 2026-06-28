import SwiftUI

/// Lets the user change the LLM model for each speaker (host + discussants) in a
/// plan. Presented as a bottom sheet from the plan card's Panelists section.
/// The model list is fetched live from GET /api/models; picking a model from a
/// speaker's dropdown persists immediately via
/// PATCH /api/discussions/{id}/speaker-model and updates the bound discussion.
struct SpeakerModelsSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    @Binding var discussion: Discussion
    var allowsEditing = true

    @State private var models: [ModelInfoDTO] = []
    @State private var isLoadingModels = true
    /// Name of the speaker whose model is currently being saved (shows a spinner
    /// on that row and blocks dismissal).
    @State private var updatingSpeaker: String?
    @State private var errorMessage: String?

    /// A plan speaker flattened for display. `id` is the name, which is unique
    /// within a plan and is what the backend matches on.
    private struct Speaker: Identifiable {
        let id: String
        let name: String
        let role: String
        let isHost: Bool
        let model: String?
    }

    private var speakers: [Speaker] {
        var out: [Speaker] = []
        if let host = discussion.script?.host, !host.name.isEmpty {
            out.append(Speaker(id: host.name, name: host.name,
                               role: "Moderator", isHost: true, model: host.model))
        }
        for d in discussion.script?.discussants ?? [] where !d.name.isEmpty {
            let aspect = d.aspect?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            out.append(Speaker(id: d.name, name: d.name,
                               role: aspect.isEmpty ? "Discussant" : aspect,
                               isHost: false, model: d.model))
        }
        return out
    }

    var body: some View {
        NavigationStack {
            Form {
                if speakers.isEmpty {
                    Section {
                        Text("This plan has no speakers yet.")
                            .foregroundStyle(Theme.secondaryText)
                    }
                } else {
                    Section {
                        ForEach(speakers) { speaker in
                            row(for: speaker)
                        }
                    } footer: {
                        Text(footerText)
                    }
                }

                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Speaker Models")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .disabled(updatingSpeaker != nil)
                }
            }
        }
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(updatingSpeaker != nil)
        .task { await loadModels() }
    }

    @ViewBuilder
    private func row(for speaker: Speaker) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: speaker.isHost ? "person.wave.2.fill" : "person.fill")
                    .foregroundStyle(Theme.accent)
                    .frame(width: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(speaker.name).font(.body.weight(.semibold))
                    if !speaker.role.isEmpty {
                        Text(speaker.role)
                            .font(.caption)
                            .foregroundStyle(Theme.secondaryText)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack {
                if updatingSpeaker == speaker.id {
                    ProgressView().controlSize(.small)
                } else if allowsEditing {
                    modelMenu(for: speaker)
                } else {
                    Text(currentLabel(for: speaker))
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.leading, 34)
        }
    }

    @ViewBuilder
    private func modelMenu(for speaker: Speaker) -> some View {
        Menu {
            if models.isEmpty {
                Text(isLoadingModels ? "Loading…" : "No models available")
            } else {
                ForEach(models) { model in
                    Button {
                        update(speaker: speaker, model: model.id)
                    } label: {
                        if model.id == speaker.model {
                            Label(model.displayLabel, systemImage: "checkmark")
                        } else {
                            Text(model.displayLabel)
                        }
                    }
                    .accessibilityIdentifier("model.\(model.id)")
                }
            }
        } label: {
            HStack(spacing: 4) {
                Text(currentLabel(for: speaker))
                    .font(.subheadline)
                    .lineLimit(1)
                Image(systemName: "chevron.up.chevron.down")
                    .font(.caption2)
            }
            .foregroundStyle(Theme.accent)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .accessibilityIdentifier("speakerModel.menu.\(speaker.id)")
        .disabled(isLoadingModels && models.isEmpty)
    }

    private func currentLabel(for speaker: Speaker) -> String {
        guard let model = speaker.model, !model.isEmpty else { return "Default" }
        if let match = models.first(where: { $0.id == model }) { return match.displayLabel }
        return model
    }

    private var footerText: String {
        if allowsEditing {
            return "The selected model is used for this speaker when the plan is generated."
        }
        return "These model assignments are saved with this plan."
    }

    private func loadModels() async {
        isLoadingModels = true
        defer { isLoadingModels = false }
        do {
            models = try await APIClient(tokens: auth).availableModels()
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func update(speaker: Speaker, model: String) {
        guard model != speaker.model else { return }
        updatingSpeaker = speaker.id
        errorMessage = nil
        Task { @MainActor in
            defer { updatingSpeaker = nil }
            do {
                discussion = try await APIClient(tokens: auth).updateSpeakerModel(
                    id: discussion.id, speaker: speaker.name, model: model)
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
