import JSONSchema
import JSONSchemaForm
import OSLog
import PhotosUI
import SwiftUI
import TipKit

private let newDiscussionLog = Logger(subsystem: "com.debatebot.ios", category: "NewDiscussion")

/// Step 1 of planning: render the server-provided form, then ask the backend to
/// create a planning discussion from the submitted values.
struct NewDiscussionView: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    @AppStorage("newDiscussion.settings.type") private var storedType = ""
    @AppStorage("newDiscussion.settings.template") private var storedTemplate = ""
    @AppStorage("newDiscussion.settings.discussants") private var storedDiscussants = 0
    @AppStorage("newDiscussion.settings.language") private var storedLanguage = ""
    @AppStorage("newDiscussion.settings.generateCover") private var storedGenerateCover = false
    @AppStorage("newDiscussion.settings.hasStoredValues") private var hasStoredSettings = false
    @AppStorage("newDiscussion.discussants") private var legacyStoredDiscussants = 0
    @AppStorage("newDiscussion.language") private var legacyStoredLanguage = ""
    /// Called once the placeholder discussion and its first planning turn are
    /// created. The plan page resumes that server-seeded turn.
    var onPlanned: (Discussion) -> Void = { _ in }
    private let initialReference: PodcastReference?
    @State private var precheckForm: PrecheckFormDTO?
    @State private var formSchema: JSONSchema?
    @State private var formSchemaJSON: String?
    @State private var formUISchema: [String: Any]?
    @State private var formData = FormData.object(properties: [:])
    @State private var pickerCoordinator = NewDiscussionFormCoordinator()
    @State private var attachmentsCoordinator = NewDiscussionAttachmentsCoordinator()
    @State private var isLoadingForm = false
    @State private var isPlanning = false
    @State private var errorMessage: String?

    /// Field id of the parent-discussion picker within the rendered schema
    /// (root → reference → discussion_id), used to pre-fill a contextual parent.
    private let referenceFieldID = "root_reference_discussion_id"

    init(reference: PodcastReference? = nil, onPlanned: @escaping (Discussion) -> Void = { _ in }) {
        initialReference = reference
        self.onPlanned = onPlanned
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                form
            }
            .navigationTitle(precheckForm?.title ?? "New \(AppStringLiteral.stationNameRaw)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(precheckForm?.cancelTitle ?? "Cancel") { dismiss() }
                        .disabled(isPlanning)
                }

                ToolbarItem(placement: .confirmationAction) {
                    Button(action: plan) {
                        if isPlanning {
                            ProgressView()
                        } else {
                            Text(precheckForm?.submitTitle ?? "Plan")
                        }
                    }
                    .disabled(!canSubmit)
                    .accessibilityIdentifier("newPlan.submit")
                    .popoverTip(NewDiscussionPlanTip(), arrowEdge: .top)
                }
            }
        }
        .interactiveDismissDisabled(true)
        .sheet(isPresented: pickerPresented) {
            ReferencePodcastPickerSheet(
                selectedID: pickerCoordinator.activeSelectionID,
                onSelect: { pickerCoordinator.complete(with: $0) }
            )
        }
        .fileImporter(
            isPresented: attachmentBinding(\.showingImporter),
            allowedContentTypes: attachmentContentTypes,
            allowsMultipleSelection: true
        ) { result in
            if case let .success(urls) = result {
                attachmentsCoordinator.importFiles(urls)
            }
        }
        .photosPicker(
            isPresented: attachmentBinding(\.showingPhotos),
            selection: photosSelection,
            matching: .images
        )
        .onChange(of: attachmentsCoordinator.selectedPhotos) { _, items in
            attachmentsCoordinator.importPhotos(items)
        }
        .sheet(isPresented: attachmentBinding(\.showingNotionPicker)) {
            NotionPagePickerSheet { pages in
                attachmentsCoordinator.importNotionPages(pages)
            }
        }
        .task {
            await loadPrecheck()
        }
        .onChange(of: formData) { _, newValue in
            persistSettings(from: newValue)
        }
    }

    /// Two-way binding into a coordinator presentation flag, keeping the picker
    /// state parent-owned (the form widget can't reliably host these sheets).
    private func attachmentBinding(_ keyPath: ReferenceWritableKeyPath<NewDiscussionAttachmentsCoordinator, Bool>) -> Binding<Bool> {
        Binding(
            get: { attachmentsCoordinator[keyPath: keyPath] },
            set: { attachmentsCoordinator[keyPath: keyPath] = $0 }
        )
    }

    private var photosSelection: Binding<[PhotosPickerItem]> {
        Binding(
            get: { attachmentsCoordinator.selectedPhotos },
            set: { attachmentsCoordinator.selectedPhotos = $0 }
        )
    }

    /// Drives the parent-discussion picker sheet; clears coordinator state when the
    /// sheet is dismissed without a selection (Cancel / swipe-down).
    private var pickerPresented: Binding<Bool> {
        Binding(
            get: { pickerCoordinator.isPresenting },
            set: { isPresenting in
                if isPresenting {
                    pickerCoordinator.isPresenting = true
                } else {
                    pickerCoordinator.cancel()
                }
            }
        )
    }

    private var form: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if isLoadingForm && precheckForm == nil {
                    HStack {
                        Spacer()
                        ProgressView().tint(Theme.accent)
                        Spacer()
                    }
                    .padding(.vertical, 24)
                } else if let formSchema, let formSchemaJSON {
                    JSONSchemaForm(
                        schema: formSchema,
                        uiSchema: formUISchema,
                        formData: $formData,
                        schemaJSON: formSchemaJSON,
                        showSubmitButton: false,
                        widgets: NewDiscussionFormUI.widgets(
                            rootFormData: $formData,
                            coordinator: pickerCoordinator,
                            attachmentsCoordinator: attachmentsCoordinator
                        ),
                        templates: NewDiscussionFormUI.templates()
                    )
                } else {
                    EmptyView()
                }

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isPlanning {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(precheckForm?.loadingTitle ?? "Creating \(AppStringLiteral.stationNameRaw)...")
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
        .scrollDismissesKeyboard(.interactively)
        .disabled(isPlanning)
    }

    private var canSubmit: Bool {
        precheckForm != nil
            && !isLoadingForm
            && !isPlanning
            && !attachmentsCoordinator.isUploading
    }

    @MainActor
    private func loadPrecheck() async {
        guard precheckForm == nil else { return }
        isLoadingForm = true
        defer { isLoadingForm = false }
        do {
            let response = try await APIClient(tokens: auth).precheck()
            applyPrecheckForm(response.newDiscussion.form)
            errorMessage = nil
        } catch {
            newDiscussionLog.error("precheck failed error=\(error.localizedDescription, privacy: .public)")
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    @MainActor
    private func applyPrecheckForm(_ form: PrecheckFormDTO) {
        precheckForm = form
        formSchemaJSON = form.schemaJSONString
        formSchema = formSchemaJSON.flatMap { try? JSONSchema(jsonString: $0) }
        formUISchema = form.uiSchemaDictionary
        formData = restoringStoredSettings(in: form.decodedInitialData, form: form)
        // When planning from an existing podcast, pre-fill the parent into the form
        // and cache it so the picker row shows its title.
        if let initialReference {
            setFormReferenceID(initialReference.id)
            pickerCoordinator.cache(initialReference, for: referenceFieldID)
        }
        persistSettings(from: formData)
    }

    /// Writes a parent discussion id into the form's `reference.discussion_id` value.
    private func setFormReferenceID(_ id: String) {
        var root = formData.object ?? [:]
        var reference = root["reference"]?.object ?? [:]
        reference["discussion_id"] = .string(id)
        root["reference"] = .object(properties: reference)
        formData = .object(properties: root)
    }

    private func restoringStoredSettings(in data: FormData, form: PrecheckFormDTO) -> FormData {
        var root = data.object ?? [:]
        var settings = root["settings"]?.object ?? [:]
        let defaults = settings
        let metadata = SettingsMetadata(form: form)

        let type = metadata.validType(storedType) ?? defaults.stringValue("type") ?? metadata.firstType
        if let type {
            settings["type"] = .string(type)
        }

        let template = metadata.validTemplate(storedTemplate, for: type)
            ?? metadata.validTemplate(defaults.stringValue("template"), for: type)
            ?? metadata.firstTemplate(for: type)
        if let template {
            settings["template"] = .string(template)
        }

        let discussants = metadata.clampedDiscussants(
            storedDiscussants > 0
                ? storedDiscussants
                : (legacyStoredDiscussants > 0 ? legacyStoredDiscussants : defaults.intValue("discussants"))
        )
        settings["discussants"] = .number(Double(discussants))

        let language = metadata.validLanguage(storedLanguage)
            ?? metadata.validLanguage(legacyStoredLanguage)
            ?? metadata.validLanguage(defaults.stringValue("language"))
            ?? metadata.firstLanguage
        if let language {
            settings["language"] = .string(language)
        }

        let generateCover = hasStoredSettings ? storedGenerateCover : (defaults.boolValue("generate_cover") ?? false)
        settings["generate_cover"] = .boolean(generateCover)

        root["settings"] = .object(properties: settings)
        return .object(properties: root)
    }

    private func persistSettings(from data: FormData) {
        guard let settings = data.object?["settings"]?.object else { return }
        storedType = settings.stringValue("type") ?? ""
        storedTemplate = settings.stringValue("template") ?? ""
        storedDiscussants = settings.intValue("discussants")
        storedLanguage = settings.stringValue("language") ?? ""
        storedGenerateCover = settings.boolValue("generate_cover") ?? false
        hasStoredSettings = true
    }

    /// Creates the placeholder discussion (fast), then hands it to the caller,
    /// which navigates to the plan page where the plan is streamed in. Creating
    /// the row first means the discussion is saved even if the planning stream is
    /// later interrupted.
    ///
    /// The whole form value tree is posted verbatim; the backend owns every key,
    /// so this view never reads or transforms individual fields.
    private func plan() {
        isPlanning = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        let submitted = formData
        // The parent discussion is carried inside the form (reference.discussion_id),
        // so it no longer needs to be passed separately.
        Task {
            do {
                let created = try await api.createDiscussion(form: submitted)
                isPlanning = false
                dismiss()
                onPlanned(created)
            } catch {
                isPlanning = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

private struct SettingsMetadata {
    private let types: [String]
    private let templates: [String]
    private let templatesByType: [String: [String]]
    private let languages: [String]
    private let discussantsRange: ClosedRange<Int>

    var firstType: String? { types.first }
    var firstLanguage: String? { languages.first }

    init(form: PrecheckFormDTO) {
        let settingsProperties = Self.settingsProperties(from: form.schema)
        let typeSchema = settingsProperties["type"]?.objectValue
        let templateSchema = settingsProperties["template"]?.objectValue
        let discussantsSchema = settingsProperties["discussants"]?.objectValue
        let languageSchema = settingsProperties["language"]?.objectValue

        types = Self.stringArray(typeSchema?["enum"])
        templates = Self.stringArray(templateSchema?["enum"])
        templatesByType = Self.stringArrayMap(templateSchema?["x-enum-by-type"])
        languages = Self.stringArray(languageSchema?["enum"])

        let lower = Self.intValue(discussantsSchema?["minimum"]) ?? 2
        let upper = Self.intValue(discussantsSchema?["maximum"]) ?? 6
        discussantsRange = lower <= upper ? lower...upper : 2...6
    }

    func validType(_ value: String?) -> String? {
        valid(value, in: types)
    }

    func validLanguage(_ value: String?) -> String? {
        valid(value, in: languages)
    }

    func validTemplate(_ value: String?, for type: String?) -> String? {
        valid(value, in: templateOptions(for: type))
    }

    func firstTemplate(for type: String?) -> String? {
        templateOptions(for: type).first
    }

    func clampedDiscussants(_ value: Int) -> Int {
        min(max(value, discussantsRange.lowerBound), discussantsRange.upperBound)
    }

    private func templateOptions(for type: String?) -> [String] {
        if let type, let scoped = templatesByType[type], !scoped.isEmpty {
            return scoped
        }
        return templates
    }

    private func valid(_ value: String?, in options: [String]) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !trimmed.isEmpty else { return nil }
        guard !options.isEmpty else { return trimmed }
        return options.contains(trimmed) ? trimmed : nil
    }

    private static func settingsProperties(from schema: [String: AnyCodable]) -> [String: AnyCodable] {
        guard case let .object(rootProperties)? = schema["properties"],
              case let .object(settingsSchema)? = rootProperties["settings"],
              case let .object(settingsProperties)? = settingsSchema["properties"] else {
            return [:]
        }
        return settingsProperties
    }

    private static func stringArray(_ value: AnyCodable?) -> [String] {
        guard case let .array(values)? = value else { return [] }
        return values.compactMap {
            guard case let .string(value) = $0 else { return nil }
            return value
        }
    }

    private static func stringArrayMap(_ value: AnyCodable?) -> [String: [String]] {
        guard case let .object(groups)? = value else { return [:] }
        var result: [String: [String]] = [:]
        for (key, value) in groups {
            result[key] = stringArray(value)
        }
        return result
    }

    private static func intValue(_ value: AnyCodable?) -> Int? {
        switch value {
        case let .int(value):
            return value
        case let .double(value):
            return Int(value)
        default:
            return nil
        }
    }
}

private extension AnyCodable {
    var objectValue: [String: AnyCodable]? {
        guard case let .object(value) = self else { return nil }
        return value
    }
}

private extension [String: FormData] {
    func stringValue(_ key: String) -> String? {
        self[key]?.string
    }

    func intValue(_ key: String) -> Int {
        Int(self[key]?.number ?? 0)
    }

    func boolValue(_ key: String) -> Bool? {
        self[key]?.boolean
    }
}
