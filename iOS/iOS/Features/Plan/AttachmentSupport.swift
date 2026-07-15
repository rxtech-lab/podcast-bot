import Kingfisher
import SwiftUI
import PhotosUI
import UniformTypeIdentifiers
import AuthenticationServices

/// A file the user picked to attach, tracked through upload and server
/// finalization. Documents carry markdown; images carry a direct URL.
struct PendingAttachment: Identifiable {
    enum Status: Equatable {
        case uploading
        case ready
        case failed(String)
    }

    let id = UUID()
    let filename: String
    var status: Status
    var markdown: String?
    var url: String?
    var mimeType: String?
    var key: String?

    var apiAttachment: Attachment? {
        guard status == .ready else { return nil }
        if let markdown, !markdown.isEmpty {
            return Attachment(filename: filename, markdown: markdown, url: url, mimeType: mimeType, key: key)
        }
        if let url, !url.isEmpty, mimeType?.hasPrefix("image/") == true {
            return Attachment(filename: filename, markdown: nil, url: url, mimeType: mimeType, key: key)
        }
        return nil
    }
}

extension Array where Element == PendingAttachment {
    /// Ready-to-send attachments.
    var apiAttachments: [Attachment] { compactMap(\.apiAttachment) }
    /// True while any attachment is still uploading/parsing.
    var isUploading: Bool { contains { $0.status == .uploading } }
}

struct AttachmentPreviewItem: Identifiable, Hashable {
    var attachment: Attachment

    var id: String {
        [
            attachment.filename,
            attachment.url ?? "",
            attachment.mimeType ?? "",
            String(attachment.markdown?.hashValue ?? 0),
        ].joined(separator: "|")
    }
}

struct AttachmentPreviewSheet: View {
    @Environment(\.dismiss) private var dismiss
    let attachment: Attachment

    var body: some View {
        NavigationStack {
            Group {
                if attachment.isImageAttachment, let url = attachment.previewURL {
                    imagePreview(url)
                } else if attachment.isAudioAttachment, let url = attachment.previewURL {
                    audioPreview(url)
                } else if let markdown = attachment.markdown?.trimmingCharacters(in: .whitespacesAndNewlines),
                          !markdown.isEmpty {
                    ScrollView {
                        MarkdownText(markdown)
                            .font(.body)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(20)
                    }
                } else {
                    fallbackPreview
                }
            }
            .navigationTitle(attachment.displayName)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private func imagePreview(_ url: URL) -> some View {
        GeometryReader { proxy in
            ScrollView([.horizontal, .vertical]) {
                KFImage.url(url)
                    .placeholder {
                        ProgressView()
                            .frame(width: proxy.size.width, height: proxy.size.height)
                    }
                    .cancelOnDisappear(false)
                    .retry(maxCount: 3, interval: .seconds(1))
                    .resizable()
                    .scaledToFit()
                    .frame(minWidth: proxy.size.width, minHeight: proxy.size.height)
            }
            .background(Theme.background)
        }
    }

    private func audioPreview(_ url: URL) -> some View {
        VStack(spacing: 20) {
            Image(systemName: "waveform.circle.fill")
                .font(.system(size: 72))
                .foregroundStyle(Theme.accent)
            Text(attachment.displayName)
                .font(.headline)
                .multilineTextAlignment(.center)
            VoiceMessageControl(urlString: url.absoluteString, isUser: false)
                .padding(.horizontal, 24)
                .padding(.vertical, 16)
                .background(Theme.accent.opacity(0.1), in: .rect(cornerRadius: 18))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
        .background(Theme.background)
    }

    private var fallbackPreview: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(attachment.displayName, systemImage: attachment.iconName)
                .font(.headline)
            if let mimeType = attachment.mimeType, !mimeType.isEmpty {
                Text(mimeType)
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
            }
            if let url = attachment.previewURL {
                Link(url.absoluteString, destination: url)
                    .font(.callout)
                    .lineLimit(3)
            } else {
                Text("No preview is available for this attachment.")
                    .font(.callout)
                    .foregroundStyle(Theme.secondaryText)
            }
            Spacer()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(20)
        .background(Theme.background)
    }
}

extension Attachment {
    var displayName: String {
        let trimmed = filename.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? String(localized: "Attachment", comment: "Fallback name for an attached file") : trimmed
    }

    var previewURL: URL? {
        guard let url, !url.isEmpty else { return nil }
        return URL(string: url)
    }

    var isImageAttachment: Bool {
        if mimeType?.lowercased().hasPrefix("image/") == true {
            return true
        }
        let name = filename.lowercased()
        return [".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".heif"].contains { name.hasSuffix($0) }
    }

    var isAudioAttachment: Bool {
        if mimeType?.lowercased().hasPrefix("audio/") == true {
            return true
        }
        let name = filename.lowercased()
        return [".mp3", ".m4a", ".mp4", ".aac", ".wav", ".ogg", ".opus", ".flac", ".aiff", ".aif"]
            .contains { name.hasSuffix($0) }
    }

    var iconName: String {
        if isImageAttachment { return "photo.fill" }
        if isAudioAttachment { return "waveform.circle.fill" }
        if mimeType?.contains("notion") == true { return "doc.text.magnifyingglass" }
        return "doc.text.fill"
    }
}

/// File types markitdown can parse. `.item` is included as a permissive
/// fallback so users aren't blocked when a specific UTI isn't listed.
let attachmentContentTypes: [UTType] = {
    var types: [UTType] = [.pdf, .plainText, .rtf, .text, .image, .html, .commaSeparatedText, .item]
    for id in [
        "org.openxmlformats.wordprocessingml.document",   // .docx
        "org.openxmlformats.presentationml.presentation", // .pptx
        "org.openxmlformats.spreadsheetml.sheet",         // .xlsx
        "com.microsoft.word.doc",
    ] {
        if let t = UTType(id) { types.append(t) }
    }
    return types
}()

/// A paperclip button + a horizontal row of attachment chips. Handles file
/// import, uploads each file via a presigned URL, and reflects status in
/// `attachments`. Reusable in
/// the new-discussion form and the plan-edit chat bar.
struct AttachmentsRow: View {
    @Environment(AuthManager.self) private var auth
    @Binding var attachments: [PendingAttachment]
    /// Compact hides the "Attach files" label, showing only the paperclip — for
    /// the chat input bar.
    var compact: Bool = false
    /// Render the chip row of picked files.
    var showsChips: Bool = true
    /// Render the attach (paperclip) button.
    var showsButton: Bool = true
    /// Grouped renders the attach row without its own glass background so it can
    /// share one card with another control (e.g. the language menu).
    var grouped: Bool = false
    var title: String = "Attach files"
    var subtitle: String? = "PDF, Word, slides, images"
    var systemImage: String = "paperclip"

    @State private var showingImporter = false
    @State private var showingPhotos = false
    @State private var showingNotionPicker = false
    @State private var notionConnected = false
    @State private var notionStatusLoaded = false
    @State private var notionStatusLoading = false
    @State private var notionAuthSession: ASWebAuthenticationSession?
    @State private var presentationProvider = AttachmentWebAuthPresentationContextProvider()
    @State private var selectedPhotos: [PhotosPickerItem] = []

    var body: some View {
        VStack(alignment: .leading, spacing: grouped ? 0 : 8) {
            if showsChips && !attachments.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(attachments) { att in
                            chip(att)
                        }
                    }
                    .padding(.vertical, 2)
                    .padding(.horizontal, grouped ? 12 : 0)
                    .padding(.top, grouped ? 12 : 0)
                }
            }
            if showsButton {
                Menu {
                    Button {
                        guard notionStatusLoaded else { return }
                        if notionConnected {
                            showingNotionPicker = true
                        } else {
                            connectNotion()
                        }
                    } label: {
                        if notionStatusLoaded {
                            Label(notionConnected ? "Pick Notion Page" : "Connect to Notion",
                                  systemImage: notionConnected ? "doc.text.magnifyingglass" : "link.badge.plus")
                        } else {
                            Label("Checking Notion", systemImage: "hourglass")
                        }
                    }
                    .disabled(!notionStatusLoaded)
                    Button {
                        showingPhotos = true
                    } label: {
                        Label("Photo Library", systemImage: "photo.on.rectangle")
                    }
                    Button {
                        showingImporter = true
                    } label: {
                        Label("Files", systemImage: "folder")
                    }
                } label: {
                    if compact {
                        Image(systemName: "paperclip")
                            .font(.title3)
                            .foregroundStyle(Theme.accent)
                    } else {
                        attachCard
                    }
                }
                .buttonStyle(.plain)
            }
        }
        .fileImporter(isPresented: $showingImporter,
                      allowedContentTypes: attachmentContentTypes,
                      allowsMultipleSelection: true) { result in
            if case let .success(urls) = result {
                importFiles(urls)
            }
        }
        .photosPicker(isPresented: $showingPhotos, selection: $selectedPhotos, matching: .images)
        .onChange(of: selectedPhotos) { _, items in
            importPhotos(items)
        }
        .sheet(isPresented: $showingNotionPicker) {
            NotionPagePickerSheet { pages in
                importNotionPages(pages)
            }
        }
        .task {
            await loadNotionStatus()
        }
    }

    /// Full-width glass card matching the form's other inputs (language menu,
    /// stepper). Used for the non-compact "Attach files" control. In grouped
    /// mode it drops its own glass so it can share a card with another row.
    @ViewBuilder
    private var attachCard: some View {
        let row = HStack(spacing: 12) {
            Image(systemName: systemImage)
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.headline)
                    .foregroundStyle(.primary)
                if let subtitle, !subtitle.isEmpty {
                    Text(subtitle)
                        .font(.subheadline)
                        .foregroundStyle(Theme.secondaryText)
                }
            }
            Spacer()
            Image(systemName: "plus.circle.fill")
                .font(.title3.weight(.semibold))
                .foregroundStyle(Theme.accent)
        }
        .padding(12)

        if grouped {
            row
        } else {
            row.glassEffect(in: .rect(cornerRadius: 16))
        }
    }

    private func chip(_ att: PendingAttachment) -> some View {
        HStack(spacing: 6) {
            switch att.status {
            case .uploading:
                ProgressView().controlSize(.mini).tint(Theme.accent)
            case .ready:
                Image(systemName: "doc.fill").font(.caption2).foregroundStyle(Theme.accent)
            case .failed:
                Image(systemName: "exclamationmark.triangle.fill").font(.caption2).foregroundStyle(.orange)
            }
            Text(att.filename)
                .font(.caption.weight(.medium))
                .lineLimit(1)
                .foregroundStyle(.primary)
            Button {
                attachments.removeAll { $0.id == att.id }
            } label: {
                Image(systemName: "xmark.circle.fill")
                    .font(.caption2)
                    .foregroundStyle(Theme.secondaryText)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .glassEffect(in: .capsule)
    }

    /// Reads each file's bytes synchronously (within its security scope) then
    /// uploads asynchronously, updating the chip's status as it resolves.
    private func importFiles(_ urls: [URL]) {
        for url in urls {
            let access = url.startAccessingSecurityScopedResource()
            let data = try? Data(contentsOf: url)
            if access { url.stopAccessingSecurityScopedResource() }
            let filename = url.lastPathComponent
            guard let data else {
                attachments.append(PendingAttachment(filename: filename, status: .failed(String(localized: "Couldn't read file", comment: "Attachment error when a picked file's bytes can't be read")), markdown: nil))
                continue
            }
            let mime = url.mimeType
            let pending = PendingAttachment(filename: filename, status: .uploading, markdown: nil, mimeType: mime)
            attachments.append(pending)
            let id = pending.id
            let api = APIClient(tokens: auth)
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

    /// Loads each picked photo's bytes and uploads it, mirroring importFiles.
    private func importPhotos(_ items: [PhotosPickerItem]) {
        guard !items.isEmpty else { return }
        for item in items {
            let utType = item.supportedContentTypes.first
            let ext = utType?.preferredFilenameExtension ?? "jpg"
            let mime = utType?.preferredMIMEType ?? "image/jpeg"
            let filename = "Photo-\(UUID().uuidString.prefix(6)).\(ext)"
            let pending = PendingAttachment(filename: filename, status: .uploading, markdown: nil, mimeType: mime)
            attachments.append(pending)
            let id = pending.id
            let api = APIClient(tokens: auth)
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

    private func importNotionPages(_ pages: [NotionPageDTO]) {
        guard !pages.isEmpty else { return }
        let api = APIClient(tokens: auth)
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
    }

    private func loadNotionStatus() async {
        guard !notionStatusLoaded, !notionStatusLoading else { return }
        notionStatusLoading = true
        defer {
            notionStatusLoading = false
            notionStatusLoaded = true
        }
        do {
            let status = try await APIClient(tokens: auth).notionStatus()
            notionConnected = status.connected
        } catch {
            notionConnected = false
        }
    }

    private func connectNotion() {
        let api = APIClient(tokens: auth)
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        notionAuthSession = nil
                        guard error == nil else { return }
                        do {
                            let status = try await api.notionStatus()
                            notionConnected = status.connected
                            notionStatusLoaded = true
                            if status.connected {
                                showingNotionPicker = true
                            }
                        } catch {
                            notionConnected = false
                            notionStatusLoaded = true
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

/// The attachment sources offered by the picker menu. The new-discussion form
/// routes each through its deep-link coordinator so the actual picker is hosted
/// by the parent view rather than the form widget.
enum AttachmentSource {
    case notion
    case photos
    case files
}

struct NotionPagePickerSheet: View {
    @Environment(AuthManager.self) private var auth
    @Environment(\.dismiss) private var dismiss

    var onAdd: ([NotionPageDTO]) -> Void

    @State private var query = ""
    @State private var pages: [NotionPageDTO] = []
    @State private var selectedIDs = Set<String>()
    @State private var selectedPagesByID: [String: NotionPageDTO] = [:]
    @State private var isLoading = false
    @State private var isConnecting = false
    @State private var errorMessage: String?
    @State private var notionAuthSession: ASWebAuthenticationSession?
    @State private var presentationProvider = AttachmentWebAuthPresentationContextProvider()

    var selectedPages: [NotionPageDTO] {
        selectedIDs.compactMap { selectedPagesByID[$0] }
    }

    var body: some View {
        NavigationStack {
            List {
                if let errorMessage {
                    Text(errorMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
                if isLoading && pages.isEmpty {
                    HStack {
                        Spacer()
                        ProgressView()
                        Spacer()
                    }
                }
                ForEach(pages) { page in
                    Button {
                        toggle(page)
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: selectedIDs.contains(page.id) ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(selectedIDs.contains(page.id) ? Theme.accent : Theme.secondaryText)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(page.title.isEmpty ? "Untitled" : page.title)
                                    .font(.body.weight(.medium))
                                    .foregroundStyle(.primary)
                                if let url = page.url, !url.isEmpty {
                                    Text(url)
                                        .font(.caption)
                                        .foregroundStyle(Theme.secondaryText)
                                        .lineLimit(1)
                                }
                            }
                            Spacer()
                        }
                    }
                }
            }
            .navigationTitle("Notion Pages")
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, prompt: "Search pages")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        allowMorePages()
                    } label: {
                        Label("Allow Access to More Pages", systemImage: "folder.badge.plus")
                    }
                    .disabled(isConnecting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        onAdd(selectedPages)
                        dismiss()
                    }
                    .disabled(selectedIDs.isEmpty)
                }
            }
            .task(id: query) {
                await search()
            }
        }
    }

    private func toggle(_ page: NotionPageDTO) {
        if selectedIDs.contains(page.id) {
            selectedIDs.remove(page.id)
            selectedPagesByID.removeValue(forKey: page.id)
        } else {
            selectedIDs.insert(page.id)
            selectedPagesByID[page.id] = page
        }
    }

    private func search() async {
        let currentQuery = query
        isLoading = true
        errorMessage = nil
        try? await Task.sleep(for: .milliseconds(250))
        guard !Task.isCancelled else { return }
        do {
            let result = try await APIClient(tokens: auth).searchNotionPages(query: currentQuery)
            guard !Task.isCancelled, currentQuery == query else { return }
            pages = result
        } catch {
            guard !Task.isCancelled, currentQuery == query else { return }
            errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
    }

    private func allowMorePages() {
        guard !isConnecting else { return }
        isConnecting = true
        errorMessage = nil
        let api = APIClient(tokens: auth)
        Task {
            do {
                let url = try await api.notionAuthURL()
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "debatepod") { _, error in
                    Task { @MainActor in
                        notionAuthSession = nil
                        isConnecting = false
                        guard error == nil else { return }
                        await search()
                    }
                }
                session.presentationContextProvider = presentationProvider
                session.prefersEphemeralWebBrowserSession = false
                notionAuthSession = session
                session.start()
            } catch {
                isConnecting = false
                errorMessage = (error as? APIError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

@MainActor
final class AttachmentWebAuthPresentationContextProvider: NSObject, ASWebAuthenticationPresentationContextProviding {
    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .first { $0.isKeyWindow } ?? ASPresentationAnchor()
    }
}

private extension URL {
    var mimeType: String {
        if let type = UTType(filenameExtension: pathExtension)?.preferredMIMEType {
            return type
        }
        return "application/octet-stream"
    }
}
