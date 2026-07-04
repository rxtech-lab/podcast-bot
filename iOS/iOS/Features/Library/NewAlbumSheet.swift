import JSONSchema
import JSONSchemaForm
import SwiftUI

/// The library toolbar's "+ > New Album" sheet: renders the server-provided
/// new-album form (GET /api/precheck, `new_album`) and posts the values
/// verbatim to create an empty album. The created album is handed back so the
/// library can open it right away for adding episodes (an empty album has no
/// row in the library list).
struct NewAlbumSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    var onCreated: (AlbumDTO) -> Void

    @State private var form: PrecheckFormDTO?
    @State private var formSchema: JSONSchema?
    @State private var formSchemaJSON: String?
    @State private var formUISchema: [String: Any]?
    @State private var formData = FormData.object(properties: [:])
    @State private var pickerCoordinator = NewDiscussionFormCoordinator()
    @State private var attachmentsCoordinator = NewDiscussionAttachmentsCoordinator()
    @State private var isLoadingForm = false
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                content
            }
            .navigationTitle(form?.title ?? "New Album")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(form?.cancelTitle ?? "Cancel") { dismiss() }
                        .disabled(isSubmitting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: create) {
                        if isSubmitting {
                            ProgressView()
                        } else {
                            Text(form?.submitTitle ?? "Create")
                        }
                    }
                    .disabled(form == nil || isLoadingForm || isSubmitting)
                    .accessibilityIdentifier("newAlbum.create")
                }
            }
        }
        .task { await loadForm() }
        .accessibilityIdentifier("newAlbum.sheet")
    }

    private var content: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if isLoadingForm && form == nil {
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
                }

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isSubmitting {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(form?.loadingTitle ?? "Creating album...")
                    }
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
        .scrollDismissesKeyboard(.interactively)
        .disabled(isSubmitting)
    }

    @MainActor
    private func loadForm() async {
        guard form == nil else { return }
        isLoadingForm = true
        defer { isLoadingForm = false }
        do {
            let response = try await APIClient(tokens: auth).precheck()
            guard let albumForm = response.newAlbum?.form else {
                errorMessage = String(localized: "Album creation is not available on this server.")
                return
            }
            apply(albumForm)
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    @MainActor
    private func apply(_ form: PrecheckFormDTO) {
        self.form = form
        formSchemaJSON = form.schemaJSONString
        formSchema = formSchemaJSON.flatMap { try? JSONSchema(jsonString: $0) }
        formUISchema = form.uiSchemaDictionary
        formData = form.decodedInitialData
    }

    /// Posts the whole form value tree verbatim; the backend owns every key,
    /// so this view never reads or transforms individual fields.
    private func create() {
        isSubmitting = true
        errorMessage = nil
        let submitted = formData
        Task {
            do {
                let album = try await APIClient(tokens: auth).createAlbum(form: submitted)
                isSubmitting = false
                onCreated(album)
                dismiss()
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
