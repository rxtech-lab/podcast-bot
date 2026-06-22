import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

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

    var apiAttachment: Attachment? {
        guard status == .ready else { return nil }
        if let markdown, !markdown.isEmpty {
            return Attachment(filename: filename, markdown: markdown, url: url, mimeType: mimeType)
        }
        if let url, !url.isEmpty, mimeType?.hasPrefix("image/") == true {
            return Attachment(filename: filename, markdown: nil, url: url, mimeType: mimeType)
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

    @State private var showingImporter = false
    @State private var showingPhotos = false
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
    }

    /// Full-width glass card matching the form's other inputs (language menu,
    /// stepper). Used for the non-compact "Attach files" control. In grouped
    /// mode it drops its own glass so it can share a card with another row.
    @ViewBuilder
    private var attachCard: some View {
        let row = HStack(spacing: 12) {
            Image(systemName: "paperclip")
                .foregroundStyle(Theme.accent)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text("Attach files")
                    .font(.headline)
                    .foregroundStyle(.primary)
                Text("PDF, Word, slides, images")
                    .font(.subheadline)
                    .foregroundStyle(Theme.secondaryText)
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
                attachments.append(PendingAttachment(filename: filename, status: .failed("Couldn't read file"), markdown: nil))
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
                        updateStatus(id, status: .failed("Couldn't read photo"), response: nil)
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

    private func updateStatus(_ id: UUID, status: PendingAttachment.Status, response: UploadResponse?) {
        guard let idx = attachments.firstIndex(where: { $0.id == id }) else { return }
        attachments[idx].status = status
        attachments[idx].markdown = response?.markdown
        attachments[idx].url = response?.url
        attachments[idx].mimeType = response?.mimeType
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
