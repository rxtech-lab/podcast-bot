import AuthenticationServices
import Kingfisher
import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

/// Coordinates the deep-link-driven parent-discussion picker for the backend-rendered
/// New Discussion form.
///
/// The form is rendered by `JSONSchemaForm`, so the `discussionPicker` widget cannot
/// present a sheet that writes back into the view's `formData` on its own. Instead the
/// widget hands this coordinator the backend-declared deep link plus a write-back
/// closure; `NewDiscussionView` owns the actual sheet and reports the selection here,
/// which both updates the form value and caches the chosen reference for display.
///
/// This is deliberately a form-local coordinator rather than the global
/// `DeepLinkRouter`: the global router presents at the app root and cannot reach the
/// form's state, whereas the parent selection must flow back into `formData`.
@Observable
@MainActor
final class NewDiscussionFormCoordinator {
    /// Host of the deep link the backend uses for the parent-discussion picker
    /// (`debatepod://discussion-picker`).
    static let pickerHost = "discussion-picker"

    /// Whether the parent-discussion picker sheet should be presented.
    var isPresenting = false

    /// The field id (e.g. `root_reference_discussion_id`) currently driving the picker.
    private(set) var activeFieldID: String?

    /// Cached references keyed by field id, used by the widget to show a title for the
    /// current selection (including pre-filled and restored values).
    private var selections: [String: PodcastReference] = [:]

    /// Write-back closure supplied by the active widget, applied when a selection is made.
    private var onSelect: ((PodcastReference?) -> Void)?

    /// Id of the active field's current selection, used to mark the checked row.
    var activeSelectionID: String? {
        activeFieldID.flatMap { selections[$0]?.id }
    }

    /// The reference currently selected for `fieldID`, if known.
    func reference(for fieldID: String) -> PodcastReference? {
        selections[fieldID]
    }

    /// Cache a reference for display (e.g. a pre-filled parent, or one resolved by id).
    func cache(_ reference: PodcastReference, for fieldID: String) {
        selections[fieldID] = reference
    }

    /// Interpret a backend-declared deep link and, when it targets the picker, present it.
    /// `onSelect` receives the chosen reference (or nil when cleared) so the widget can
    /// write the id back into its form value.
    func open(
        deepLink: String,
        fieldID: String,
        onSelect: @escaping (PodcastReference?) -> Void
    ) {
        guard let url = URL(string: deepLink), url.host == Self.pickerHost else { return }
        activeFieldID = fieldID
        self.onSelect = onSelect
        isPresenting = true
    }

    /// Report the user's choice from the picker sheet: cache it, write it back into the
    /// form value, and dismiss.
    func complete(with reference: PodcastReference?) {
        if let activeFieldID {
            if let reference {
                selections[activeFieldID] = reference
            } else {
                selections.removeValue(forKey: activeFieldID)
            }
        }
        onSelect?(reference)
        finish()
    }

    /// Clear a selection without opening the picker (the row's clear button).
    func clear(fieldID: String) {
        selections.removeValue(forKey: fieldID)
    }

    /// Dismiss the picker without changing the selection.
    func cancel() {
        finish()
    }

    private func finish() {
        isPresenting = false
        activeFieldID = nil
        onSelect = nil
    }
}

extension Discussion {
    /// Lightweight reference used as a parent discussion for follow-up planning.
    var podcastReference: PodcastReference {
        PodcastReference(id: id, title: displayTitle, topic: topic)
    }
}

/// Coordinates the deep-link-driven attachments picker for the backend-rendered
/// New Discussion form.
///
/// Like `NewDiscussionFormCoordinator`, this exists because a `JSONSchemaForm`
/// widget cannot reliably own presentation/upload state: the form re-creates the
/// widget (wrapped in `AnyView`) on every edit, which would drop in-flight upload
/// `@State` and dismiss a half-open importer. So the live `attachments` list and
/// the presentation flags live here (owned by `NewDiscussionView`), the widget's
/// menu opens a source through the backend-declared deep link, and the parent view
/// hosts the actual file/photo/Notion pickers — mirroring the parent-discussion
/// picker. Ready attachments are written back into the form value via `onReady`.
@Observable
@MainActor
final class NewDiscussionAttachmentsCoordinator {
    /// Host of the deep link the backend uses for the attachments picker
    /// (`debatepod://attachment-picker`).
    static let pickerHost = "attachment-picker"

    /// Live picked attachments with upload status, rendered as chips by the widget.
    private(set) var attachments: [PendingAttachment] = []

    /// Presentation flags bound to the parent view's pickers.
    var showingImporter = false
    var showingPhotos = false
    var showingNotionPicker = false
    var selectedPhotos: [PhotosPickerItem] = []

    /// Attachment opened for preview by tapping its chip. Parent-hosted (like
    /// the pickers) because the form widget is re-created on every edit.
    var previewedAttachmentID: UUID?

    var previewedAttachment: PendingAttachment? {
        previewedAttachmentID.flatMap { id in attachments.first { $0.id == id } }
    }

    func preview(_ id: UUID) { previewedAttachmentID = id }

    /// True when any attachment would be silently missing from the plan
    /// (failed upload/parse, or ready with no readable content) — submission
    /// is blocked until the user removes it.
    var hasProblemAttachments: Bool { attachments.contains { $0.hasProblem } }

    /// Notion connection status, loaded once so the menu can offer connect vs pick.
    private(set) var notionConnected = false
    private(set) var notionStatusLoaded = false

    /// True while any attachment is still uploading — used to block submission.
    var isUploading: Bool { attachments.isUploading }

    private var fieldID: String?
    private var onReady: (([Attachment]) -> Void)?
    private var api: APIClient?
    private var notionStatusLoading = false
    private var notionAuthSession: ASWebAuthenticationSession?
    private let presentationProvider = AttachmentWebAuthPresentationContextProvider()

    /// Supplied by the widget so uploads can run; cheap to set repeatedly.
    func configure(api: APIClient) { self.api = api }

    /// Registered by the widget so completed uploads flow back into `formData`.
    func bind(fieldID: String, onReady: @escaping ([Attachment]) -> Void) {
        self.fieldID = fieldID
        self.onReady = onReady
    }

    /// Interpret the backend deep link and present the picker for `source`.
    func open(deepLink: String, source: AttachmentSource) {
        guard let url = URL(string: deepLink), url.host == Self.pickerHost else { return }
        switch source {
        case .files:
            showingImporter = true
        case .photos:
            showingPhotos = true
        case .notion:
            if notionConnected {
                showingNotionPicker = true
            } else {
                connectNotion()
            }
        }
    }

    func remove(_ id: UUID) {
        attachments.removeAll { $0.id == id }
        syncReady()
    }

    // MARK: - Imports (mirrors AttachmentsRow, but parent-owned for the form)

    /// Reads each file's bytes within its security scope, then uploads.
    func importFiles(_ urls: [URL]) {
        guard let api else { return }
        for url in urls {
            let access = url.startAccessingSecurityScopedResource()
            let data = try? Data(contentsOf: url)
            if access { url.stopAccessingSecurityScopedResource() }
            let filename = url.lastPathComponent
            guard let data else {
                attachments.append(PendingAttachment(filename: filename, status: .failed(String(localized: "Couldn't read file", comment: "Attachment error when a picked file's bytes can't be read")), markdown: nil))
                continue
            }
            let mime = url.attachmentMIMEType
            let pending = PendingAttachment(filename: filename, status: .uploading, markdown: nil, mimeType: mime)
            attachments.append(pending)
            let id = pending.id
            Task {
                do {
                    let resp = try await api.uploadFile(data: data, filename: filename, mimeType: mime)
                    updateStatus(id, status: .ready, response: resp)
                } catch {
                    let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                    updateStatus(id, status: .failed(message), response: nil)
                }
            }
        }
    }

    func importPhotos(_ items: [PhotosPickerItem]) {
        guard let api, !items.isEmpty else { return }
        for item in items {
            let utType = item.supportedContentTypes.first
            let ext = utType?.preferredFilenameExtension ?? "jpg"
            let mime = utType?.preferredMIMEType ?? "image/jpeg"
            let filename = "Photo-\(UUID().uuidString.prefix(6)).\(ext)"
            let pending = PendingAttachment(filename: filename, status: .uploading, markdown: nil, mimeType: mime)
            attachments.append(pending)
            let id = pending.id
            Task {
                do {
                    guard let data = try await item.loadTransferable(type: Data.self) else {
                        updateStatus(id, status: .failed(String(localized: "Couldn't read photo", comment: "Attachment error when a picked photo's bytes can't be loaded")), response: nil)
                        return
                    }
                    let resp = try await api.uploadFile(data: data, filename: filename, mimeType: mime)
                    updateStatus(id, status: .ready, response: resp)
                } catch {
                    let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                    updateStatus(id, status: .failed(message), response: nil)
                }
            }
        }
        selectedPhotos = []
    }

    func importNotionPages(_ pages: [NotionPageDTO]) {
        guard let api, !pages.isEmpty else { return }
        for page in pages {
            let filename = "\(page.title.isEmpty ? "Notion page" : page.title).md"
            let pending = PendingAttachment(filename: filename, status: .uploading, markdown: nil, url: page.url, mimeType: "text/markdown+notion")
            attachments.append(pending)
            let id = pending.id
            Task {
                do {
                    let resp = try await api.notionPageAttachment(pageID: page.id)
                    updateStatus(id, status: .ready, response: resp)
                } catch {
                    let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                    updateStatus(id, status: .failed(message), response: nil)
                }
            }
        }
    }

    private func updateStatus(_ id: UUID, status: PendingAttachment.Status, response: UploadResponse?) {
        guard let idx = attachments.firstIndex(where: { $0.id == id }) else { return }
        attachments[idx].status = status
        attachments[idx].markdown = response?.markdown
        attachments[idx].url = response?.url
        attachments[idx].mimeType = response?.mimeType
        attachments[idx].key = response?.key
        syncReady()
    }

    /// Push the ready attachments into the form value.
    private func syncReady() {
        onReady?(attachments.apiAttachments)
    }

    /// E2E only: inject a tiny ready image attachment as a data URL. The
    /// hermetic backend has no S3 and the simulator's photo picker is
    /// out-of-process, so this bypasses only the upload — the attachment then
    /// travels the real path through the form, the server's persisted planning
    /// turn, and the (fake) model as an image part.
    func importE2ESampleImage() {
        attachments.append(PendingAttachment(
            filename: "E2E-Sample.png",
            status: .ready,
            markdown: nil,
            url: Self.e2eSampleImageDataURL,
            mimeType: "image/png"
        ))
        syncReady()
    }

    /// A 1×1 PNG, small enough to persist in the planning turn verbatim.
    private static let e2eSampleImageDataURL =
        "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

    // MARK: - Notion connection

    func loadNotionStatus() async {
        guard !notionStatusLoaded, !notionStatusLoading, let api else { return }
        notionStatusLoading = true
        defer {
            notionStatusLoading = false
            notionStatusLoaded = true
        }
        do {
            notionConnected = try await api.notionStatus().connected
        } catch {
            notionConnected = false
        }
    }

    private func connectNotion() {
        guard let api else { return }
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { [weak self] _, error in
                    Task { @MainActor in
                        guard let self else { return }
                        self.notionAuthSession = nil
                        guard error == nil else { return }
                        do {
                            self.notionConnected = try await api.notionStatus().connected
                            self.notionStatusLoaded = true
                            if self.notionConnected {
                                self.showingNotionPicker = true
                            }
                        } catch {
                            self.notionConnected = false
                            self.notionStatusLoaded = true
                        }
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                notionAuthSession = session
                session.start()
            } catch {
                let message = (error as? APIError)?.errorDescription ?? error.localizedDescription
                attachments.append(PendingAttachment(filename: "Notion", status: .failed(message), markdown: nil))
            }
        }
    }
}

private extension URL {
    var attachmentMIMEType: String {
        UTType(filenameExtension: pathExtension)?.preferredMIMEType ?? "application/octet-stream"
    }
}

/// Searchable sheet listing the user's existing discussions to pick a parent.
/// Ported from the original hand-built New Discussion form; reports the selection
/// through `onSelect` rather than a binding so it can be driven by the form coordinator.
struct ReferencePodcastPickerSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss
    /// Id of the currently selected discussion, used to show the checkmark.
    let selectedID: String?
    let onSelect: (PodcastReference?) -> Void
    @State private var discussions: [Discussion] = []
    @State private var query = ""
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var searchTask: Task<Void, Never>?

    var body: some View {
        NavigationStack {
            ZStack {
                Theme.background.ignoresSafeArea()
                List {
                    if isLoading && discussions.isEmpty {
                        HStack {
                            Spacer()
                            ProgressView().tint(Theme.accent)
                            Spacer()
                        }
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                    ForEach(discussions) { discussion in
                        Button {
                            onSelect(discussion.podcastReference)
                            dismiss()
                        } label: {
                            HStack(spacing: 12) {
                                DiscussionCoverThumbnail(discussion: discussion, size: 44)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(discussion.displayTitle)
                                        .font(.subheadline.weight(.semibold))
                                        .foregroundStyle(.primary)
                                        .lineLimit(1)
                                    Text(discussion.topic)
                                        .font(.caption)
                                        .foregroundStyle(Theme.secondaryText)
                                        .lineLimit(2)
                                }
                                Spacer()
                                if selectedID == discussion.id {
                                    Image(systemName: "checkmark.circle.fill")
                                        .foregroundStyle(Theme.accent)
                                }
                            }
                            .padding(.vertical, 4)
                        }
                        .buttonStyle(.plain)
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                    }
                }
                .listStyle(.plain)
                .scrollContentBackground(.hidden)
                if let errorMessage, discussions.isEmpty {
                    ContentUnavailableView("Could not load stations",
                                           systemImage: "exclamationmark.triangle",
                                           description: Text(errorMessage))
                } else if !isLoading && discussions.isEmpty {
                    ContentUnavailableView("No Station",
                                           systemImage: "waveform",
                                           description: Text("Create or search for a podcast to use as follow-up context."))
                }
            }
            .navigationTitle("Parent Station")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search stations")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .task {
                await load()
            }
            .onChange(of: query) { _, value in
                searchTask?.cancel()
                searchTask = Task {
                    try? await Task.sleep(for: .milliseconds(250))
                    guard !Task.isCancelled else { return }
                    await load(search: value)
                }
            }
            .onDisappear {
                searchTask?.cancel()
            }
        }
        #if os(macOS)
        .frame(minHeight: 480)
        #endif
    }

    @MainActor
    private func load(search: String? = nil) async {
        isLoading = true
        defer { isLoading = false }
        do {
            discussions = try await APIClient(tokens: auth).parentPodcasts(limit: 50, query: search ?? query)
            errorMessage = nil
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// Square cover thumbnail for a discussion row (image, gradient, or waveform fallback).
struct DiscussionCoverThumbnail: View {
    let discussion: Discussion
    let size: CGFloat

    var body: some View {
        Group {
            if let url = discussion.cover?.renderableImageURL {
                KFImage.url(url)
                    .placeholder {
                        fallback
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFill()
            } else if let cover = discussion.cover, cover.hasGradient {
                LinearGradient(colors: [color(cover.gradientStart), color(cover.gradientEnd)],
                               startPoint: .topLeading,
                               endPoint: .bottomTrailing)
            } else {
                fallback
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: 8))
    }

    private var fallback: some View {
        ZStack {
            LinearGradient(colors: [Theme.accent.opacity(0.75), Color.orange.opacity(0.72)],
                           startPoint: .topLeading,
                           endPoint: .bottomTrailing)
            Image(systemName: "waveform")
                .font(.system(size: size * 0.38, weight: .semibold))
                .foregroundStyle(.white)
        }
    }

    private func color(_ hex: String?) -> Color {
        guard let hex else { return Theme.accent }
        let trimmed = hex.trimmingCharacters(in: CharacterSet(charactersIn: "# "))
        guard trimmed.count == 6, let value = Int(trimmed, radix: 16) else {
            return Theme.accent
        }
        let red = Double((value >> 16) & 0xff) / 255.0
        let green = Double((value >> 8) & 0xff) / 255.0
        let blue = Double(value & 0xff) / 255.0
        return Color(red: red, green: green, blue: blue)
    }
}
