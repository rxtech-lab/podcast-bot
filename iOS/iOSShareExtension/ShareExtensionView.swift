import Combine
import SwiftUI

@MainActor
final class ShareExtensionModel: ObservableObject {
    enum Phase: Equatable {
        case loading
        case ready
        case submitting(String)
        case failed(String)
    }

    @Published var phase: Phase = .loading
    @Published var items: [IncomingShareItem] = []
    @Published var discussionForm: ShareDiscussionFormDefinition?
    @Published var audioForm: ShareAudioFormDefinition?
    @Published var topic = ""
    @Published var type = "discussion"
    @Published var template = "default"
    @Published var language = "en-US"
    @Published var discussants = 3
    @Published var generateCover = false
    @Published var maxSpeakers = 2

    private let inputItems: [Any]
    private let api = ShareExtensionAPI()
    private let openDiscussion: (String) async -> Bool
    private let complete: () -> Void

    init(inputItems: [Any],
         openDiscussion: @escaping (String) async -> Bool,
         complete: @escaping () -> Void) {
        self.inputItems = inputItems
        self.openDiscussion = openDiscussion
        self.complete = complete
    }

    var isAudio: Bool { items.count == 1 && items.first?.kind == .audio }
    var canSubmit: Bool {
        phase == .ready && (isAudio || !topic.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }
    var title: String {
        isAudio ? (audioForm?.title ?? "Upload Own Audio") : (discussionForm?.title ?? "New Station")
    }
    var submitTitle: String {
        isAudio ? (audioForm?.submitTitle ?? "Transcribe") : (discussionForm?.submitTitle ?? "Plan")
    }
    var templates: [ShareOption] { discussionForm?.templatesByType[type] ?? [] }

    func load() async {
        do {
            async let payload = SharePayloadLoader.load(inputItems: inputItems)
            async let bootstrap = api.precheck()
            items = try await payload
            let precheck = try await bootstrap
            discussionForm = precheck.discussion
            audioForm = precheck.uploadAudio
            applyInitialValues(precheck.discussion)
            if isAudio {
                guard let audioForm = precheck.uploadAudio else {
                    throw ShareAPIError.server(403, "Uploading your own audio is not enabled for your account.")
                }
                maxSpeakers = audioForm.initialMaxSpeakers
                if let item = items.first, audioForm.maxBytes > 0, fileSize(item) > audioForm.maxBytes {
                    throw ShareAPIError.server(400, "This audio file is larger than your plan allows (\(formatBytes(audioForm.maxBytes)) max).")
                }
            }
            phase = .ready
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    func cancel() { complete() }

    func submit() {
        guard canSubmit else { return }
        Task {
            do {
                let discussionID: String
                if isAudio {
                    discussionID = try await submitAudio()
                } else {
                    discussionID = try await submitDiscussion()
                }
                guard await openDiscussion(discussionID) else {
                    throw ShareAPIError.couldNotOpenApp
                }
                complete()
            } catch {
                phase = .failed(error.localizedDescription)
            }
        }
    }

    func normalizeTemplate() {
        let available = templates
        if !available.contains(where: { $0.id == template }) {
            template = available.first?.id ?? "default"
        }
    }

    private func applyInitialValues(_ form: ShareDiscussionFormDefinition) {
        type = form.initialType
        template = form.initialTemplate
        language = form.initialLanguage
        discussants = form.initialDiscussants
        generateCover = form.initialGenerateCover
        topic = form.initialTopic.isEmpty ? suggestedTopic() : form.initialTopic
        normalizeTemplate()
    }

    private func suggestedTopic() -> String {
        let links = items.compactMap(\.webURL).map(\.absoluteString)
        if !links.isEmpty { return links.joined(separator: "\n") }
        let names = items.map(\.filename).joined(separator: ", ")
        return names.isEmpty ? "" : "Create a station from the shared source: \(names)"
    }

    private func submitDiscussion() async throws -> String {
        var attachments: [[String: Any]] = []
        for (index, item) in items.enumerated() {
            phase = .submitting("Preparing source \(index + 1) of \(items.count)…")
            if let url = item.webURL {
                attachments.append([
                    "filename": item.filename,
                    "url": url.absoluteString,
                    "mime_type": item.mimeType,
                ])
            } else {
                attachments.append(try await api.upload(item: item, podcastAudio: false).attachmentObject)
            }
        }
        phase = .submitting(discussionForm?.loadingTitle ?? "Creating station…")
        return try await api.createDiscussion(
            topic: topic.trimmingCharacters(in: .whitespacesAndNewlines),
            type: type,
            template: template,
            language: language,
            discussants: discussants,
            generateCover: generateCover,
            attachments: attachments
        )
    }

    private func submitAudio() async throws -> String {
        guard let item = items.first else { throw SharePayloadError.noSupportedItems }
        phase = .submitting("Uploading \(item.filename)…")
        let upload = try await api.upload(item: item, podcastAudio: true)
        phase = .submitting(audioForm?.loadingTitle ?? "Starting transcription…")
        return try await api.createUploadedAudio(
            upload: upload,
            sizeBytes: fileSize(item),
            maxSpeakers: maxSpeakers
        )
    }

    private func fileSize(_ item: IncomingShareItem) -> Int64 {
        guard let fileURL = item.fileURL else { return 0 }
        let size = try? fileURL.resourceValues(forKeys: [.fileSizeKey]).fileSize
        return Int64(size ?? 0)
    }

    private func formatBytes(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
    }
}

struct ShareExtensionRootView: View {
    @ObservedObject var model: ShareExtensionModel

    var body: some View {
        NavigationStack {
            Group {
                switch model.phase {
                case .loading:
                    ProgressView("Loading shared items…")
                case .failed(let message):
                    ContentUnavailableView(
                        "Couldn't Add to PanelFM",
                        systemImage: "exclamationmark.triangle",
                        description: Text(message)
                    )
                    .padding()
                case .ready, .submitting:
                    form
                }
            }
            .navigationTitle(model.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: model.cancel)
                        .disabled(isSubmitting)
                }
                if model.phase != .loading, !isFailed {
                    ToolbarItem(placement: .confirmationAction) {
                        Button(model.submitTitle, action: model.submit)
                            .disabled(!model.canSubmit)
                    }
                }
            }
        }
        .task { await model.load() }
    }

    private var isSubmitting: Bool {
        if case .submitting = model.phase { return true }
        return false
    }

    private var isFailed: Bool {
        if case .failed = model.phase { return true }
        return false
    }

    private var form: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                sourceSection
                if model.isAudio { audioSettings } else { discussionSettings }
                if case .submitting(let message) = model.phase {
                    HStack(spacing: 10) {
                        ProgressView()
                        Text(message)
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
        .disabled(isSubmitting)
    }

    private var sourceSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(model.items.count == 1 ? "Shared Source" : "Shared Sources")
                .font(.headline)
            VStack(spacing: 0) {
                ForEach(Array(model.items.enumerated()), id: \.element.id) { index, item in
                    if index > 0 { Divider().padding(.leading, 44) }
                    HStack(spacing: 12) {
                        Image(systemName: item.systemImage)
                            .foregroundStyle(.purple)
                            .frame(width: 24)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(item.filename).font(.subheadline.weight(.semibold)).lineLimit(1)
                            Text(item.webURL?.absoluteString ?? item.mimeType)
                                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer()
                    }
                    .padding(12)
                }
            }
            .glassEffect(in: .rect(cornerRadius: 16))
        }
    }

    @ViewBuilder
    private var discussionSettings: some View {
        if let form = model.discussionForm {
            VStack(alignment: .leading, spacing: 8) {
                Text(form.topicTitle).font(.headline)
                TextField("", text: $model.topic, axis: .vertical)
                    .lineLimit(5...10)
                    .padding(12)
                    .glassEffect(in: .rect(cornerRadius: 16))
                if !form.topicDescription.isEmpty {
                    Text(form.topicDescription).font(.caption).foregroundStyle(.secondary)
                }
            }
            VStack(spacing: 0) {
                menuRow("Type", systemImage: "book.pages.fill", selection: $model.type, options: form.types)
                    .onChange(of: model.type) { _, _ in model.normalizeTemplate() }
                Divider().padding(.leading, 48)
                menuRow("Template", systemImage: "square.grid.2x2", selection: $model.template, options: model.templates)
                if model.type == "discussion" {
                    Divider().padding(.leading, 48)
                    HStack {
                        Label("Panelists", systemImage: "person.2.fill")
                        Spacer()
                        Stepper("Panelists", value: $model.discussants, in: form.discussantsRange)
                            .labelsHidden()
                        Text("\(model.discussants)").monospacedDigit().frame(minWidth: 22)
                    }
                    .padding(12)
                }
                Divider().padding(.leading, 48)
                menuRow("Language", systemImage: "globe", selection: $model.language, options: form.languages)
                Divider().padding(.leading, 48)
                Toggle(isOn: $model.generateCover) {
                    Label("Generate cover", systemImage: "photo.badge.plus")
                }
                .tint(.purple)
                .padding(12)
            }
            .glassEffect(in: .rect(cornerRadius: 16))
        }
    }

    @ViewBuilder
    private var audioSettings: some View {
        if let form = model.audioForm {
            HStack {
                Label("Max speakers", systemImage: "person.2.fill")
                Spacer()
                Stepper("Max speakers", value: $model.maxSpeakers, in: form.maxSpeakersRange)
                    .labelsHidden()
                Text("\(model.maxSpeakers)").monospacedDigit().frame(minWidth: 26)
            }
            .padding(12)
            .glassEffect(in: .rect(cornerRadius: 16))
        }
    }

    private func menuRow(_ title: String, systemImage: String,
                         selection: Binding<String>, options: [ShareOption]) -> some View {
        HStack {
            Label(title, systemImage: systemImage)
            Spacer()
            Picker(title, selection: selection) {
                ForEach(options) { option in Text(option.label).tag(option.id) }
            }
            .labelsHidden()
            .pickerStyle(.menu)
            .tint(.purple)
        }
        .padding(12)
    }
}
