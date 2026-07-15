//
//  iOSTests.swift
//  iOSTests
//
//  Created by Qiwei Li on 6/22/26.
//

import XCTest
import AVFoundation
@testable import iOS

extension iOSTests {
    func testIllustrationCuesMapServerDTOs() {
        let dtos = [
            IllustrationCueDTO(startMS: 78694, imageURL: "https://img/2.png", caption: " star "),
            IllustrationCueDTO(startMS: 0, imageURL: "https://img/0.png", caption: "Opening"),
            IllustrationCueDTO(startMS: 36406, imageURL: "https://img/1.png", caption: nil),
        ]
        let cues = PlayerModel.illustrationCues(from: dtos)
        XCTAssertEqual(cues.count, 3)
        XCTAssertEqual(cues[0].start, 0)
        XCTAssertEqual(cues[0].url.absoluteString, "https://img/0.png")
        XCTAssertEqual(cues[0].caption, "Opening")
        XCTAssertEqual(cues[1].start, 36.406, accuracy: 0.001)
        XCTAssertEqual(cues[1].caption, "")
        XCTAssertEqual(cues[2].start, 78.694, accuracy: 0.001)
        XCTAssertEqual(cues[2].caption, "star")
    }

    func testIllustrationCuesDropInvalidEntries() {
        let dtos = [
            IllustrationCueDTO(startMS: 10_000, imageURL: "   ", caption: nil),
            IllustrationCueDTO(startMS: -500, imageURL: "https://img/a.png", caption: nil),
        ]
        let cues = PlayerModel.illustrationCues(from: dtos)
        XCTAssertEqual(cues.count, 1)
        // Negative offsets clamp to 0 rather than producing an unreachable cue.
        XCTAssertEqual(cues[0].start, 0)
        XCTAssertTrue(PlayerModel.illustrationCues(from: []).isEmpty)
    }

    func testIllustrationCueDTODecodesServerJSON() throws {
        let json = Data("""
        {"illustrations":[{"start_ms":36406,"image_url":"https://img/1.png","caption":"bar"},{"start_ms":0,"image_url":"https://img/0.png"}]}
        """.utf8)
        let decoded = try JSONDecoder().decode(IllustrationsResponseDTO.self, from: json)
        XCTAssertEqual(decoded.illustrations.count, 2)
        XCTAssertEqual(decoded.illustrations[0].startMS, 36406)
        XCTAssertEqual(decoded.illustrations[0].imageURL, "https://img/1.png")
        XCTAssertEqual(decoded.illustrations[0].caption, "bar")
        XCTAssertNil(decoded.illustrations[1].caption)
    }
}
