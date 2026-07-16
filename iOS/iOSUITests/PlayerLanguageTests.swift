//
//  PlayerLanguageTests.swift
//  iOSUITests
//
//  E2E test for podcast presentation-language switching. Uses the read-only
//  test-translated fixture (en-US source + ready zh-CN translation seeded in
//  internal/server/e2e_seed.go) and asserts on Chinese marker strings that
//  appear nowhere in the English fixtures.
//

import XCTest

final class PlayerLanguageTests: E2ETestCase {
    // Marker strings from the seeded zh-CN translation bundle.
    private let translatedTitle = "E2E 翻译播客"
    private let translatedHost = "测试主持人"
    private let translatedDiscussant = "爱丽丝"
    private let translatedLine = "欢迎收听这期合成端到端测试讨论。"
    // The transcript list rests scrolled to the newest line, so on-screen
    // assertions for it use the closing line rather than the opening one.
    private let translatedClosingLine = "感谢各位，本次测试讨论到此结束。"
    private let translatedSummaryMarker = "端到端翻译摘要"
    private let translatedMindmapRoot = "端到端思维导图"
    private let sourceClosingLine = "Thank you all. That concludes our test discussion."

    /// One flow through every translated surface: transcript, title, lyrics,
    /// plan, speaker models (the plan → Models regression), summary, mindmap.
    func testLanguageSwitchTranslatesAllSurfaces() throws {
        let app = launch()
        openLibraryRow(app, id: "test-translated")
        XCTAssertTrue(playerOpened(app), "player did not open for test-translated")

        // The podcast starts in its source language.
        XCTAssertTrue(anyText(app, containing: sourceClosingLine).waitForExistence(timeout: 15),
                      "source transcript line not shown before switching")

        switchLanguage(app, to: "zh-CN")

        // Transcript lines and speaker names swap to the translation.
        XCTAssertTrue(anyText(app, containing: translatedClosingLine).waitForExistence(timeout: 20),
                      "transcript line was not translated")
        XCTAssertTrue(anyText(app, containing: translatedHost).waitForExistence(timeout: 5),
                      "transcript speaker name was not translated")
        // The player title reflects the translated bundle.
        XCTAssertTrue(anyText(app, containing: translatedTitle).waitForExistence(timeout: 10),
                      "title was not translated")

        try assertTranslatedPlanAndModels(app)
        try assertTranslatedLyrics(app)
        try assertTranslatedDocument(app, menuButton: "Summary", marker: translatedSummaryMarker)
        try assertTranslatedDocument(app, menuButton: "Mindmap", marker: translatedMindmapRoot)
    }

    // MARK: - Steps

    private func switchLanguage(_ app: XCUIApplication, to code: String) {
        let picker = app.buttons["player.languagePicker"]
        XCTAssertTrue(picker.waitForExistence(timeout: 10),
                      "language picker not shown (no ready translation advertised?)")
        var switched = false
        for _ in 0..<3 {
            picker.tap()
            let option = app.buttons["player.language.\(code)"]
            if option.waitForExistence(timeout: 4) {
                option.tap()
                switched = true
                break
            }
            dismissMenu(app)
        }
        XCTAssertTrue(switched, "\(code) not offered in the language menu")
    }

    /// Opens the Plan sheet and the speaker-models sheet behind its "Models"
    /// button. Regression coverage: both re-fetch the discussion, and used to
    /// drop the presentation language — the models sheet came up in the source
    /// language and, through the shared binding, flipped the plan's speaker
    /// text back to the original too.
    private func assertTranslatedPlanAndModels(_ app: XCUIApplication) throws {
        try openDocumentAction(app, button: "Plan")

        let editModels = app.buttons["plan.editModels"]
        XCTAssertTrue(editModels.waitForExistence(timeout: 15), "plan card did not load")
        XCTAssertTrue(anyText(app, containing: translatedHost).waitForExistence(timeout: 10),
                      "plan host name was not translated")
        XCTAssertTrue(anyText(app, containing: translatedDiscussant).exists,
                      "plan panelist name was not translated")

        editModels.tap()
        // The models sheet re-fetches the discussion; the translated names must
        // survive and the source-language names must never appear.
        XCTAssertTrue(anyText(app, containing: translatedDiscussant).waitForExistence(timeout: 10),
                      "speaker models sheet lost the translation")
        XCTAssertFalse(anyText(app, containing: "Alice").waitForExistence(timeout: 3),
                       "speaker models sheet shows source-language speaker names")

        closeSheet(app)

        // Back on the plan: the binding write-back from the models sheet must
        // not have reverted the plan to the source language.
        XCTAssertTrue(editModels.waitForExistence(timeout: 8), "plan sheet not visible after closing models")
        XCTAssertTrue(anyText(app, containing: translatedDiscussant).waitForExistence(timeout: 5),
                      "plan speaker text reverted to the source language after opening Models")
        XCTAssertFalse(anyText(app, containing: "Alice").exists,
                       "plan shows source-language speaker names after opening Models")

        closeSheet(app)
        XCTAssertTrue(playerOpened(app), "player not visible after closing the plan")
    }

    /// Expands the full-screen player and checks the lyrics page shows the
    /// translated cue text and speaker.
    private func assertTranslatedLyrics(_ app: XCUIApplication) throws {
        let expand = app.descendants(matching: .any).matching(identifier: "player.expand").firstMatch
        XCTAssertTrue(expand.waitForExistence(timeout: 10), "player bar expand area not found")
        expand.tap()

        // With cover artwork centered, lyrics hide behind the quote-bubble
        // toggle; without artwork the transcript is already visible.
        let lyricsToggle = app.buttons["player.lyrics"]
        if lyricsToggle.waitForExistence(timeout: 5) {
            lyricsToggle.tap()
        }
        XCTAssertTrue(anyText(app, containing: translatedLine).waitForExistence(timeout: 15),
                      "lyrics cue text was not translated")
        XCTAssertTrue(anyText(app, containing: translatedHost).exists,
                      "lyrics speaker label was not translated")

        let minimize = app.buttons["Minimize"]
        XCTAssertTrue(minimize.waitForExistence(timeout: 5), "full player minimize button not found")
        minimize.tap()
        XCTAssertTrue(playerOpened(app), "player not visible after minimizing the full player")
    }

    /// Opens a documents-menu sheet (Summary/Mindmap) and asserts a translated
    /// marker string renders inside it.
    private func assertTranslatedDocument(_ app: XCUIApplication, menuButton: String, marker: String) throws {
        try openDocumentAction(app, button: menuButton)
        XCTAssertTrue(anyText(app, containing: marker).waitForExistence(timeout: 15),
                      "\(menuButton) content was not translated")
        closeSheet(app)
        XCTAssertTrue(playerOpened(app), "player not visible after closing \(menuButton)")
    }

    // MARK: - Helpers

    /// Matches any element whose accessibility label contains the marker.
    /// Transcript bubbles and markdown views can merge or wrap strings, so
    /// exact staticText labels are too brittle.
    private func anyText(_ app: XCUIApplication, containing marker: String) -> XCUIElement {
        app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS %@", marker)).firstMatch
    }

    /// Taps an entry in the server-rendered documents menu, retrying the menu
    /// open (it can race with the player settling). Menu labels come from the
    /// backend localized by Accept-Language, which stays English in the
    /// simulator regardless of the podcast's presentation language.
    private func openDocumentAction(_ app: XCUIApplication, button: String) throws {
        for _ in 0..<4 {
            app.buttons["player.documents"].tap()
            let entry = app.buttons[button].firstMatch
            if entry.waitForExistence(timeout: 4) {
                entry.tap()
                return
            }
            dismissMenu(app)
        }
        XCTFail("\(button) not available in the documents menu")
    }

    /// Closes the top-most sheet via its Close/Done toolbar button, falling
    /// back to a swipe-down when the sheet has no explicit control.
    private func closeSheet(_ app: XCUIApplication) {
        for label in ["Close", "Done"] {
            let button = app.buttons[label].firstMatch
            if button.exists, button.isHittable {
                button.tap()
                return
            }
        }
        app.swipeDown(velocity: .fast)
    }
}
