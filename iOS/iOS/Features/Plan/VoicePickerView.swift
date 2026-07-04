import SwiftUI
import Translation

/// Lets the user pick an Azure TTS voice for one speaker. Pushed from
/// `SpeakerModelsSheet`; the full catalog (GET /api/voices) is grouped by
/// language with the plan's own language first. Every row can play a short
/// sample — the sentence is translated on-device into the plan language via
/// the Translation framework (falling back to English when no translation is
/// available) and synthesized server-side, where samples are cached per
/// (voice, language) so repeat previews are instant.
struct VoicePickerView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    let speakerName: String
    let currentVoice: String?
    /// BCP-47 plan language (e.g. "zh-CN") used for sample translation and the
    /// server-side preview cache key.
    let planLanguage: String
    let voices: [VoiceInfoDTO]
    let isLoading: Bool
    /// Called with the picked voice ShortName, or "" for automatic assignment.
    let onSelect: (String) -> Void

    @State private var searchText = ""
    @State private var translationConfig: TranslationSession.Configuration?
    @State private var translatedSample: String?
    /// Voice whose preview URL is currently being fetched (shows a spinner).
    @State private var previewingVoice: String?
    /// Voice whose sample is loaded into the player.
    @State private var playingVoice: String?
    @State private var previewPlayer: AudioMessagePlayer?
    @State private var errorMessage: String?

    private static let englishSample = "Hello! Here's a quick preview of how this voice sounds."

    private var sampleText: String { translatedSample ?? Self.englishSample }

    /// Lowercased language code of the plan language ("zh-CN" → "zh").
    private var planLanguageCode: String {
        planLanguage.split(separator: "-").first.map { $0.lowercased() } ?? "en"
    }

    var body: some View {
        List {
            if let errorMessage {
                Section {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
            }

            Section {
                autoRow
            }

            if voices.isEmpty {
                Section {
                    Text(isLoading ? "Loading voices…" : "No voices available.")
                        .foregroundStyle(Theme.secondaryText)
                }
            } else {
                ForEach(sections, id: \.locale) { section in
                    Section(section.title) {
                        ForEach(section.voices) { voice in
                            row(for: voice)
                        }
                    }
                }
            }
        }
        .navigationTitle(speakerName)
        .navigationBarTitleDisplayMode(.inline)
        .searchable(text: $searchText, prompt: Text("Search voices"))
        .task {
            guard translatedSample == nil, planLanguageCode != "en" else { return }
            translationConfig = TranslationSession.Configuration(
                source: Locale.Language(identifier: "en"),
                target: Locale.Language(identifier: planLanguage))
        }
        .translationTask(translationConfig) { session in
            // Best-effort: an undownloaded pack or unsupported pair just keeps
            // the English sample.
            let translated = try? await session.translate(Self.englishSample).targetText
            await MainActor.run { translatedSample = translated }
        }
        .onDisappear { previewPlayer?.pause() }
    }

    private var autoRow: some View {
        Button {
            onSelect("")
            dismiss()
        } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Automatic")
                        .foregroundStyle(.primary)
                    Text("Let the engine pick a fitting voice")
                        .font(.caption)
                        .foregroundStyle(Theme.secondaryText)
                }
                Spacer()
                if currentVoice?.isEmpty != false {
                    Image(systemName: "checkmark")
                        .foregroundStyle(Theme.accent)
                }
            }
        }
        .accessibilityIdentifier("voice.auto")
    }

    private func row(for voice: VoiceInfoDTO) -> some View {
        HStack(spacing: 12) {
            Button {
                onSelect(voice.name)
                dismiss()
            } label: {
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(voice.displayName)
                            .foregroundStyle(.primary)
                        if let detail = voiceDetail(voice) {
                            Text(detail)
                                .font(.caption)
                                .foregroundStyle(Theme.secondaryText)
                        }
                    }
                    Spacer()
                    if voice.name == currentVoice {
                        Image(systemName: "checkmark")
                            .foregroundStyle(Theme.accent)
                    }
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("voice.\(voice.name)")

            previewButton(for: voice)
        }
    }

    @ViewBuilder
    private func previewButton(for voice: VoiceInfoDTO) -> some View {
        if previewingVoice == voice.name {
            ProgressView().controlSize(.small)
        } else {
            Button {
                preview(voice)
            } label: {
                Image(systemName: isPlayingPreview(of: voice) ? "pause.circle.fill" : "play.circle")
                    .font(.title3)
                    .foregroundStyle(Theme.accent)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("voice.preview.\(voice.name)")
        }
    }

    private func isPlayingPreview(of voice: VoiceInfoDTO) -> Bool {
        playingVoice == voice.name && previewPlayer?.isPlaying == true
    }

    private func preview(_ voice: VoiceInfoDTO) {
        if playingVoice == voice.name, let previewPlayer {
            previewPlayer.toggle()
            return
        }
        previewPlayer?.pause()
        previewingVoice = voice.name
        errorMessage = nil
        Task { @MainActor in
            defer { previewingVoice = nil }
            do {
                let url = try await APIClient(tokens: auth).previewVoice(
                    voice: voice.name, language: planLanguage, text: sampleText)
                let player = AudioMessagePlayer(urlString: url.absoluteString)
                previewPlayer = player
                playingVoice = voice.name
                player.play()
            } catch {
                guard !APIClient.isCancellation(error) else { return }
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    // MARK: - Grouping

    private struct VoiceSection {
        let locale: String
        let title: String
        let voices: [VoiceInfoDTO]
    }

    private var filteredVoices: [VoiceInfoDTO] {
        let query = searchText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !query.isEmpty else { return voices }
        return voices.filter { voice in
            voice.displayName.localizedCaseInsensitiveContains(query)
                || voice.name.localizedCaseInsensitiveContains(query)
                || (voice.localeName ?? "").localizedCaseInsensitiveContains(query)
                || voice.locale.localizedCaseInsensitiveContains(query)
        }
    }

    /// Sections per locale: the plan's exact locale first, then sibling
    /// locales of the same language, then everything else alphabetically.
    private var sections: [VoiceSection] {
        let grouped = Dictionary(grouping: filteredVoices, by: \.locale)
        return grouped.keys
            .sorted { a, b in
                let (ra, rb) = (localeRank(a), localeRank(b))
                if ra != rb { return ra < rb }
                return a.localizedCaseInsensitiveCompare(b) == .orderedAscending
            }
            .map { locale in
                let group = grouped[locale] ?? []
                return VoiceSection(
                    locale: locale,
                    title: group.first?.localeName ?? locale,
                    voices: group.sorted {
                        $0.displayName.localizedCaseInsensitiveCompare($1.displayName) == .orderedAscending
                    })
            }
    }

    private func localeRank(_ locale: String) -> Int {
        if locale.caseInsensitiveCompare(planLanguage) == .orderedSame { return 0 }
        if locale.lowercased().hasPrefix(planLanguageCode + "-") || locale.lowercased() == planLanguageCode { return 1 }
        return 2
    }

    private func voiceDetail(_ voice: VoiceInfoDTO) -> String? {
        var parts: [String] = []
        if let gender = voice.gender, !gender.isEmpty { parts.append(gender) }
        if let styles = voice.styles, !styles.isEmpty {
            parts.append(styles.prefix(3).joined(separator: ", "))
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
