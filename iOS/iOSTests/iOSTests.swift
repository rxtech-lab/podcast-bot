//
//  iOSTests.swift
//  iOSTests
//
//  Created by Qiwei Li on 6/22/26.
//

import XCTest
@testable import iOS

final class iOSTests: XCTestCase {
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

    func testLiveCaptionLeadMatchesObservedDelay() {
        let cues = [
            VTTCue(start: 1.5, end: 3, text: "Now audible")
        ]
        let correctedLiveTime = 0.0 + PlayerModel.liveCaptionLeadSeconds

        XCTAssertEqual(PlayerModel.liveCaptionLeadSeconds, 1.5)
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: 0.0), "")
        XCTAssertEqual(PlayerModel.captionText(in: cues, at: correctedLiveTime), "Now audible")
    }

    func testLiveCaptionLeadPersistsUntilPlayerSwitchesToFinalAudio() {
        let playbackTime = 10.0

        let duringLivePlayback = PlayerModel.captionLookupTime(playbackTime: playbackTime,
                                                               usesLiveCaptionTiming: true)
        let afterJobFinishedButStillPlayingLiveHLS = PlayerModel.captionLookupTime(playbackTime: playbackTime,
                                                                                   usesLiveCaptionTiming: true)
        let afterFinalAudioLoaded = PlayerModel.captionLookupTime(playbackTime: playbackTime,
                                                                  usesLiveCaptionTiming: false)

        XCTAssertEqual(duringLivePlayback, playbackTime + PlayerModel.liveCaptionLeadSeconds)
        XCTAssertEqual(afterJobFinishedButStillPlayingLiveHLS, playbackTime + PlayerModel.liveCaptionLeadSeconds)
        XCTAssertEqual(afterFinalAudioLoaded, playbackTime)
    }
}
