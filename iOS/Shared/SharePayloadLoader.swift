import Foundation
import UniformTypeIdentifiers

enum IncomingShareKind: Equatable {
    case audio
    case file
    case webURL
}

struct IncomingShareItem: Identifiable, Equatable {
    let id = UUID()
    let kind: IncomingShareKind
    let filename: String
    let mimeType: String
    let fileURL: URL?
    let webURL: URL?

    var systemImage: String {
        switch kind {
        case .audio: "waveform"
        case .file: mimeType == "application/pdf" ? "doc.richtext" : "doc"
        case .webURL: "link"
        }
    }
}

enum SharePayloadError: LocalizedError {
    case noSupportedItems
    case mixedAudio
    case cannotRead(String)

    var errorDescription: String? {
        switch self {
        case .noSupportedItems:
            "PanelFM couldn't find an audio file, webpage, PDF, or document in this share."
        case .mixedAudio:
            "Share one audio file at a time. Audio can't be combined with other files or links."
        case .cannotRead(let name):
            "PanelFM couldn't read \(name)."
        }
    }
}

enum SharePayloadLoader {
    static func load(inputItems: [Any]) async throws -> [IncomingShareItem] {
        let extensionItems = inputItems.compactMap { $0 as? NSExtensionItem }
        let providers = extensionItems.flatMap { $0.attachments ?? [] }
        var results: [IncomingShareItem] = []

        for provider in providers {
            if let audioType = firstType(in: provider, conformingTo: .audio) {
                let file = try await copyFile(from: provider, typeIdentifier: audioType)
                results.append(IncomingShareItem(
                    kind: .audio,
                    filename: displayName(provider: provider, url: file),
                    mimeType: UTType(audioType)?.preferredMIMEType ?? mimeType(for: file),
                    fileURL: file,
                    webURL: nil
                ))
                continue
            }

            if let fileType = firstDocumentType(in: provider) {
                let file = try await copyFile(from: provider, typeIdentifier: fileType)
                results.append(IncomingShareItem(
                    kind: .file,
                    filename: displayName(provider: provider, url: file),
                    mimeType: UTType(fileType)?.preferredMIMEType ?? mimeType(for: file),
                    fileURL: file,
                    webURL: nil
                ))
            }

            // A Safari PDF share commonly vends both the PDF representation and
            // its http(s) URL. Intentionally load both from the provider(s).
            if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier),
               let url = try? await loadURL(from: provider),
               ["http", "https"].contains(url.scheme?.lowercased() ?? "") {
                results.append(IncomingShareItem(
                    kind: .webURL,
                    filename: url.host ?? url.absoluteString,
                    mimeType: "text/uri-list",
                    fileURL: nil,
                    webURL: url
                ))
            }
        }

        results = deduplicated(results)
        guard !results.isEmpty else { throw SharePayloadError.noSupportedItems }
        let audioCount = results.filter { $0.kind == .audio }.count
        if audioCount > 0, results.count != 1 { throw SharePayloadError.mixedAudio }
        return results
    }

    private static func firstType(in provider: NSItemProvider, conformingTo wanted: UTType) -> String? {
        provider.registeredTypeIdentifiers.first { identifier in
            UTType(identifier)?.conforms(to: wanted) == true
        }
    }

    private static func firstDocumentType(in provider: NSItemProvider) -> String? {
        let concreteType = provider.registeredTypeIdentifiers.first { identifier in
            guard let type = UTType(identifier),
                  !type.conforms(to: .url),
                  !type.conforms(to: .audio),
                  !type.conforms(to: .plainText),
                  type != .data,
                  type != .content,
                  type != .item else { return false }
            return type.conforms(to: .data) || type.conforms(to: .content)
        }
        if let concreteType { return concreteType }

        // URL providers often also advertise public.data. Treating that generic
        // representation as a document creates a phantom attachment and can
        // hide the actual webpage URL.
        guard !provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) else { return nil }
        return provider.registeredTypeIdentifiers.first { identifier in
            guard let type = UTType(identifier) else { return false }
            return type == .data || type == .content || type == .item
        }
    }

    private static func copyFile(from provider: NSItemProvider,
                                 typeIdentifier: String) async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            provider.loadFileRepresentation(forTypeIdentifier: typeIdentifier) { source, error in
                if let error {
                    continuation.resume(throwing: error)
                    return
                }
                guard let source else {
                    continuation.resume(throwing: SharePayloadError.cannotRead(provider.suggestedName ?? "file"))
                    return
                }
                do {
                    let directory = FileManager.default.temporaryDirectory
                        .appendingPathComponent("PanelFMShare", isDirectory: true)
                    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    let baseName = provider.suggestedName?.trimmingCharacters(in: .whitespacesAndNewlines)
                    let name = (baseName?.isEmpty == false ? baseName! : source.lastPathComponent)
                    let destination = directory.appendingPathComponent("\(UUID().uuidString)-\(name)")
                    try FileManager.default.copyItem(at: source, to: destination)
                    continuation.resume(returning: destination)
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    private static func loadURL(from provider: NSItemProvider) async throws -> URL {
        try await withCheckedThrowingContinuation { continuation in
            provider.loadItem(forTypeIdentifier: UTType.url.identifier) { value, error in
                if let error {
                    continuation.resume(throwing: error)
                } else if let url = value as? URL {
                    continuation.resume(returning: url)
                } else if let url = value as? NSURL {
                    continuation.resume(returning: url as URL)
                } else if let string = value as? String, let url = URL(string: string) {
                    continuation.resume(returning: url)
                } else if let data = value as? Data,
                          let string = String(data: data, encoding: .utf8),
                          let url = URL(string: string.trimmingCharacters(in: .whitespacesAndNewlines)) {
                    continuation.resume(returning: url)
                } else {
                    continuation.resume(throwing: SharePayloadError.cannotRead("webpage URL"))
                }
            }
        }
    }

    private static func displayName(provider: NSItemProvider, url: URL) -> String {
        let suggested = provider.suggestedName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return suggested.isEmpty ? url.lastPathComponent : suggested
    }

    private static func mimeType(for url: URL) -> String {
        UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
    }

    private static func deduplicated(_ items: [IncomingShareItem]) -> [IncomingShareItem] {
        var URLs = Set<String>()
        var files = Set<String>()
        return items.filter { item in
            if let url = item.webURL {
                return URLs.insert(url.absoluteString).inserted
            }
            guard let file = item.fileURL else { return true }
            let size = (try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
            return files.insert("\(item.filename):\(size):\(item.mimeType)").inserted
        }
    }
}
