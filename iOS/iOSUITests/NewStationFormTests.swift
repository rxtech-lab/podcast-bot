//
//  NewStationFormTests.swift
//  iOSUITests
//
//  E2E tests for the server-driven New Station form.
//

import XCTest

final class NewStationFormTests: E2ETestCase {
    /// The Panelists (discussants) row only applies to discussions: the server
    /// schema hides it behind an if/then conditional on the selected type, so
    /// switching to Audio Book must remove the row and switching back must
    /// restore it.
    func testPanelistsHiddenForAudioBookType() throws {
        let app = launch()
        app.buttons["library.create"].tap()
        let newStation = app.buttons["library.new-station"]
        XCTAssertTrue(newStation.waitForExistence(timeout: 5), "new-station menu item not found")
        newStation.tap()

        // Server-rendered form loaded once the topic field is present.
        let topic = app.descendants(matching: .any)
            .matching(identifier: "newPlan.field").firstMatch
        XCTAssertTrue(topic.waitForExistence(timeout: 15), "topic field not found")

        // The settings card sits below the tall topic box; bring it on screen.
        let panelists = app.staticTexts["Panelists"]
        if !panelists.waitForExistence(timeout: 3) {
            app.swipeUp()
        }
        XCTAssertTrue(panelists.waitForExistence(timeout: 5),
                      "Panelists row should be visible for the default Discussion type")

        // Switch Type to Audio Book via the glass menu row.
        let typeRow = app.buttons.matching(NSPredicate(format: "label CONTAINS 'Type'")).firstMatch
        XCTAssertTrue(typeRow.waitForExistence(timeout: 5), "Type menu row not found")
        typeRow.tap()
        let audioBook = app.buttons["Audio Book"]
        XCTAssertTrue(audioBook.waitForExistence(timeout: 5), "Audio Book option not offered")
        audioBook.tap()

        // The conditional hides the Panelists row for audiobooks.
        let hidden = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "exists == false"), object: panelists)
        XCTAssertEqual(XCTWaiter().wait(for: [hidden], timeout: 8), .completed,
                       "Panelists row should disappear for Audio Book")

        // Switching back to Discussion restores the row.
        typeRow.tap()
        let discussion = app.buttons["Discussion"]
        XCTAssertTrue(discussion.waitForExistence(timeout: 5), "Discussion option not offered")
        discussion.tap()
        XCTAssertTrue(panelists.waitForExistence(timeout: 8),
                      "Panelists row should reappear for Discussion")
    }
}
