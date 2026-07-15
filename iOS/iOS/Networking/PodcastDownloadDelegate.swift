import Foundation

final class PodcastDownloadDelegate: NSObject, URLSessionDownloadDelegate {
    let destinationURL: URL
    let progress: (Double) -> Void
    var continuation: CheckedContinuation<URL, Error>?
    private var completionResult: Result<URL, Error>?

    init(destinationURL: URL, progress: @escaping (Double) -> Void) {
        self.destinationURL = destinationURL
        self.progress = progress
    }

    func urlSession(_ session: URLSession,
                    downloadTask: URLSessionDownloadTask,
                    didWriteData bytesWritten: Int64,
                    totalBytesWritten: Int64,
                    totalBytesExpectedToWrite: Int64) {
        guard totalBytesExpectedToWrite > 0 else { return }
        progress(min(1, max(0, Double(totalBytesWritten) / Double(totalBytesExpectedToWrite))))
    }

    func urlSession(_ session: URLSession,
                    downloadTask: URLSessionDownloadTask,
                    didFinishDownloadingTo location: URL) {
        do {
            try FileManager.default.createDirectory(
                at: destinationURL.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            if FileManager.default.fileExists(atPath: destinationURL.path) {
                try FileManager.default.removeItem(at: destinationURL)
            }
            try FileManager.default.copyItem(at: location, to: destinationURL)
            completionResult = .success(destinationURL)
        } catch {
            completionResult = .failure(error)
        }
    }

    func urlSession(_ session: URLSession,
                    task: URLSessionTask,
                    didCompleteWithError error: Error?) {
        if let error {
            continuation?.resume(throwing: error)
            continuation = nil
            return
        }
        if let http = task.response as? HTTPURLResponse,
           !(200..<300).contains(http.statusCode) {
            continuation?.resume(throwing: APIError.http(http.statusCode, HTTPURLResponse.localizedString(forStatusCode: http.statusCode)))
            continuation = nil
            return
        }
        switch completionResult {
        case let .success(url):
            progress(1)
            continuation?.resume(returning: url)
        case let .failure(error):
            continuation?.resume(throwing: error)
        case .none:
            continuation?.resume(throwing: APIError.invalidRequest(String(localized: "Download did not produce a file.",
                                                                          comment: "Shown when a podcast download completes without producing a file")))
        }
        continuation = nil
    }
}
