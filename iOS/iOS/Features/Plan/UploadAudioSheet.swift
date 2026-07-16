import JSONSchema
import JSONSchemaForm
import OSLog
import SwiftUI
import UniformTypeIdentifiers

private let uploadAudioLog = Logger(subsystem: "com.debatebot.ios", category: "UploadAudio")

/// Upload Own Audio: renders the server-provided form (audio file + max
/// speakers), then asks the backend to create a discussion and transcribe the
/// uploaded audio. The caller navigates to the plan chat, which shows the
/// transcription progress and then the AI transcript review.
struct UploadAudioSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    /// Called once the discussion is created and transcription has started.
    var onCreated: (Discussion) -> Void = { _ in }
    /// Pre-picked audio (e.g. an in-app recording) uploaded automatically
    /// instead of the user choosing a file. `initialFilename` becomes the
    /// server-side filename (it titles the episode).
    var initialFileURL: URL?
    var initialFilename: String?

    @State private var precheckForm: PrecheckFormDTO?
    @State private var formSchema: JSONSchema?
    @State private var formSchemaJSON: String?
    @State private var formUISchema: [String: Any]?
    @State private var formData = FormData.object(properties: [:])
    @State private var audioCoordinator = UploadAudioCoordinator()
    @State private var isLoadingForm = false
    @State private var isSubmitting = false
    @State private var errorMessage: String?
    @State private var unavailable = false

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                form
            }
            .navigationTitle(precheckForm?.title ?? String(localized: "Upload Own Audio", comment: "Upload own audio sheet title"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(precheckForm?.cancelTitle ?? "Cancel") { dismiss() }
                        .disabled(isSubmitting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: submit) {
                        if isSubmitting {
                            ProgressView()
                        } else {
                            Text(precheckForm?.submitTitle ?? "Transcribe")
                        }
                    }
                    .disabled(!canSubmit)
                    .accessibilityIdentifier("uploadAudio.submit")
                }
            }
        }
        .interactiveDismissDisabled(isSubmitting || audioCoordinator.isUploading)
        .fileImporter(
            isPresented: importerBinding,
            allowedContentTypes: [.audio],
            allowsMultipleSelection: false
        ) { result in
            if case let .success(urls) = result, let url = urls.first {
                audioCoordinator.importFile(url)
            }
        }
        .task {
            await loadPrecheck()
            if let url = initialFileURL, !unavailable {
                audioCoordinator.stageInitialFile(url: url, filename: initialFilename ?? url.lastPathComponent)
            }
        }
    }

    private var importerBinding: Binding<Bool> {
        Binding(
            get: { audioCoordinator.showingImporter },
            set: { audioCoordinator.showingImporter = $0 }
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
                } else if unavailable {
                    ContentUnavailableView(
                        String(localized: "Not available", comment: "Upload own audio unavailable title"),
                        systemImage: "waveform.slash",
                        description: Text(String(localized: "Uploading your own audio is not enabled for your account.",
                                                 comment: "Upload own audio unavailable description"))
                    )
                    .padding(.vertical, 24)
                } else if let formSchema, let formSchemaJSON {
                    if let description = precheckForm?.description, !description.isEmpty {
                        Text(description)
                            .font(.subheadline)
                            .foregroundStyle(Theme.secondaryText)
                    }
                    JSONSchemaForm(
                        schema: formSchema,
                        uiSchema: formUISchema,
                        formData: $formData,
                        schemaJSON: formSchemaJSON,
                        showSubmitButton: false,
                        widgets: NewDiscussionFormUI.uploadAudioWidgets(
                            rootFormData: $formData,
                            audioCoordinator: audioCoordinator
                        ),
                        templates: NewDiscussionFormUI.templates()
                    )
                } else {
                    EmptyView()
                }

                if let errorMessage {
                    Text(errorMessage).font(.footnote).foregroundStyle(.red)
                }

                if isSubmitting {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text(precheckForm?.loadingTitle ?? String(localized: "Starting transcription...", comment: "Upload own audio submit progress"))
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

    private var canSubmit: Bool {
        precheckForm != nil
            && !isLoadingForm
            && !isSubmitting
            && audioCoordinator.isReady
    }

    @MainActor
    private func loadPrecheck() async {
        guard precheckForm == nil, !unavailable else { return }
        isLoadingForm = true
        defer { isLoadingForm = false }
        do {
            let response = try await APIClient(tokens: auth).precheck()
            guard let form = response.uploadAudio?.form else {
                unavailable = true
                return
            }
            precheckForm = form
            formSchemaJSON = form.schemaJSONString
            formSchema = formSchemaJSON.flatMap { try? JSONSchema(jsonString: $0) }
            formUISchema = form.uiSchemaDictionary
            formData = form.decodedInitialData
            errorMessage = nil
        } catch {
            uploadAudioLog.error("precheck failed error=\(error.localizedDescription, privacy: .public)")
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// Posts the whole form value tree verbatim; the server owns every key.
    private func submit() {
        isSubmitting = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        let submitted = formData
        Task {
            do {
                let created = try await api.createUploadAudioDiscussion(form: submitted)
                isSubmitting = false
                dismiss()
                onCreated(created)
            } catch {
                isSubmitting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
