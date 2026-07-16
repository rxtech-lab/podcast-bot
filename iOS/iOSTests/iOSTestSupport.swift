//
//  iOSTests.swift
//  iOSTests
//
//  Created by Qiwei Li on 6/22/26.
//

import XCTest
import AVFoundation
@testable import iOS


func decodeDiscussion(status: String, pointsCharged: Int, showUsageSummary: Bool = true) throws -> Discussion {
    let json = """
    {
      "id": "discussion-1",
      "topic": "Topic",
      "title": "Title",
      "status": "\(status)",
      "language": "en",
      "points_charged": \(pointsCharged),
      "showUsageSummary": \(showUsageSummary)
    }
    """
    return try JSONDecoder().decode(Discussion.self, from: Data(json.utf8))
}

func decodeScript(_ json: String) throws -> ScriptDTO {
    try JSONDecoder().decode(ScriptDTO.self, from: Data(json.utf8))
}

struct StaticTokenProvider: TokenProviding {
    let tokenValue: String

    init(token: String) {
        self.tokenValue = token
    }

    func token() async -> String? {
        tokenValue
    }

    func refreshedToken() async -> String? {
        tokenValue
    }
}

extension URLRequest {
    var httpBodyStreamData: Data? {
        guard let stream = httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 1024)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count <= 0 { break }
            data.append(buffer, count: count)
        }
        return data
    }
}

extension URL {
    var queryItems: [String: String] {
        URLComponents(url: self, resolvingAgainstBaseURL: false)?
            .queryItems?
            .reduce(into: [:]) { result, item in result[item.name] = item.value ?? "" } ?? [:]
    }
}

// MARK: - Timed illustration timeline (backend-owned)

