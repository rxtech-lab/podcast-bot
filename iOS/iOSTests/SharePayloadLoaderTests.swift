import UniformTypeIdentifiers
import XCTest
@testable import iOS

final class SharePayloadLoaderTests: XCTestCase {
    func testSafariPDFSharePreservesFileAndWebLink() async throws {
        let pdfURL = try temporaryFile(named: "source.pdf", contents: Data("%PDF-1.4\n%%EOF".utf8))
        defer { try? FileManager.default.removeItem(at: pdfURL.deletingLastPathComponent()) }

        let item = NSExtensionItem()
        item.attachments = [
            try XCTUnwrap(NSItemProvider(contentsOf: pdfURL)),
            webURLProvider("https://example.com/source.pdf"),
        ]

        let payload = try await SharePayloadLoader.load(inputItems: [item])

        XCTAssertEqual(payload.count, 2)
        XCTAssertEqual(payload.filter { $0.kind == .file }.count, 1)
        XCTAssertEqual(payload.filter { $0.kind == .webURL }.count, 1)
        XCTAssertEqual(payload.first { $0.kind == .webURL }?.webURL?.absoluteString,
                       "https://example.com/source.pdf")
    }

    func testAudioShareUsesExclusiveAudioRoute() async throws {
        let audioURL = try temporaryFile(named: "recording.m4a", contents: Data())
        defer { try? FileManager.default.removeItem(at: audioURL.deletingLastPathComponent()) }

        let item = NSExtensionItem()
        item.attachments = [try XCTUnwrap(NSItemProvider(contentsOf: audioURL))]

        let payload = try await SharePayloadLoader.load(inputItems: [item])

        XCTAssertEqual(payload.count, 1)
        XCTAssertEqual(payload.first?.kind, .audio)
        XCTAssertTrue(payload.first?.mimeType.hasPrefix("audio/") == true)
    }

    func testMixedAudioAndLinkIsRejected() async throws {
        let audioURL = try temporaryFile(named: "recording.m4a", contents: Data())
        defer { try? FileManager.default.removeItem(at: audioURL.deletingLastPathComponent()) }

        let item = NSExtensionItem()
        item.attachments = [
            try XCTUnwrap(NSItemProvider(contentsOf: audioURL)),
            webURLProvider("https://example.com"),
        ]

        do {
            _ = try await SharePayloadLoader.load(inputItems: [item])
            XCTFail("Expected mixed audio shares to be rejected")
        } catch let error as SharePayloadError {
            guard case .mixedAudio = error else {
                return XCTFail("Expected mixedAudio, got \(error)")
            }
        }
    }

    private func temporaryFile(named name: String, contents: Data) throws -> URL {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("SharePayloadLoaderTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let fileURL = directory.appendingPathComponent(name)
        try contents.write(to: fileURL)
        return fileURL
    }

    private func webURLProvider(_ string: String) -> NSItemProvider {
        let provider = NSItemProvider()
        provider.registerDataRepresentation(
            forTypeIdentifier: UTType.url.identifier,
            visibility: .all
        ) { completion in
            completion(Data(string.utf8), nil)
            return nil
        }
        return provider
    }
}
