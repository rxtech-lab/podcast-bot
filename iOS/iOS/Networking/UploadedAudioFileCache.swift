import Foundation

actor UploadedAudioFileCache {
    static let shared = UploadedAudioFileCache()

    private let fileManager = FileManager.default

    func localURL(
        discussionID: String,
        sourceURL: @Sendable () async throws -> URL
    ) async throws -> URL {
        let directory = try cacheDirectory(for: discussionID)
        if let cachedURL = validCachedFile(in: directory) {
            return cachedURL
        }

        let remoteURL = try await sourceURL()
        var request = URLRequest(url: remoteURL)
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        let (temporaryURL, response) = try await URLSession.shared.download(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw cacheError()
        }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.http(http.statusCode, HTTPURLResponse.localizedString(forStatusCode: http.statusCode))
        }

        let pathExtension = safePathExtension(remoteURL.pathExtension)
        let destinationURL = directory
            .appendingPathComponent("source", isDirectory: false)
            .appendingPathExtension(pathExtension)
        if fileManager.fileExists(atPath: destinationURL.path) {
            try fileManager.removeItem(at: destinationURL)
        }
        try fileManager.moveItem(at: temporaryURL, to: destinationURL)
        return destinationURL
    }

    private func cacheDirectory(for discussionID: String) throws -> URL {
        let cachesURL = try fileManager.url(
            for: .cachesDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        )
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-_."))
        let safeID = String(discussionID.unicodeScalars.map { allowed.contains($0) ? Character($0) : "_" })
        let directory = cachesURL
            .appendingPathComponent("UploadedAudio", isDirectory: true)
            .appendingPathComponent(safeID.isEmpty ? "unknown" : safeID, isDirectory: true)
        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory
    }

    private func validCachedFile(in directory: URL) -> URL? {
        let keys: Set<URLResourceKey> = [.isRegularFileKey, .fileSizeKey]
        guard let files = try? fileManager.contentsOfDirectory(
            at: directory,
            includingPropertiesForKeys: Array(keys),
            options: [.skipsHiddenFiles]
        ) else { return nil }

        for fileURL in files {
            let values = try? fileURL.resourceValues(forKeys: keys)
            if values?.isRegularFile == true, (values?.fileSize ?? 0) > 0 {
                return fileURL
            }
            try? fileManager.removeItem(at: fileURL)
        }
        return nil
    }

    private func safePathExtension(_ pathExtension: String) -> String {
        let allowed = CharacterSet.alphanumerics
        let sanitized = String(pathExtension.unicodeScalars.filter { allowed.contains($0) })
        return sanitized.isEmpty ? "audio" : sanitized
    }

    private func cacheError() -> APIError {
        .invalidRequest(String(
            localized: "Could not cache uploaded audio.",
            comment: "Error shown when the original uploaded audio cannot be saved for transcript replay"
        ))
    }
}

