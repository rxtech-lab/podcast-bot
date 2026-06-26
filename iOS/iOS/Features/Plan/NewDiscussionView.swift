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
        formSchemaJSON = jsonString(from: form.schema)
        formSchema = formSchemaJSON.flatMap { try? JSONSchema(jsonString: $0) }
        formUISchema = foundationDictionary(from: form.uiSchema)
        formData = decodedFormData(from: form.initialData)
        // When planning from an existing podcast, pre-fill the parent into the form
        // and cache it so the picker row shows its title.
        if let initialReference {
            setFormReferenceID(initialReference.id)
            pickerCoordinator.cache(initialReference, for: referenceFieldID)
        }
    }

    /// Writes a parent discussion id into the form's `reference.discussion_id` value.
    private func setFormReferenceID(_ id: String) {
        var root = formData.object ?? [:]
        var reference = root["reference"]?.object ?? [:]
        reference["discussion_id"] = .string(id)
        root["reference"] = .object(properties: reference)
        formData = .object(properties: root)
    }

    private func jsonString(from value: some Encodable) -> String? {
        guard let data = try? JSONEncoder().encode(value) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private func foundationDictionary(from value: [String: AnyCodable]?) -> [String: Any]? {
        guard let value,
              let data = try? JSONEncoder().encode(value),
              let object = try? JSONSerialization.jsonObject(with: data),
              let dictionary = object as? [String: Any]
        else {
            return nil
        }
        return dictionary
    }

    private func decodedFormData(from value: [String: AnyCodable]?) -> FormData {
        guard let value,
              let data = try? JSONEncoder().encode(value),
              let decoded = try? JSONDecoder().decode(FormData.self, from: data)
        else {
            return .object(properties: [:])
        }
        return decoded
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

