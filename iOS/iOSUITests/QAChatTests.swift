//
//  QAChatTests.swift
//  iOSUITests
//
//  E2E tests for the podcast Q&A chat and the global library chat. The fake
//  LLM scripts the agent loop (search_content → answer, plus one batch display
//  or highlight card in global scope), and the seeded ready podcasts
//  are indexed at server boot, so the flows run deterministically.
//

import XCTest

final class QAChatTests: E2ETestCase {
    /// The deterministic reply streamed by the fake LLM's QA branch
    /// (e2e.QAAnswerText). Kept in sync manually — it is a stable contract.
    private let answerMarker = "synthetic grounded answer"

    // MARK: - 1. Podcast Q&A: ask → tool card + answer → history persists

    func testPodcastQAAskAndHistoryPersists() throws {
        let app = launch()
        openLibraryRow(app, id: "test-ready")
        XCTAssertTrue(playerOpened(app), "player did not open for test-ready")

        openDocumentAction(app, button: "Ask")
        clearExistingChat(app)
        sendChatMessage(app, "What was discussed in this episode?")

        XCTAssertTrue(anyText(app, containing: answerMarker).waitForExistence(timeout: 40),
                      "the Q&A answer never streamed in")

        // Relaunch: the conversation is stored per podcast, so the history
        // must survive.
        let app2 = launch()
        openLibraryRow(app2, id: "test-ready")
        XCTAssertTrue(playerOpened(app2), "player did not reopen for test-ready")
        openDocumentAction(app2, button: "Ask")
        XCTAssertTrue(anyText(app2, containing: answerMarker).waitForExistence(timeout: 20),
                      "the Q&A history did not persist across relaunch")
    }

    // MARK: - 2. Podcast Q&A cannot be dismissed interactively

    func testPodcastQAInteractiveDismissIsDisabled() throws {
        let app = launch()
        openLibraryRow(app, id: "test-ready")
        XCTAssertTrue(playerOpened(app), "player did not open for test-ready")

        openDocumentAction(app, button: "Ask")
        clearExistingChat(app)
        let input = app.textFields["qa.input"].firstMatch
        XCTAssertTrue(input.waitForExistence(timeout: 15), "podcast Q&A chat never appeared")
        XCTAssertTrue(app.buttons["qa.clear"].waitForExistence(timeout: 5),
                      "podcast chat clear button was missing")

        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85))
        start.press(forDuration: 0.1, thenDragTo: end)

        XCTAssertTrue(input.waitForExistence(timeout: 3),
                      "podcast Q&A chat was dismissed interactively")
    }

    // MARK: - 3. Global chat back button restores the library

    func testGlobalChatBackButtonReturnsToLibrary() throws {
        let app = launch()

        openHomeTab(app, "Chat", timeout: 15)
        XCTAssertTrue(app.textFields["qa.input"].firstMatch.waitForExistence(timeout: 15),
                      "global chat never appeared")

        let back = app.navigationBars.buttons.firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 5), "global chat back button was missing")
        back.tap()

        XCTAssertTrue(app.buttons["library.account"].waitForExistence(timeout: 15),
                      "going back from global chat left the Home tab content blank")
    }

    // MARK: - 4. Global chat: one batch grid opens either podcast

    func testGlobalChatShowPodcastCardOpensPlayer() throws {
        let app = launch()

        openHomeTab(app, "Chat", timeout: 15)
        clearExistingChat(app)

        sendChatMessage(app, "Show me the podcast about testing")

        XCTAssertTrue(app.otherElements["qa.card.podcasts"].waitForExistence(timeout: 40),
                      "display_podcasts grid never appeared")
        XCTAssertTrue(app.buttons["qa.card.podcast.test-ready-summary"].exists,
                      "the second podcast was not included in the same grid")
        let card = app.buttons["qa.card.podcast.test-ready"]
        card.tap()

        XCTAssertTrue(playerOpened(app, timeout: 25), "tapping the podcast card did not open the player")
    }

    // MARK: - 5. Global chat: quotes from two podcasts share one tool card

    func testGlobalChatShowsBatchHighlightsWithoutRepeatingTitles() throws {
        let app = launch()

        openHomeTab(app, "Chat", timeout: 15)
        clearExistingChat(app)
        sendChatMessage(app, "Show highlights and quotes from my testing podcasts")

        let highlightCard = app.descendants(matching: .any)
            .matching(identifier: "qa.card.highlights").firstMatch
        XCTAssertTrue(highlightCard.waitForExistence(timeout: 40),
                      "batch highlight card never appeared")
        XCTAssertEqual(app.descendants(matching: .any)
            .matching(identifier: "qa.card.highlight-line").count, 2,
            "highlight lines were not grouped into one batch result")
        let titlePredicate = NSPredicate(format: "label == %@", "E2E Ready Podcast")
        let allTitleCount = app.staticTexts.matching(titlePredicate).count
        let cardTitleCount = highlightCard.descendants(matching: .staticText)
            .matching(titlePredicate).count
        XCTAssertGreaterThan(cardTitleCount, 0, "the rendered card was missing its podcast title")
        XCTAssertEqual(allTitleCount, cardTitleCount,
                       "the assistant repeated a podcast title outside the rendered card")
    }

    // MARK: - 6. Insufficient balance surfaces the top-up alert

    func testQAInsufficientBalanceShowsTopUp() throws {
        let app = launch()
        openLibraryRow(app, id: "test-ready")
        XCTAssertTrue(playerOpened(app), "player did not open for test-ready")

        openDocumentAction(app, button: "Ask")
        clearExistingChat(app)
        sendChatMessage(app, "e2e insufficient balance")

        let alert = anyText(app, containing: "points but have")
        XCTAssertTrue(alert.waitForExistence(timeout: 20), "insufficient-points alert never appeared")
        let topUp = app.buttons["Top Up"].firstMatch
        XCTAssertTrue(topUp.waitForExistence(timeout: 5), "Top Up option missing from the alert")
        app.buttons["OK"].firstMatch.tap()
    }

    // MARK: - Helpers

    private func sendChatMessage(_ app: XCUIApplication, _ text: String) {
        let input = app.textFields["qa.input"].firstMatch
        XCTAssertTrue(input.waitForExistence(timeout: 15), "chat input never appeared")
        input.tap()
        input.typeText(text)
        let send = app.buttons["qa.send"].firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 5), "send button missing")
        send.tap()
    }

    /// Clears persisted history left by another QA test or a test-plan
    /// repetition. An empty conversation keeps the toolbar button disabled, so
    /// waiting briefly for it to become enabled distinguishes old history from
    /// a clean start without coupling tests to execution order.
    private func clearExistingChat(_ app: XCUIApplication) {
        let clear = app.buttons["qa.clear"].firstMatch
        XCTAssertTrue(clear.waitForExistence(timeout: 5), "chat clear button was missing")

        let enabled = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "enabled == true"),
            object: clear
        )
        guard XCTWaiter.wait(for: [enabled], timeout: 2) == .completed else { return }

        clear.tap()
        let confirm = app.buttons["qa.clear.confirm"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "clear confirmation was missing")
        confirm.tap()

        let disabled = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "enabled == false"),
            object: clear
        )
        XCTAssertEqual(XCTWaiter.wait(for: [disabled], timeout: 10), .completed,
                       "persisted chat history was not cleared")
    }

    /// Taps an entry in the server-rendered documents menu, retrying the menu
    /// open (it can race with the player settling).
    private func openDocumentAction(_ app: XCUIApplication, button: String) {
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

    private func anyText(_ app: XCUIApplication, containing marker: String) -> XCUIElement {
        app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS %@", marker)).firstMatch
    }
}
