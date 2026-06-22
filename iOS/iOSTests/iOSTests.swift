//
//  iOSTests.swift
//  iOSTests
//
//  Created by Qiwei Li on 6/22/26.
//

import XCTest
@testable import iOS

final class iOSTests: XCTestCase {
    override func tearDown() {
        URLProtocolStub.handler = nil
        super.tearDown()
    }

    func testSendJobMessagePostsToRunningJobOrchestrator() async throws {
        var capturedRequest: URLRequest?
        var capturedBody: Data?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            capturedBody = request.httpBodyStreamData ?? request.httpBody
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 204,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data())
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        try await api.sendJobMessage(id: "job-1",
                                     text: "end it fast",
                                     username: "Qiwei",
                                     discussionID: "discussion-1")

        XCTAssertEqual(capturedRequest?.httpMethod, "POST")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/jobs/job-1/messages")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Content-Type"), "application/json")

        let body = try XCTUnwrap(capturedBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: String])
        XCTAssertEqual(json["text"], "end it fast")
        XCTAssertEqual(json["username"], "Qiwei")
        XCTAssertEqual(json["discussion_id"], "discussion-1")
    }

    func testForceStopJobPostsToStopEndpoint() async throws {
        var capturedRequest: URLRequest?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 202,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data())
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        try await api.forceStopJob(id: "job-1")

        XCTAssertEqual(capturedRequest?.httpMethod, "POST")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/jobs/job-1/stop")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Content-Type"), "application/json")
    }

    func testTranscriptChunksAppendUntilDoneMarker() {
        var lines: [LiveLine] = []

        XCTAssertNil(LiveLine.applyTranscriptEvent(to: &lines,
                                                   speaker: "Sarah",
                                                   role: "discussant",
                                                   text: "First sentence.",
                                                   done: false))
        XCTAssertEqual(lines.count, 1)
        XCTAssertEqual(lines[0].text, "First sentence.")
        XCTAssertFalse(lines[0].done)

        XCTAssertNil(LiveLine.applyTranscriptEvent(to: &lines,
                                                   speaker: "Sarah",
                                                   role: "discussant",
                                                   text: "Second sentence.",
                                                   done: false))
        XCTAssertEqual(lines.count, 1)
        XCTAssertEqual(lines[0].text, "First sentence. Second sentence.")

        let completed = LiveLine.applyTranscriptEvent(to: &lines,
                                                      speaker: "Sarah",
                                                      role: "discussant",
                                                      text: "",
                                                      done: true)
        XCTAssertEqual(completed?.text, "First sentence. Second sentence.")
        XCTAssertTrue(lines[0].done)
    }

    func testTranscriptScrollTokenChangesWhenLastMessageTextUpdates() {
        var lines = [
            LiveLine(speaker: "Sarah", role: "discussant", text: "First", isUser: false, done: false)
        ]
        let before = TranscriptScrollToken.make(for: lines)

        lines[0].text = "First Second"
        let afterTextUpdate = TranscriptScrollToken.make(for: lines)

        lines[0].done = true
        let afterDone = TranscriptScrollToken.make(for: lines)

        XCTAssertNotEqual(before, afterTextUpdate)
        XCTAssertNotEqual(afterTextUpdate, afterDone)
        XCTAssertEqual(before.count, afterTextUpdate.count)
    }

    func testCaptionTextSelectsCueForLookupTime() {
        let cues = [
            VTTCue(start: 0, end: 1, text: "First"),
            VTTCue(start: 1.5, end: 3, text: "Second")
        ]

        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 0.5), "First")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 2.0), "Second")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 3.5), "")
    }

    func testCaptionSpeakerMatchesCaptionTextIgnoringPunctuation() {
        let lines = [
            LiveLine(speaker: "建國兄",
                     role: "discussant",
                     text: "建國兄看到的技術杠杆和雅麗擔心的分配風險。",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "You",
                     role: "user",
                     text: "What happens next?",
                     isUser: true,
                     done: true)
        ]

        XCTAssertEqual(PlayerModel.captionSpeaker(for: "建國兄看到的技術杠杆和雅麗擔心的分配風險", in: lines),
                       "建國兄")
        XCTAssertNil(PlayerModel.captionSpeaker(for: "not in the transcript", in: lines))
    }

    func testLiveCaptionHasNoManualLead() {
        // The backend now emits zero-bias VTT for audio-only feeds, so cues align
        // with the recording and the frontend must not advance the lookup time —
        // a non-zero lead would surface captions early.
        let cues = [
            VTTCue(start: 1.5, end: 3, text: "Now audible")
        ]

        XCTAssertEqual(PlayerModel.liveCaptionLeadSeconds, 0.0)
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 0.0), "")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 1.5), "Now audible")
    }

    func testCaptionLookupTimeIsUnshiftedRegardlessOfTimingMode() {
        let playbackTime = 10.0

        let duringLivePlayback = PlayerModel.captionLookupTime(playbackTime: playbackTime,
                                                               usesLiveCaptionTiming: true)
        let afterFinalAudioLoaded = PlayerModel.captionLookupTime(playbackTime: playbackTime,
                                                                  usesLiveCaptionTiming: false)

        XCTAssertEqual(duringLivePlayback, playbackTime)
        XCTAssertEqual(afterFinalAudioLoaded, playbackTime)
    }

    func testFinalLyricsGroupAdjacentShortCues() {
        let cues = [
            VTTCue(start: 0.0, end: 1.0, text: "Yes"),
            VTTCue(start: 1.1, end: 2.0, text: "that is the point"),
            VTTCue(start: 2.1, end: 3.0, text: "we should pause"),
            VTTCue(start: 8.0, end: 9.0, text: "A later caption stands alone")
        ]

        let groups = PlayerModel.groupLyricCues(cues)

        XCTAssertEqual(groups.count, 2)
        XCTAssertEqual(groups[0].start, 0.0)
        XCTAssertEqual(groups[0].end, 3.0)
        XCTAssertEqual(groups[0].text, "Yes\nthat is the point\nwe should pause")
        XCTAssertEqual(groups[0].firstCueIndex, 0)
        XCTAssertEqual(groups[0].lastCueIndex, 2)
        XCTAssertEqual(groups[1].text, "A later caption stands alone")
    }

    func testFinalLyricsDoNotGroupKnownSpeakerChanges() {
        let cues = [
            VTTCue(start: 0.0, end: 1.0, text: "Yes"),
            VTTCue(start: 1.1, end: 2.0, text: "No")
        ]

        let groups = PlayerModel.groupLyricCues(cues) { cue in
            cue.text == "Yes" ? "Speaker A" : "Speaker B"
        }

        XCTAssertEqual(groups.count, 2)
        XCTAssertEqual(groups[0].text, "Yes")
        XCTAssertEqual(groups[1].text, "No")
    }

    func testHLSReadinessFindsFirstMediaSegment() {
        let playlist = """
        #EXTM3U
        #EXT-X-VERSION:3
        #EXT-X-TARGETDURATION:4
        #EXTINF:4.000000,
        seg_00000.ts
        #EXTINF:4.000000,
        seg_00001.ts
        """

        XCTAssertEqual(APIClient.firstHLSMediaSegment(in: playlist), "seg_00000.ts")
        XCTAssertNil(APIClient.firstHLSMediaSegment(in: "#EXTM3U\n#EXT-X-VERSION:3\n"))
    }
}

private struct StaticTokenProvider: TokenProviding {
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

private final class URLProtocolStub: URLProtocol {
    static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool {
        true
    }

    override class func canonicalRequest(for request: URLRequest) -> URLRequest {
        request
    }

    override func startLoading() {
        guard let handler = Self.handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
            return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

private extension URLRequest {
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
