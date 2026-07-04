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
    @State private var voices: [VoiceInfoDTO] = []
    @State private var isLoadingVoices = true
    /// Name of the speaker whose model is currently being saved (shows a spinner
    /// on that row and blocks dismissal).
    @State private var updatingSpeaker: String?
    @State private var errorMessage: String?
    /// Programmatic navigation target. The model and voice rows live in the
    /// same list row, and two NavigationLinks in one row misfire (tapping one
    /// can activate the other), so the rows are plain buttons that set this.
    @State private var pickerRoute: PickerRoute?

    private enum PickerRoute: Hashable, Identifiable {
        case model(speakerID: String)
        case voice(speakerID: String)

        var id: String {
            switch self {
            case .model(let speakerID): "model-\(speakerID)"
            case .voice(let speakerID): "voice-\(speakerID)"
            }
        }
    }

    /// A plan speaker flattened for display. `id` is the name, which is unique
    /// within a plan and is what the backend matches on.
    private struct Speaker: Identifiable {
        let id: String
        let name: String
        let role: String
        let isHost: Bool
        let model: String?
        let voice: String?
    }

    private var speakers: [Speaker] {
        var out: [Speaker] = []
        func appendUnique(name: String, role: String, isHost: Bool, model: String?, voice: String?, seen: inout Set<String>) {
            let key = Self.normalizedSpeakerName(name)
            guard !key.isEmpty, !seen.contains(key) else { return }
            out.append(Speaker(id: name, name: name, role: role, isHost: isHost, model: model, voice: voice))
            seen.insert(key)
        }

        if discussion.script?.type == "audio-book" {
            var seenNames = Set<String>()
            if let host = discussion.script?.audioBookHost, !host.name.isEmpty {
                appendUnique(name: host.name, role: "Narrator", isHost: true,
                             model: host.model, voice: host.voice, seen: &seenNames)
            }
            for speaker in discussion.script?.audioBookSpeakers ?? [] {
                let role = (speaker.description ?? speaker.gender ?? "")
                    .trimmingCharacters(in: .whitespacesAndNewlines)
                appendUnique(name: speaker.name, role: role, isHost: false,
                             model: speaker.model, voice: speaker.voice, seen: &seenNames)
            }
            return out
        }
        if let host = discussion.script?.host, !host.name.isEmpty {
            out.append(Speaker(id: host.name, name: host.name,
                               role: "Moderator", isHost: true, model: host.model,
                               voice: host.voice))
        }
        for d in discussion.script?.discussants ?? [] where !d.name.isEmpty {
            let aspect = d.aspect?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            out.append(Speaker(id: d.name, name: d.name,
                               role: aspect.isEmpty ? "Discussant" : aspect,
                               isHost: false, model: d.model, voice: d.voice))
        }
        return out
    }

    /// BCP-47 plan language used for backend voice-preview sample selection.
    private var planLanguage: String {
        discussion.script?.language ?? discussion.language
    }

    var body: some View {
        NavigationStack {
            Form {
                if isLoadingModels && models.isEmpty {
                    Section {
                        modelCatalogLoadingView
                    }
                } else if speakers.isEmpty {
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
            .navigationDestination(item: $pickerRoute) { route in
                destination(for: route)
            }
            .animation(.easeInOut(duration: 0.18), value: isLoadingModels)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .disabled(updatingSpeaker != nil)
                }
            }
        }
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(updatingSpeaker != nil)
        .task {
            async let modelsLoad: Void = loadModels()
            async let voicesLoad: Void = loadVoices()
            async let discussionLoad: Void = refreshDiscussion()
            _ = await (modelsLoad, voicesLoad, discussionLoad)
        }
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
                    if isLoadingModels && models.isEmpty {
                        modelLoadingLabel
                    } else {
                        modelRow(for: speaker)
                    }
                } else {
                    Text(currentLabel(for: speaker))
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.leading, 34)
            if updatingSpeaker != speaker.id {
                voiceRow(for: speaker)
                    .padding(.leading, 34)
            }
        }
    }

    private var modelCatalogLoadingView: some View {
        VStack(spacing: 12) {
            ProgressView()
                .controlSize(.large)
                .tint(Theme.accent)
            Text("Loading models…")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(Theme.secondaryText)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 28)
        .accessibilityLabel("Loading models")
    }

    private var modelLoadingLabel: some View {
        HStack(spacing: 8) {
            ProgressView()
                .controlSize(.small)
                .tint(Theme.accent)
            Text("Loading models…")
                .font(.subheadline)
            Spacer(minLength: 0)
        }
        .foregroundStyle(Theme.secondaryText)
        .accessibilityLabel("Loading models")
    }

    @ViewBuilder
    private func voiceRow(for speaker: Speaker) -> some View {
        if allowsEditing {
            Button {
                pickerRoute = .voice(speakerID: speaker.id)
            } label: {
                voiceLabel(for: speaker)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("speakerVoice.link.\(speaker.id)")
        } else {
            voiceLabel(for: speaker)
        }
    }

    private func voiceLabel(for speaker: Speaker) -> some View {
        HStack(spacing: 4) {
            Image(systemName: "waveform")
                .font(.caption)
            Text(currentVoiceLabel(for: speaker))
                .font(.subheadline)
                .lineLimit(1)
        }
        .foregroundStyle(allowsEditing ? Theme.accent : Theme.secondaryText)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func currentVoiceLabel(for speaker: Speaker) -> String {
        guard let voice = speaker.voice, !voice.isEmpty else {
            return String(localized: "Automatic voice",
                          comment: "Voice row label when no explicit TTS voice is picked")
        }
        if let match = voices.first(where: { $0.name == voice }) { return match.displayName }
        return voice
    }

    private func modelRow(for speaker: Speaker) -> some View {
        Button {
            pickerRoute = .model(speakerID: speaker.id)
        } label: {
            HStack(spacing: 4) {
                Image(systemName: "cpu")
                    .font(.caption)
                Text(currentLabel(for: speaker))
                    .font(.subheadline)
                    .lineLimit(1)
            }
            .foregroundStyle(Theme.accent)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("speakerModel.link.\(speaker.id)")
    }

    @ViewBuilder
    private func destination(for route: PickerRoute) -> some View {
        switch route {
        case .model(let speakerID):
            if let speaker = speakers.first(where: { $0.id == speakerID }) {
                ModelPickerView(
                    speakerName: speaker.name,
                    currentModel: speaker.model,
                    models: models,
                    isLoading: isLoadingModels,
                    onSelect: { update(speaker: speaker, model: $0) })
            }
        case .voice(let speakerID):
            if let speaker = speakers.first(where: { $0.id == speakerID }) {
                VoicePickerView(
                    speakerName: speaker.name,
                    currentVoice: speaker.voice,
                    planLanguage: planLanguage,
                    voices: voices,
                    isLoading: isLoadingVoices,
                    onSelect: { update(speaker: speaker, voice: $0) })
            }
        }
    }

    private func currentLabel(for speaker: Speaker) -> String {
        guard let model = speaker.model, !model.isEmpty else { return "Default" }
        if let match = models.first(where: { $0.id == model }) { return match.displayLabel }
        return model
    }

    private var footerText: String {
        if allowsEditing {
            return "The selected model and voice are used for this speaker when the plan is generated."
        }
        return "These model and voice assignments are saved with this plan."
    }

    private static func normalizedSpeakerName(_ name: String) -> String {
        name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
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

    private func loadVoices() async {
        isLoadingVoices = true
        defer { isLoadingVoices = false }
        do {
            voices = try await APIClient(tokens: auth).availableVoices()
        } catch {
            // The voice roster is optional (e.g. Azure unconfigured → 503);
            // failing to load it must not block model editing, so no error
            // banner — the picker just shows "No voices available".
            guard !APIClient.isCancellation(error) else { return }
        }
    }

    /// Re-fetches the discussion so the sheet reflects the persisted per-speaker
    /// model/voice overrides. The presenting screen's copy can be stale: the
    /// library list endpoint doesn't apply overrides, and the plan screen never
    /// re-fetches the discussion after navigation.
    private func refreshDiscussion() async {
        guard let fresh = try? await APIClient(tokens: auth)
            .discussion(id: discussion.id, includeEditTurns: false),
            fresh.script != nil,
            updatingSpeaker == nil
        else { return }
        discussion = fresh
    }

    /// Persists a voice pick ("" clears back to automatic) and refreshes the
    /// bound discussion with the server's updated plan.
    private func update(speaker: Speaker, voice: String) {
        guard voice != (speaker.voice ?? "") else { return }
        updatingSpeaker = speaker.id
        errorMessage = nil
        Task { @MainActor in
            defer { updatingSpeaker = nil }
            do {
                discussion = try await APIClient(tokens: auth).updateSpeakerVoice(
                    id: discussion.id, speaker: speaker.name, voice: voice)
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
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
