import Combine
import SwiftUI

@MainActor
final class ShareExtensionModel: ObservableObject {
    enum Destination: Hashable {
        case createNew
        case existingPlan
    }

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
    @Published var destination: Destination = .createNew
    @Published var plans: [SharePlanOption] = []
    @Published var selectedPlan: SharePlanOption?
    @Published var planSearchText = ""
    @Published var loadedPlanSearchQuery = ""
    @Published var isLoadingPlans = false
    @Published var isLoadingMorePlans = false
    @Published var canLoadMorePlans = false
    @Published var plansError: String?

    private let inputItems: [Any]
    private let api = ShareExtensionAPI()
    private let openDiscussion: (String) async -> Bool
    private let complete: () -> Void
    private let plansPageSize = 20
    private var planOffset = 0

    init(inputItems: [Any],
         openDiscussion: @escaping (String) async -> Bool,
         complete: @escaping () -> Void) {
        self.inputItems = inputItems
        self.openDiscussion = openDiscussion
        self.complete = complete
    }

    var isAudio: Bool { items.count == 1 && items.first?.kind == .audio }
    var canSubmit: Bool {
        guard phase == .ready else { return false }
        switch destination {
        case .createNew:
            return isAudio || !topic.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        case .existingPlan:
            return selectedPlan != nil
        }
    }
    var title: String {
        if destination == .existingPlan { return "Send to Plan" }
        return isAudio ? (audioForm?.title ?? "Upload Own Audio") : (discussionForm?.title ?? "New Station")
    }
    var submitTitle: String {
        if destination == .existingPlan { return "Send" }
        return isAudio ? (audioForm?.submitTitle ?? "Transcribe") : (discussionForm?.submitTitle ?? "Plan")
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
                switch destination {
                case .createNew:
                    if isAudio {
                        discussionID = try await submitAudio()
                    } else {
                        discussionID = try await submitDiscussion()
                    }
                case .existingPlan:
                    discussionID = try await submitToExistingPlan()
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

    func loadPlans() async {
        let requestedQuery = normalizedPlanQuery
        isLoadingPlans = true
        plansError = nil
        defer {
            if normalizedPlanQuery == requestedQuery {
                isLoadingPlans = false
            }
        }
        do {
            let page = try await api.plans(limit: plansPageSize, offset: 0, query: requestedQuery)
            guard normalizedPlanQuery == requestedQuery else { return }
            loadedPlanSearchQuery = requestedQuery
            plans = page.plans
            planOffset = plansPageSize
            canLoadMorePlans = page.canLoadMore
        } catch {
            guard normalizedPlanQuery == requestedQuery else { return }
            plans = []
            canLoadMorePlans = false
            plansError = error.localizedDescription
        }
    }

    func loadMorePlans() async {
        let requestedQuery = loadedPlanSearchQuery
        let requestedOffset = planOffset
        guard canLoadMorePlans, !isLoadingPlans, !isLoadingMorePlans,
              normalizedPlanQuery == requestedQuery else { return }
        isLoadingMorePlans = true
        defer { isLoadingMorePlans = false }
        do {
            let page = try await api.plans(
                limit: plansPageSize,
                offset: requestedOffset,
                query: requestedQuery
            )
            guard normalizedPlanQuery == requestedQuery,
                  loadedPlanSearchQuery == requestedQuery,
                  planOffset == requestedOffset else { return }
            let existingIDs = Set(plans.map(\.id))
            plans.append(contentsOf: page.plans.filter { !existingIDs.contains($0.id) })
            planOffset += plansPageSize
            canLoadMorePlans = page.canLoadMore
            plansError = nil
        } catch {
            guard normalizedPlanQuery == requestedQuery else { return }
            plansError = error.localizedDescription
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
        let attachments = try await prepareAttachments()
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

    private func submitToExistingPlan() async throws -> String {
        guard let selectedPlan else { throw ShareAPIError.invalidResponse }
        let attachments = try await prepareAttachments()
        phase = .submitting("Adding sources to \(selectedPlan.displayTitle)…")
        let prompt: String
        if selectedPlan.isNews {
            prompt = "Use the shared sources to add one more news roundup to this plan."
        } else {
            prompt = "Please use the shared sources to update this plan. If this is a news plan, add one more news roundup."
        }
        try await api.sendToPlan(id: selectedPlan.id, prompt: prompt, attachments: attachments)
        return selectedPlan.id
    }

    private func prepareAttachments() async throws -> [[String: Any]] {
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
        return attachments
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

    private var normalizedPlanQuery: String {
        planSearchText.trimmingCharacters(in: .whitespacesAndNewlines)
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
        VStack(spacing: 0) {
            Picker("Destination", selection: $model.destination) {
                Label("Create New", systemImage: "plus.circle")
                    .tag(ShareExtensionModel.Destination.createNew)
                Label("Existing Plan", systemImage: "paperplane")
                    .tag(ShareExtensionModel.Destination.existingPlan)
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 4)

            if model.destination == .createNew {
                createNewTab
            } else {
                existingPlanTab
            }
        }
        .disabled(isSubmitting)
    }

    private var createNewTab: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                sourceSection
                if model.isAudio { audioSettings } else { discussionSettings }
                submittingStatus
            }
            .padding(16)
        }
    }

    private var existingPlanTab: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                sourceSection
                planPicker
                submittingStatus
            }
            .padding(16)
        }
        .task(id: model.planSearchText) {
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            await model.loadPlans()
        }
    }

    @ViewBuilder
    private var submittingStatus: some View {
        if case .submitting(let message) = model.phase {
            HStack(spacing: 10) {
                ProgressView()
                Text(message)
            }
            .font(.footnote)
            .foregroundStyle(.secondary)
        }
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

    private var planPicker: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Choose a Plan").font(.headline)
            HStack(spacing: 10) {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search plans", text: $model.planSearchText)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                if !model.planSearchText.isEmpty {
                    Button {
                        model.planSearchText = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear search")
                }
            }
            .padding(12)
            .glassEffect(in: .rect(cornerRadius: 14))

            if model.isLoadingPlans, model.plans.isEmpty {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("Loading plans…")
                }
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 24)
            } else if let error = model.plansError, model.plans.isEmpty {
                ContentUnavailableView(
                    "Couldn't Load Plans",
                    systemImage: "exclamationmark.triangle",
                    description: Text(error)
                )
                Button("Try Again") { Task { await model.loadPlans() } }
                    .frame(maxWidth: .infinity)
            } else if model.plans.isEmpty {
                ContentUnavailableView(
                    model.loadedPlanSearchQuery.isEmpty ? "No Plans Yet" : "No Matching Plans",
                    systemImage: "doc.text.magnifyingglass"
                )
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(model.plans.enumerated()), id: \.element.id) { index, plan in
                        if index > 0 { Divider().padding(.leading, 48) }
                        Button {
                            model.selectedPlan = plan
                        } label: {
                            HStack(spacing: 12) {
                                Image(systemName: plan.isNews ? "newspaper.fill" : "pencil.and.list.clipboard")
                                    .foregroundStyle(.purple)
                                    .frame(width: 24)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(plan.displayTitle)
                                        .font(.subheadline.weight(.semibold))
                                        .foregroundStyle(.primary)
                                        .lineLimit(2)
                                    if !plan.topic.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                        Text(plan.topic)
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                            .lineLimit(1)
                                    }
                                }
                                Spacer()
                                Image(systemName: model.selectedPlan?.id == plan.id ? "checkmark.circle.fill" : "circle")
                                    .foregroundStyle(model.selectedPlan?.id == plan.id ? .purple : .secondary)
                            }
                            .padding(12)
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .glassEffect(in: .rect(cornerRadius: 16))

                if let error = model.plansError {
                    Text(error).font(.caption).foregroundStyle(.red)
                }
            }

            if model.canLoadMorePlans {
                Button {
                    Task { await model.loadMorePlans() }
                } label: {
                    HStack(spacing: 8) {
                        if model.isLoadingMorePlans { ProgressView() }
                        Text(model.isLoadingMorePlans ? "Loading…" : "Load More Plans")
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .disabled(model.isLoadingMorePlans)
            }
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
