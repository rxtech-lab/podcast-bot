//
//  TranscriptRetimeTests.swift
//  iOSUITests
//
//  E2E tests for the uploaded-audio caption editor (TranscriptSegmentRetimeSheet,
//  "Subtitle Timing"). They drive the seeded `test-uploaded-audio` fixture whose
//  five captions ("E2E caption one" … "E2E caption five") start 5 s apart; the
//  E2E backend serves the fixture's audio as a local silent MP3, so seeks and
//  playback behave like production. Current time is controlled through the
//  deterministic ±1 s nudge buttons rather than real-time playback.
//

import XCTest

final class TranscriptRetimeTests: E2ETestCase {
    private let fixtureID = "test-uploaded-audio"

    // MARK: - Navigation helper

    /// Library row → plan card's Transcript section → transcript sheet →
    /// "Edit From Start", landing on the retime sheet at the earliest caption.
    private func openRetimeSheet(_ app: XCUIApplication) {
        openLibraryRow(app, id: fixtureID)

        let transcript = app.buttons["plan.chapters"]
        if !transcript.waitForExistence(timeout: 20) {
            app.swipeUp()
        }
        XCTAssertTrue(transcript.waitForExistence(timeout: 10), "transcript section not shown on the plan card")
        transcript.tap()

        let editFromStart = app.buttons["transcript.editFromStart"]
        XCTAssertTrue(editFromStart.waitForExistence(timeout: 10), "transcript sheet did not open")
        editFromStart.tap()

        XCTAssertTrue(app.navigationBars["Subtitle Timing"].waitForExistence(timeout: 10),
                      "retime sheet did not open")
    }

    /// Waits until an element attribute (label, value, …) satisfies the predicate.
    @discardableResult
    private func waitForState(_ element: XCUIElement, _ format: String, _ value: String,
                              timeout: TimeInterval = 10) -> Bool {
        let expectation = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: format, value), object: element)
        return XCTWaiter().wait(for: [expectation], timeout: timeout) == .completed
    }

    /// Finds the currently-rendered text node with this identifier and label.
    /// The caption transition and audio timeline replace accessibility nodes,
    /// so retaining an XCUIElement across either update is unreliable on iOS 26.3.
    private func waitForText(_ app: XCUIApplication, identifier: String, label: String,
                             timeout: TimeInterval = 10) -> Bool {
        app.staticTexts
            .matching(identifier: identifier)
            .matching(NSPredicate(format: "label == %@", label))
            .firstMatch
            .waitForExistence(timeout: timeout)
    }

    private func waitForTextChange(_ app: XCUIApplication, identifier: String, from label: String,
                                   timeout: TimeInterval = 10) -> Bool {
        app.staticTexts
            .matching(identifier: identifier)
            .matching(NSPredicate(format: "label != %@", label))
            .firstMatch
            .waitForExistence(timeout: timeout)
    }

    /// Nudges the audio position forward one second and waits for the current
    /// time readout to land exactly one second later (the seek is async). The
    /// expected value is derived from the current readout, so the test does
    /// not depend on the caption's absolute offset (a prior save test run may
    /// have retimed it).
    private func nudgeForward(_ app: XCUIApplication) {
        let currentLabel = app.staticTexts["retime.currentTime"].label
        guard let expected = timestampByAddingOneSecond(to: currentLabel) else {
            return XCTFail("unparseable current time readout: \(currentLabel)")
        }
        app.buttons["retime.plus1s"].tap()
        XCTAssertTrue(waitForText(app, identifier: "retime.currentTime", label: expected),
                      "current time never reached \(expected)")
    }

    /// "HH:MM:SS:mmm" + 1 s, matching transcriptRetimeTimestamp's format.
    private func timestampByAddingOneSecond(to label: String) -> String? {
        let parts = label.split(separator: ":").compactMap { Int64($0) }
        guard parts.count == 4 else { return nil }
        let ms = ((parts[0] * 3600 + parts[1] * 60 + parts[2]) * 1000 + parts[3]) + 1000
        return String(format: "%02d:%02d:%02d:%03d",
                      ms / 3_600_000, (ms % 3_600_000) / 60_000, (ms % 60_000) / 1000, ms % 1000)
    }

    // MARK: - 1. "Set to current time" advances through the captions

    /// Each pair of taps stamps a caption's start and end from the current
    /// audio time; stamping the end auto-advances to the next caption. Five
    /// taps (with nudges keeping end > start) must land on caption three with
    /// the end boundary armed.
    func testSetCurrentTimeAdvancesCaptions() throws {
        let app = launch()
        openRetimeSheet(app)

        let setCurrent = app.buttons["retime.setCurrent"]
        // Caption one: stamp start, nudge, stamp end → advances to caption two.
        setCurrent.tap()
        XCTAssertTrue(waitForState(setCurrent, "label CONTAINS %@", "Set End to Current Time"),
                      "first tap did not switch the armed boundary to the end")
        nudgeForward(app)
        setCurrent.tap()
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption two"
        ),
                      "stamping caption one's end did not advance to caption two")

        // Caption two: same start → nudge → end rhythm → caption three.
        setCurrent.tap()
        nudgeForward(app)
        setCurrent.tap()
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption three"
        ),
                      "stamping caption two's end did not advance to caption three")

        // Fifth tap stamps caption three's start and arms its end boundary.
        setCurrent.tap()
        XCTAssertTrue(waitForState(setCurrent, "label CONTAINS %@", "Set End to Current Time"),
                      "fifth tap did not arm the end boundary on caption three")
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption three"
        ),
                      "fifth tap must stay on caption three (only its start is stamped)")

        // Playback smoke check: play advances the clock, pause stops it.
        let currentTime = app.staticTexts["retime.currentTime"]
        let before = currentTime.label
        app.buttons["retime.play"].tap()
        XCTAssertTrue(waitForTextChange(app, identifier: "retime.currentTime", from: before),
                      "audio playback never advanced the current time")
        app.buttons["retime.play"].tap()

        // Leave without saving so the fixture stays pristine for other tests.
        app.buttons["retime.cancel"].tap()
    }

    // MARK: - 2. Save persists the retimed caption to the backend

    func testSaveRetimePersists() throws {
        let app = launch()
        openRetimeSheet(app)

        // Move the first caption's start to 1 s via the wheel (deterministic).
        app.buttons["retime.adjustStart"].tap()
        let secondsWheel = app.pickerWheels.element(boundBy: 1)
        XCTAssertTrue(secondsWheel.waitForExistence(timeout: 8), "timestamp wheel did not appear")
        secondsWheel.adjust(toPickerWheelValue: "01")
        app.buttons["retime.picker.done"].tap()

        let startField = app.buttons["retime.selectStart"]
        XCTAssertTrue(waitForState(startField, "value == %@", "00:00:01:000"),
                      "start field did not take the wheel value, shows: \(String(describing: startField.value))")

        app.buttons["retime.save"].tap()
        let sheetGone = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "exists == false"),
            object: app.navigationBars["Subtitle Timing"])
        XCTAssertEqual(XCTWaiter().wait(for: [sheetGone], timeout: 15), .completed,
                       "retime sheet did not dismiss after saving")

        // The backend is authoritative: the segment PATCH must have landed.
        let raw = try fetchDiscussionRaw(fixtureID)
        XCTAssertTrue(raw.contains("\"offset_ms\":1000"),
                      "saved start offset not found in the discussion JSON")
        XCTAssertTrue(raw.contains("\"duration_ms\":3000"),
                      "saved duration (end unchanged at 4 s) not found in the discussion JSON")

        // Reopen from the still-open transcript sheet: the editor must show
        // the persisted timing, not the stale pre-save value.
        let editFromStart = app.buttons["transcript.editFromStart"]
        XCTAssertTrue(editFromStart.waitForExistence(timeout: 8), "transcript sheet not visible after save")
        editFromStart.tap()
        XCTAssertTrue(app.navigationBars["Subtitle Timing"].waitForExistence(timeout: 10),
                      "retime sheet did not reopen")
        XCTAssertTrue(waitForState(app.buttons["retime.selectStart"], "value == %@", "00:00:01:000"),
                      "reopened editor lost the saved start time")
        app.buttons["retime.cancel"].tap()
    }

    // MARK: - 3. Tapping the start/end fields switches the armed boundary

    func testTapStartEndSwitchesBoundary() throws {
        let app = launch()
        openRetimeSheet(app)

        let setCurrent = app.buttons["retime.setCurrent"]
        XCTAssertTrue(setCurrent.label.contains("Set Start to Current Time"),
                      "editor must open with the start boundary armed, shows: \(setCurrent.label)")

        app.buttons["retime.selectEnd"].tap()
        XCTAssertTrue(waitForState(setCurrent, "label CONTAINS %@", "Set End to Current Time"),
                      "tapping the end field did not arm the end boundary")

        app.buttons["retime.selectStart"].tap()
        XCTAssertTrue(waitForState(setCurrent, "label CONTAINS %@", "Set Start to Current Time"),
                      "tapping the start field did not re-arm the start boundary")

        app.buttons["retime.cancel"].tap()
    }

    // MARK: - 4. The wheel picker edits a timestamp

    func testWheelEditsTimestamp() throws {
        let app = launch()
        openRetimeSheet(app)

        app.buttons["retime.adjustStart"].tap()
        XCTAssertTrue(app.navigationBars["Adjust Start Time"].waitForExistence(timeout: 8),
                      "wheel picker sheet did not open")
        // 60 s fixture audio < 1 h, so the hours wheel is hidden.
        XCTAssertEqual(app.pickerWheels.count, 3, "expected minutes/seconds/milliseconds wheels")

        app.pickerWheels.element(boundBy: 1).adjust(toPickerWheelValue: "02")
        app.buttons["retime.picker.done"].tap()

        XCTAssertTrue(waitForState(app.buttons["retime.selectStart"], "value == %@", "00:00:02:000"),
                      "start field did not take the wheel value")

        app.buttons["retime.cancel"].tap()
    }

    // MARK: - 5. Prev/next navigate between captions

    func testPrevNextNavigatesCaptions() throws {
        let app = launch()
        openRetimeSheet(app)

        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption one"
        ),
                      "editor must open on the earliest caption")
        XCTAssertFalse(app.buttons["retime.previous"].isEnabled,
                       "previous must be disabled on the first caption")

        app.buttons["retime.next"].tap()
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption two"
        ),
                      "next did not move to caption two")

        app.buttons["retime.next"].tap()
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption three"
        ),
                      "next did not move to caption three")

        app.buttons["retime.previous"].tap()
        XCTAssertTrue(waitForText(
            app, identifier: "retime.currentSubtitle", label: "E2E caption two"
        ),
                      "previous did not move back to caption two")

        app.buttons["retime.cancel"].tap()
    }
}
