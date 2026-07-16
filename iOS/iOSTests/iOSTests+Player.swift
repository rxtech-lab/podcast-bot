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

    func testPresentationLanguageSwapPreservesTranscriptRowIdentities() {
        let source = [
            LiveLine(speaker: "Host", role: "host", text: "Welcome", isUser: false, done: true),
            LiveLine(speaker: "Guest", role: "discussant", text: "Thank you", isUser: false, done: true)
        ]
        let translated = [
            DiscussionLineDTO(speaker: "主持人", role: "host", side: nil,
                              text: "歡迎", startMS: 0, isUser: false),
            DiscussionLineDTO(speaker: "嘉賓", role: "discussant", side: nil,
                              text: "謝謝", startMS: 1_000, isUser: false)
        ]

        let result = PlayerModel.presentationLines(
            from: translated,
            preservingIdentitiesFrom: source
        )

        XCTAssertEqual(result.map(\.id), source.map(\.id))
        XCTAssertEqual(result.map(\.speaker), ["主持人", "嘉賓"])
        XCTAssertEqual(result.map(\.text), ["歡迎", "謝謝"])
        XCTAssertNil(result[0].audioOffsetSeconds)
        XCTAssertEqual(result[1].audioOffsetSeconds, 1)
    }

    func testInitialPresentationLanguageAdoptsServerSelectedTranslation() throws {
        var discussion = try decodeDiscussion(status: "ready", pointsCharged: 0)
        discussion.language = "zh-CN"
        discussion.mainLanguage = "en-US"

        XCTAssertEqual(PlayerModel.initialPresentationLanguage(from: discussion), "zh-CN")

        discussion.language = "en-US"
        XCTAssertNil(PlayerModel.initialPresentationLanguage(from: discussion))
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

    func testVisibleTranscriptLinesSuppressesAdjacentRepeatedPrefix() {
        let intro = "欢迎收听由我为您播讲的长篇有声书，《一滴水的威力：极简冷笑话》。"
        let lines = [
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: intro,
                     isUser: false,
                     done: true),
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: "\(intro)\n\n生活，有时像是一场严谨的实验。",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: "Yes.",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: "Yes.",
                     isUser: false,
                     done: true)
        ]

        let visible = PlayerModel.visibleTranscriptLines(lines)

        XCTAssertEqual(visible.map(\.text), [
            "\(intro)\n\n生活，有时像是一场严谨的实验。",
            "Yes.",
            "Yes."
        ])
    }

    func testVisibleTranscriptLinesSuppressesEmptyHistoricalRows() {
        let lines = [
            LiveLine(speaker: "路人甲",
                     role: "host",
                     text: "那个，各位老师，我就想问一句，你们平时淋过雨吗？",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: "   ",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "张博士",
                     role: "discussant",
                     text: "是啊，原来我们都淋过雨。",
                     isUser: false,
                     done: true),
            LiveLine(speaker: "小雅",
                     role: "host",
                     text: "",
                     isUser: false,
                     done: true)
        ]

        let visible = PlayerModel.visibleTranscriptLines(lines)

        XCTAssertEqual(visible.map(\.speaker), ["路人甲", "张博士"])
        XCTAssertTrue(visible.allSatisfy(\.hasRenderablePayload))
    }

    func testTranscriptDisplayTextStripsNaturalSpeechMarkers() {
        let line = LiveLine(speaker: "小雅",
                            role: "series-host",
                            text: #"""
<pause time="800ms"/>
这条消息发出来的瞬间，那个飞速滚动的学术群突然停滞了。
"""#,
                            isUser: false,
                            done: true)

        XCTAssertTrue(line.hasDisplayText)
        XCTAssertEqual(line.displayText, "这条消息发出来的瞬间，那个飞速滚动的学术群突然停滞了。")
        XCTAssertEqual(PlayerModel.visibleTranscriptLines([line]).map(\.displayText), [
            "这条消息发出来的瞬间，那个飞速滚动的学术群突然停滞了。"
        ])
    }

    func testMarkerOnlyTranscriptLineIsNotVisible() {
        let line = LiveLine(speaker: "小雅",
                            role: "series-host",
                            text: #"<pause time="500ms"/>"#,
                            isUser: false,
                            done: true)

        XCTAssertFalse(line.hasDisplayText)
        XCTAssertFalse(PlayerModel.isVisibleTranscriptLine(line))
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

    @MainActor
    func testPlayerSessionStoreReusesFullScreenSessionAcrossRebuild() async throws {
        let discussion = try decodeDiscussion(status: "ready", pointsCharged: 0)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"))
        var createdModels = 0
        let store = PlayerSessionStore(
            releaseGracePeriod: .milliseconds(5),
            startsModels: false
        ) { discussion, api, username, userID, shareToken in
            createdModels += 1
            return PlayerModel(discussion: discussion,
                               api: api,
                               username: username,
                               userID: userID,
                               shareToken: shareToken)
        }

        let first = store.acquire(discussion: discussion,
                                  api: api,
                                  username: "Qiwei",
                                  userID: "user-1")
        first.isFullPlayerPresented = true
        store.release(first)
        let second = store.acquire(discussion: discussion,
                                   api: api,
                                   username: "Qiwei",
                                   userID: "user-1")

        XCTAssertTrue(first === second)
        XCTAssertTrue(second.isFullPlayerPresented)
        XCTAssertEqual(createdModels, 1)
        XCTAssertEqual(store.activeSessionCount, 1)
    }

    @MainActor
    func testPlayerSessionStoreReleasesInactiveSessionAfterGracePeriod() async throws {
        let discussion = try decodeDiscussion(status: "ready", pointsCharged: 0)
        let api = APIClient(baseURL: URL(string: "https://engine.example")!,
                            tokens: StaticTokenProvider(token: "token-1"))
        let store = PlayerSessionStore(releaseGracePeriod: .milliseconds(5),
                                       startsModels: false)
        let session = store.acquire(discussion: discussion,
                                    api: api,
                                    username: "Qiwei",
                                    userID: "user-1")

        store.release(session)
        try await Task.sleep(for: .milliseconds(20))

        XCTAssertEqual(store.activeSessionCount, 0)
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

    func testTranscriptSnapshotReplacesLocalPrefixLine() {
        let local = [
            LiveLine(speaker: "晓凡 (XIAO FAN)",
                     role: "host",
                     text: "欢迎收听音频书，星海的最后回响。",
                     isUser: false,
                     done: true)
        ]
        let snapshot = [
            TranscriptDTO(speaker: "晓凡 (XIAO FAN)",
                          role: "host",
                          side: nil,
                          text: "欢迎收听音频书，星海的最后回响。 在遥远的未来，当星际航行的辉煌逐渐褪色。",
                          at: nil)
        ]

        XCTAssertEqual(PlayerModel.snapshotPrefixReplacementIndex(for: snapshot[0],
                                                                  text: snapshot[0].text,
                                                                  isUser: false,
                                                                  in: local,
                                                                  snapshot: snapshot),
                       0)
    }

    func testTranscriptSnapshotKeepsPrefixLineWhenAuthoritative() {
        let local = [
            LiveLine(speaker: "晓凡 (XIAO FAN)",
                     role: "host",
                     text: "欢迎收听音频书，星海的最后回响。",
                     isUser: false,
                     done: true)
        ]
        let snapshot = [
            TranscriptDTO(speaker: "晓凡 (XIAO FAN)",
                          role: "host",
                          side: nil,
                          text: "欢迎收听音频书，星海的最后回响。",
                          at: nil),
            TranscriptDTO(speaker: "晓凡 (XIAO FAN)",
                          role: "host",
                          side: nil,
                          text: "欢迎收听音频书，星海的最后回响。 在遥远的未来，当星际航行的辉煌逐渐褪色。",
                          at: nil)
        ]

        XCTAssertNil(PlayerModel.snapshotPrefixReplacementIndex(for: snapshot[1],
                                                                text: snapshot[1].text,
                                                                isUser: false,
                                                                in: local,
                                                                snapshot: snapshot))
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

    func testCaptionCuePrefersLatestOverlappingCue() {
        // Stored segments can overlap (an STT phrase may overrun the next
        // one); cue starts stay monotonic, so the latest-starting cue
        // containing the time is the line actually being spoken.
        let cues = [
            VTTCue(start: 13, end: 35, text: "Overruns the next two"),
            VTTCue(start: 22, end: 26, text: "Second"),
            VTTCue(start: 26, end: 31, text: "Third")
        ]

        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 15), "Overruns the next two")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 23), "Second")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 27), "Third")
        // Inside only the overlapping long cue again after its successors end.
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 33), "Overruns the next two")
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

    func testAudioBookChapterTitleUsesLeadForDelayedTitleCue() throws {
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
            VTTCue(start: 86, end: 89, text: "陈奶奶望向山路。"),
            VTTCue(start: 90, end: 94, text: "第二章，雨后的归途")
        ]

        XCTAssertEqual(PlayerModel.audioBookChapterTitle(at: 88.2, in: cues, script: script), "雨后的归途")
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
