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

    func testPodcastTranscriptReadinessIgnoresUserOnlyLines() {
        let userOnly = [
            LiveLine(speaker: "Qiwei", role: "user", text: "Are we ready?", isUser: true, done: true),
            LiveLine(speaker: "Qiwei", role: "viewer", text: "Echoed from stream", isUser: false, done: true)
        ]
        let podcastLines = userOnly + [
            LiveLine(speaker: "Host", role: "host", text: "Welcome to the discussion.", isUser: false, done: true)
        ]

        XCTAssertFalse(PlayerModel.containsPodcastTranscript(userOnly))
        XCTAssertTrue(PlayerModel.containsPodcastTranscript(podcastLines))
    }

    func testPodcastTranscriptVisibilityKeepsLocalUserMessageButHidesRoleOnlyEcho() {
        let localUserMessage = LiveLine(speaker: "Qiwei",
                                       role: "user",
                                       text: "What about the budget?",
                                       isUser: true,
                                       done: true)
        let roleOnlyEcho = LiveLine(speaker: "Qiwei",
                                    role: "user",
                                    text: "What about the budget?",
                                    isUser: false,
                                    done: true)
        let panelLine = LiveLine(speaker: "Host",
                                 role: "host",
                                 text: "Let's take that question.",
                                 isUser: false,
                                 done: true)

        XCTAssertTrue(PlayerModel.isVisibleTranscriptLine(localUserMessage))
        XCTAssertFalse(PlayerModel.isVisibleTranscriptLine(roleOnlyEcho))
        XCTAssertTrue(PlayerModel.isVisibleTranscriptLine(panelLine))
    }

    func testAudioOnlyVoiceMessageDoesNotRequireDisplayText() {
        let voiceLine = LiveLine(speaker: "Qiwei",
                                 role: "user",
                                 text: "   ",
                                 isUser: true,
                                 done: true,
                                 audioURL: "https://media.example/voice.m4a")
        let textLine = LiveLine(speaker: "Qiwei",
                                role: "user",
                                text: "hello",
                                isUser: true,
                                done: true)

        XCTAssertTrue(voiceLine.hasAudio)
        XCTAssertFalse(voiceLine.hasDisplayText)
        XCTAssertFalse(textLine.hasAudio)
        XCTAssertTrue(textLine.hasDisplayText)
    }

    func testAudioInterruptionResumeOptionIsRecognized() {
        let resumable: [AnyHashable: Any] = [
            AVAudioSessionInterruptionOptionKey: AVAudioSession.InterruptionOptions.shouldResume.rawValue
        ]
        let notResumable: [AnyHashable: Any] = [
            AVAudioSessionInterruptionOptionKey: UInt(0)
        ]

        XCTAssertTrue(PlayerModel.audioInterruptionShouldResume(resumable))
        XCTAssertFalse(PlayerModel.audioInterruptionShouldResume(notResumable))
        XCTAssertFalse(PlayerModel.audioInterruptionShouldResume(nil))
    }

    func testNowPlayingArtworkSourceKeyUsesRenderableCover() {
        let imageCover = DiscussionCover(type: "image",
                                         imageURL: "https://cdn.example/cover.webp",
                                         imageKey: "covers/cover.webp",
                                         gradientStart: nil,
                                         gradientEnd: nil,
                                         prompt: nil)
        let gradientCover = DiscussionCover(type: "gradient",
                                            imageURL: nil,
                                            imageKey: nil,
                                            gradientStart: "#111111",
                                            gradientEnd: "#777777",
                                            prompt: nil)
        let keyOnlyCover = DiscussionCover(type: "image",
                                           imageURL: nil,
                                           imageKey: "covers/cover.webp",
                                           gradientStart: nil,
                                           gradientEnd: nil,
                                           prompt: nil)

        XCTAssertEqual(PlayerModel.nowPlayingArtworkSourceKey(for: imageCover),
                       "image:https://cdn.example/cover.webp")
        XCTAssertEqual(PlayerModel.nowPlayingArtworkSourceKey(for: gradientCover),
                       "gradient:#111111:#777777")
        XCTAssertNil(PlayerModel.nowPlayingArtworkSourceKey(for: keyOnlyCover))
    }

    func testPersistedSenderMetadataMarksReloadedJobTranscriptUserLineAsMine() {
        var reloadedLine = LiveLine(speaker: "Qiwei",
                                    role: "user",
                                    text: "What about the budget?",
                                    isUser: true,
                                    done: true)
        let persistedLine = DiscussionLineDTO(speaker: "Qiwei",
                                              role: "user",
                                              side: nil,
                                              text: "What about the budget?",
                                              startMS: nil,
                                              isUser: true,
                                              senderUserID: "oauth:me")

        XCTAssertFalse(PlayerModel.isLineAuthoredByCurrentUser(
            reloadedLine,
            currentUserID: "oauth:me",
            currentUsername: "Renamed User"
        ))

        PlayerModel.applyPersistedMetadata(to: &reloadedLine, from: persistedLine)

        XCTAssertEqual(reloadedLine.senderUserID, "oauth:me")
        XCTAssertTrue(PlayerModel.isLineAuthoredByCurrentUser(
            reloadedLine,
            currentUserID: "oauth:me",
            currentUsername: "Renamed User"
        ))
    }

    func testPersistedMetadataCarriesAudiobookImageURL() {
        var reloadedLine = LiveLine(speaker: "Narrator",
                                    role: "host",
                                    text: "",
                                    isUser: false,
                                    done: true)
        let persistedLine = DiscussionLineDTO(speaker: "Narrator",
                                              role: "host",
                                              side: nil,
                                              text: "",
                                              startMS: nil,
                                              isUser: false,
                                              imageURL: "https://cdn.example/chapter-1.webp")

        PlayerModel.applyPersistedMetadata(to: &reloadedLine, from: persistedLine)

        XCTAssertEqual(reloadedLine.imageURL, "https://cdn.example/chapter-1.webp")
        XCTAssertTrue(reloadedLine.hasImage)
    }

    func testSpeakerPaletteUsesTranscriptOrderToAvoidHashCollision() {
        let lines = [
            LiveLine(speaker: "Host", role: "host", text: "Welcome.", isUser: false, done: true),
            LiveLine(speaker: "Guest", role: "discussant", text: "Thanks.", isUser: false, done: true)
        ]

        XCTAssertEqual(SpeakerPalette.index(for: "Host"), SpeakerPalette.index(for: "Guest"))
        XCTAssertNotEqual(SpeakerPalette.index(for: "Host", in: lines),
                          SpeakerPalette.index(for: "Guest", in: lines))
    }

    func testDiscussionPointsTextWaitsForReadyStatus() throws {
        let generating = try decodeDiscussion(status: "generating", pointsCharged: 21)
        let ready = try decodeDiscussion(status: "ready", pointsCharged: 21)
        let hidden = try decodeDiscussion(status: "ready", pointsCharged: 21, showUsageSummary: false)

        XCTAssertNil(generating.pointsText)
        XCTAssertEqual(ready.pointsText, "21 points")
        XCTAssertNil(hidden.pointsText)
    }

    func testFinishedDiscussionRefreshUsesAuthoritativePoints() throws {
        var current = try decodeDiscussion(status: "generating", pointsCharged: 15)
        current.lines = [
            DiscussionLineDTO(speaker: "Host", role: "host", side: nil,
                              text: "Local final line", startMS: nil, isUser: false)
        ]
        var fresh = try decodeDiscussion(status: "ready", pointsCharged: 132)
        fresh.lines = []

        let merged = PlayerModel.mergingLocalDiscussionState(current: current, fresh: fresh)

        XCTAssertEqual(merged.status, .ready)
        XCTAssertEqual(merged.pointsCharged, 132)
        XCTAssertEqual(merged.pointsText, "132 points")
        XCTAssertEqual(merged.lines?.map(\.text), ["Local final line"])
    }

    func testLikedStationPreservesKnownRenderableCoverURL() throws {
        var marketStation = try decodeDiscussion(status: "ready", pointsCharged: 0)
        marketStation.cover = DiscussionCover(type: "image",
                                               imageURL: "https://cdn.example/cover.webp?signature=abc",
                                               imageKey: "covers/discussion-1.webp",
                                               gradientStart: nil,
                                               gradientEnd: nil,
                                               prompt: nil)
        var likeResponse = try decodeDiscussion(status: "ready", pointsCharged: 0)
        likeResponse.cover = DiscussionCover(type: "image",
                                             imageURL: nil,
                                             imageKey: "covers/discussion-1.webp",
                                             gradientStart: nil,
                                             gradientEnd: nil,
                                             prompt: nil)

        let merged = likeResponse.preservingRenderableCover(from: marketStation)

        XCTAssertEqual(merged.cover?.imageURL, marketStation.cover?.imageURL)
        XCTAssertNotNil(merged.cover?.renderableImageURL)
        XCTAssertNil(likeResponse.cover?.renderableImageURL)
    }

    func testSpeakerInitialsIgnoreParenthesizedRomanization() {
        XCTAssertEqual(SpeakerPalette.initials(for: "陆严 (LU YAN)"), "陆")
        XCTAssertEqual(SpeakerPalette.initials(for: "陆严（LU YAN）"), "陆")
    }

    func testTranscriptLoadingDelayKeepsOneSecondMinimum() {
        let start = Date(timeIntervalSinceReferenceDate: 100)

        XCTAssertEqual(PlayerModel.minimumTranscriptLoadingSeconds, 1.0)
        XCTAssertEqual(PlayerModel.remainingTranscriptLoadingDelay(startedAt: start,
                                                                   now: start.addingTimeInterval(0.25)),
                       0.75,
                       accuracy: 0.001)
        XCTAssertEqual(PlayerModel.remainingTranscriptLoadingDelay(startedAt: start,
                                                                   now: start.addingTimeInterval(1.25)),
                       0,
                       accuracy: 0.001)
    }

    func testTranscriptLoadingClearsWhenJobFailsWithoutTranscript() {
        XCTAssertFalse(PlayerModel.transcriptLoadingVisibleAfterTerminalFailure(wasVisible: true))
        XCTAssertFalse(PlayerModel.transcriptLoadingVisibleAfterTerminalFailure(wasVisible: false))
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

    func testAudioBookChapterTitleTracksLatestHeardCaptionTitle() throws {
        let script = try decodeScript("""
        {
          "title": "云间的送信人",
          "type": "audio-book",
          "language": "zh-Hans",
          "audio_book_chapters": [
            { "title": "晓峰的来信", "summary": "开篇。" },
            { "title": "雨后的归途", "summary": "结尾。" }
          ]
        }
        """)
        let cues = [
            VTTCue(start: 0, end: 3, text: "第一章：晓峰的来信"),
            VTTCue(start: 12, end: 18, text: "陈奶奶是村里年纪最大的长辈。"),
            VTTCue(start: 90, end: 94, text: "第二章，雨后的归途"),
            VTTCue(start: 100, end: 106, text: "他们沿着山路慢慢走回去。")
        ]

        XCTAssertEqual(PlayerModel.audioBookChapterTitle(at: 20, in: cues, script: script), "晓峰的来信")
        XCTAssertEqual(PlayerModel.audioBookChapterTitle(at: 101, in: cues, script: script), "雨后的归途")
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

    func testCancellationErrorsAreRecognizedAsNonAlerting() {
        XCTAssertTrue(APIClient.isCancellation(CancellationError()))
        XCTAssertTrue(APIClient.isCancellation(URLError(.cancelled)))
        XCTAssertTrue(APIClient.isCancellation(NSError(domain: NSURLErrorDomain,
                                                       code: NSURLErrorCancelled)))
        XCTAssertFalse(APIClient.isCancellation(URLError(.timedOut)))
        XCTAssertFalse(APIClient.isCancellation(APIError.notAuthenticated))
    }
}

private func decodeDiscussion(status: String, pointsCharged: Int, showUsageSummary: Bool = true) throws -> Discussion {
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

private func decodeScript(_ json: String) throws -> ScriptDTO {
    try JSONDecoder().decode(ScriptDTO.self, from: Data(json.utf8))
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

private extension URL {
    var queryItems: [String: String] {
        URLComponents(url: self, resolvingAgainstBaseURL: false)?
            .queryItems?
            .reduce(into: [:]) { result, item in result[item.name] = item.value ?? "" } ?? [:]
    }
}
