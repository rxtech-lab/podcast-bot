//
//  iOSTests.swift
//  iOSTests
//
//  Created by Qiwei Li on 6/22/26.
//

import XCTest
import AVFoundation
@testable import iOS

final class iOSTests: XCTestCase {
    override func tearDown() {
        URLProtocolStub.handler = nil
        super.tearDown()
    }

    func testTranscriptRetimeStateClampsNudgesAndPreservesCaption() {
        let original = TranscriptSegmentDTO(
            speaker: "Guest",
            offsetMs: 2_000,
            durationMs: 3_000,
            text: "Keep this caption unchanged."
        )
        var state = TranscriptRetimeState(segment: original, audioDurationMs: 10_000)

        XCTAssertEqual(state.nudgedTimestamp(from: 500, by: -1_000), 0)
        XCTAssertEqual(state.nudgedTimestamp(from: 9_500, by: 1_000), 10_000)

        state.set(.start, to: 2_500)
        state.set(.end, to: 7_000)
        let revised = state.revisedSegment(from: original)

        XCTAssertTrue(state.isValid)
        XCTAssertEqual(revised.speaker, original.speaker)
        XCTAssertEqual(revised.text, original.text)
        XCTAssertEqual(revised.offsetMs, 2_500)
        XCTAssertEqual(revised.durationMs, 4_500)

        state.set(.start, to: 10_000)
        XCTAssertFalse(state.isValid)
    }

    func testTranscriptRetimeTimestampUsesFixedClockFields() {
        XCTAssertEqual(transcriptRetimeTimestamp(663_800), "00:11:03:800")
        XCTAssertEqual(transcriptRetimeTimestamp(3_661_007), "01:01:01:007")
        XCTAssertEqual(transcriptRetimeTimestamp(-1), "00:00:00:000")
    }

    func testTranscriptPlaybackTimestampTracksAndClampsObservedPlayerTime() {
        XCTAssertEqual(
            transcriptPlaybackTimestamp(CMTime(value: 2_345, timescale: 1_000), maximumMs: 10_000),
            2_345
        )
        XCTAssertEqual(
            transcriptPlaybackTimestamp(CMTime(value: 12_000, timescale: 1_000), maximumMs: 10_000),
            10_000
        )
        XCTAssertEqual(
            transcriptPlaybackTimestamp(CMTime(value: -500, timescale: 1_000), maximumMs: 10_000),
            0
        )
        XCTAssertNil(transcriptPlaybackTimestamp(.invalid, maximumMs: 10_000))
    }

    func testTranscriptRetimeSequenceUsesChronologicalOrderAndMovesBackward() {
        let segments = [
            TranscriptSegmentDTO(speaker: "C", offsetMs: 8_000, durationMs: 1_000, text: "Third"),
            TranscriptSegmentDTO(speaker: "A", offsetMs: 1_000, durationMs: 1_000, text: "First"),
            TranscriptSegmentDTO(speaker: "B", offsetMs: 4_000, durationMs: 1_000, text: "Second")
        ]
        var sequence = TranscriptRetimeSequence(segments: segments, initialIndex: 1)

        XCTAssertNil(sequence.previousSegment)
        XCTAssertEqual(sequence.currentSegment.text, "First")
        XCTAssertEqual(sequence.nextSegment?.text, "Second")
        XCTAssertTrue(sequence.moveNext())
        XCTAssertEqual(sequence.currentSegment.text, "Second")
        XCTAssertEqual(sequence.previousSegment?.text, "First")
        XCTAssertEqual(sequence.nextSegment?.text, "Third")

        let revised = TranscriptSegmentDTO(
            speaker: "B",
            offsetMs: 4_500,
            durationMs: 750,
            text: "Second"
        )
        sequence.replaceCurrent(with: revised)
        XCTAssertEqual(sequence.pendingIndices, [2])
        XCTAssertEqual(sequence.pendingUpdates, [
            TranscriptSegmentUpdate(index: 2, segment: revised)
        ])

        XCTAssertTrue(sequence.movePrevious())
        XCTAssertEqual(sequence.currentSegment.text, "First")
        XCTAssertTrue(sequence.moveNext())
        XCTAssertEqual(sequence.currentSegment.offsetMs, 4_500)
        sequence.markSaved(at: 2)
        XCTAssertTrue(sequence.pendingIndices.isEmpty)
    }

    func testTranscriptBatchUpdateUsesOneRequest() async throws {
        var capturedRequest: URLRequest?
        var capturedBody: Data?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            capturedBody = request.httpBodyStreamData ?? request.httpBody
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            let responseBody = """
            {
              "id": "discussion-1",
              "topic": "Uploaded audio",
              "title": "Uploaded audio",
              "status": "planning",
              "language": "en-US"
            }
            """
            return (response, Data(responseBody.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)
        let updates = [
            TranscriptSegmentUpdate(
                index: 0,
                segment: TranscriptSegmentDTO(
                    speaker: "Host", offsetMs: 250, durationMs: 4_250, text: "First"
                )
            ),
            TranscriptSegmentUpdate(
                index: 2,
                segment: TranscriptSegmentDTO(
                    speaker: "Guest", offsetMs: 8_500, durationMs: 3_000, text: "Third"
                )
            )
        ]

        _ = try await api.updateTranscriptSegments(id: "discussion-1", updates: updates)

        XCTAssertEqual(capturedRequest?.httpMethod, "PATCH")
        XCTAssertEqual(
            capturedRequest?.url?.path,
            "/api/discussions/discussion-1/transcript/segments"
        )
        let body = try XCTUnwrap(capturedBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        let encodedUpdates = try XCTUnwrap(json["updates"] as? [[String: Any]])
        XCTAssertEqual(encodedUpdates.count, 2)
        XCTAssertEqual(encodedUpdates[0]["index"] as? Int, 0)
        XCTAssertEqual(encodedUpdates[0]["offset_ms"] as? Int, 250)
        XCTAssertEqual(encodedUpdates[1]["index"] as? Int, 2)
        XCTAssertEqual(encodedUpdates[1]["text"] as? String, "Third")
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

    @MainActor
    func testForceStopActionRequiresDiscussionOwner() throws {
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: URLSession(configuration: .ephemeral))
        var discussion = try decodeDiscussion(status: "generating", pointsCharged: 0)
        discussion.jobID = "job-1"
        discussion.isOwner = false
        let listenerModel = PlayerModel(discussion: discussion, api: api, username: "viewer")
        let listenerCanForceStop = listenerModel.canForceStop
        let listenerShowsForceStopAction = listenerModel.showsForceStopAction

        XCTAssertFalse(listenerCanForceStop)
        XCTAssertFalse(listenerShowsForceStopAction)

        discussion.isOwner = true
        let ownerModel = PlayerModel(discussion: discussion, api: api, username: "owner")
        let ownerCanForceStop = ownerModel.canForceStop
        let ownerShowsForceStopAction = ownerModel.showsForceStopAction

        XCTAssertTrue(ownerCanForceStop)
        XCTAssertTrue(ownerShowsForceStopAction)
    }

    func testDiscussionsSearchAddsQueryParameter() async throws {
        var capturedRequest: URLRequest?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data("[]".utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        _ = try await api.discussions(limit: 10,
                                      offset: 20,
                                      query: "  vibe coding  ",
                                      visibility: .public)

        XCTAssertEqual(capturedRequest?.httpMethod, "GET")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/discussions")
        XCTAssertEqual(capturedRequest?.url?.queryItems["limit"], "10")
        XCTAssertEqual(capturedRequest?.url?.queryItems["offset"], "20")
        XCTAssertEqual(capturedRequest?.url?.queryItems["q"], "vibe coding")
        XCTAssertEqual(capturedRequest?.url?.queryItems["visibility"], "public")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
    }

    func testPrecheckAddsClientHeadersAndDecodesSchema() async throws {
        var capturedRequest: URLRequest?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            let body = """
            {
              "new_discussion": {
                "form": {
                  "title": "New Station",
                  "submit_title": "Plan",
                  "cancel_title": "Cancel",
                  "loading_title": "Creating station...",
                  "schema": {
                    "type": "object",
                    "properties": {
                      "topic": { "type": "string" }
                    }
                  },
                  "ui_schema": {
                    "topic": { "ui:widget": "textarea" }
                  },
                  "initial_data": {
                    "topic": ""
                  },
                  "actions": []
                }
              }
            }
            """
            return (response, Data(body.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        let response = try await api.precheck()

        XCTAssertEqual(capturedRequest?.httpMethod, "GET")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/precheck")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "X-Client-Platform"), "ios")
        XCTAssertNotNil(capturedRequest?.value(forHTTPHeaderField: "X-Client-Version"))
        XCTAssertNotNil(capturedRequest?.value(forHTTPHeaderField: "X-Client-Build"))
        XCTAssertEqual(response.newDiscussion.form.title, "New Station")
        XCTAssertEqual(response.newDiscussion.form.schema["type"], .string("object"))
    }

    func testMarketStationsSearchAddsQueryParameter() async throws {
        var capturedRequest: URLRequest?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data("[]".utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        _ = try await api.marketStations(limit: 12, offset: 24, query: "  space music  ")

        XCTAssertEqual(capturedRequest?.httpMethod, "GET")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/market/stations")
        XCTAssertEqual(capturedRequest?.url?.queryItems["limit"], "12")
        XCTAssertEqual(capturedRequest?.url?.queryItems["offset"], "24")
        XCTAssertEqual(capturedRequest?.url?.queryItems["q"], "space music")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
    }

    func testPodcastPlayerUniversalLinkParsesAsDiscussionDeepLink() throws {
        let link = try XCTUnwrap(DeepLink(url: URL(string: "https://podcast.rxlab.app/p/discussion-1")!))

        XCTAssertEqual(link, .publicDiscussion(id: "discussion-1"))
    }

    func testPlayerDiscussionUsesOwnedDetailBeforeMarketFallback() async throws {
        var paths: [String] = []
        URLProtocolStub.handler = { request in
            paths.append(request.url?.path ?? "")
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data("""
            {"id":"discussion-1","topic":"Private topic","title":"Private title","status":"ready","language":"en","visibility":"private"}
            """.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        let discussion = try await api.playerDiscussion(id: "discussion-1")

        XCTAssertEqual(discussion.id, "discussion-1")
        XCTAssertEqual(discussion.visibility, .private)
        XCTAssertEqual(paths, ["/api/discussions/discussion-1"])
    }

    func testPlayerDiscussionFallsBackToMarketAndJoinsWhenOwnedDetailMissing() async throws {
        var paths: [String] = []
        URLProtocolStub.handler = { request in
            paths.append(request.url?.path ?? "")
            switch request.url?.path {
            case "/api/discussions/discussion-1":
                let response = HTTPURLResponse(url: request.url!,
                                               statusCode: 404,
                                               httpVersion: nil,
                                               headerFields: nil)!
                return (response, Data("not found".utf8))
            case "/api/market/stations/discussion-1":
                let response = HTTPURLResponse(url: request.url!,
                                               statusCode: 200,
                                               httpVersion: nil,
                                               headerFields: nil)!
                return (response, Data("""
                {"id":"discussion-1","topic":"Public topic","title":"Public title","status":"ready","language":"en","visibility":"public"}
                """.utf8))
            case "/api/discussions/discussion-1/join":
                let response = HTTPURLResponse(url: request.url!,
                                               statusCode: 204,
                                               httpVersion: nil,
                                               headerFields: nil)!
                return (response, Data())
            default:
                let response = HTTPURLResponse(url: request.url!,
                                               statusCode: 500,
                                               httpVersion: nil,
                                               headerFields: nil)!
                return (response, Data("unexpected path".utf8))
            }
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        let discussion = try await api.playerDiscussion(id: "discussion-1")

        XCTAssertEqual(discussion.id, "discussion-1")
        XCTAssertEqual(discussion.visibility, .public)
        XCTAssertEqual(paths, [
            "/api/discussions/discussion-1",
            "/api/market/stations/discussion-1",
            "/api/discussions/discussion-1/join",
        ])
    }

    func testMarketProfileDecodesCreatorAndStationsWithoutEmail() async throws {
        URLProtocolStub.handler = { request in
            XCTAssertEqual(request.url?.path, "/api/market/profile")
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data("""
            {
              "profile":{"id":"oauth:user-1","display_name":"Qiwei","avatar_url":"https://auth.example/avatar.png","follower_count":2,"is_self":true},
              "stations":[{"id":"discussion-1","topic":"Topic","title":"Title","status":"ready","language":"en","visibility":"public","creator":{"id":"oauth:user-1","display_name":"Qiwei","avatar_url":"https://auth.example/avatar.png"}}],
              "following":[{"id":"oauth:user-2","display_name":"Creator Two","follower_count":1,"is_followed":true}]
            }
            """.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        let profile = try await api.marketProfile()

        XCTAssertEqual(profile.profile.displayName, "Qiwei")
        XCTAssertEqual(profile.profile.avatarURL, "https://auth.example/avatar.png")
        XCTAssertEqual(profile.stations.first?.creator?.id, "oauth:user-1")
        XCTAssertEqual(profile.following.first?.displayName, "Creator Two")
    }

    func testCreatorAPIsUseCreatorRoutes() async throws {
        var captured: [(String, String)] = []
        URLProtocolStub.handler = { request in
            captured.append((request.httpMethod ?? "", request.url?.path ?? ""))
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            if request.url?.path.hasSuffix("/stations") == true {
                return (response, Data("[]".utf8))
            }
            return (response, Data("""
            {"id":"oauth:creator","display_name":"Creator","follower_count":1,"is_followed":true}
            """.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        _ = try await api.creatorProfile(id: "oauth:creator")
        _ = try await api.creatorStations(id: "oauth:creator", limit: 10, offset: 20)
        _ = try await api.followCreator(id: "oauth:creator")
        _ = try await api.unfollowCreator(id: "oauth:creator")

        XCTAssertEqual(captured.map(\.0), ["GET", "GET", "POST", "DELETE"])
        XCTAssertEqual(captured.map(\.1), [
            "/api/market/creators/oauth:creator",
            "/api/market/creators/oauth:creator/stations",
            "/api/market/creators/oauth:creator/follow",
            "/api/market/creators/oauth:creator/follow",
        ])
    }

    func testPublishVisibilityPatchEncodesCover() async throws {
        var capturedRequest: URLRequest?
        var capturedBody: Data?
        URLProtocolStub.handler = { request in
            capturedRequest = request
            capturedBody = request.httpBodyStreamData ?? request.httpBody
            let response = HTTPURLResponse(url: request.url!,
                                           statusCode: 200,
                                           httpVersion: nil,
                                           headerFields: nil)!
            return (response, Data("""
            {"id":"discussion-1","topic":"Topic","title":"Title","status":"ready","language":"en","visibility":"public"}
            """.utf8))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [URLProtocolStub.self]
        let session = URLSession(configuration: config)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"),
                            session: session)

        _ = try await api.updateDiscussionVisibility(
            id: "discussion-1",
            visibility: .public,
            cover: DiscussionCover(type: "gradient",
                                   imageURL: nil,
                                   imageKey: nil,
                                   gradientStart: "#111111",
                                   gradientEnd: "#777777",
                                   prompt: nil)
        )

        XCTAssertEqual(capturedRequest?.httpMethod, "PATCH")
        XCTAssertEqual(capturedRequest?.url?.path, "/api/discussions/discussion-1/visibility")
        XCTAssertEqual(capturedRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer token-1")
        let body = try XCTUnwrap(capturedBody)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(json["visibility"] as? String, "public")
        let cover = try XCTUnwrap(json["cover"] as? [String: Any])
        XCTAssertEqual(cover["type"] as? String, "gradient")
        XCTAssertEqual(cover["gradient_start"] as? String, "#111111")
        XCTAssertEqual(cover["gradient_end"] as? String, "#777777")
    }

}
