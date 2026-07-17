//
//  SemanticSearchTests.swift
//  iOSUITests
//
//  E2E tests for the home screen's global semantic content search. The fake
//  LLM's deterministic embeddings make token-overlapping queries rank the
//  seeded transcript chunks first, and the seeded podcasts are indexed at
//  server boot.
//

import XCTest

final class SemanticSearchTests: E2ETestCase {
    func testHomeSemanticSearchShowsGroupedMatchesAndOpensPlayer() throws {
        let app = launch()

        // The library row must exist first, so search results have data.
        XCTAssertTrue(findLibraryRow(app, id: "test-ready").waitForExistence(timeout: 20),
                      "seeded ready podcast never appeared")

        // Search lives behind the tab bar's search button.
        openHomeTab(app, "Search")
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10), "search field never appeared")
        search.tap()
        // A phrase from the seeded transcript, so token overlap ranks it first.
        search.typeText("technical angle system works")

        // A grouped result section appears with the matched chunk text and a
        // similarity badge.
        let matchedText = app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS 'technical angle'")).firstMatch
        XCTAssertTrue(matchedText.waitForExistence(timeout: 25), "matched chunk text never appeared")

        // The match row is one collapsed accessibility element (it's a
        // Button), so the badge's "N percent match" label merges into the
        // row's combined label.
        let scoreBadge = app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS 'percent match'")).firstMatch
        XCTAssertTrue(scoreBadge.waitForExistence(timeout: 10), "similarity badge never appeared")

        // Tapping the group header (or a match row) opens the podcast player.
        let header = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'search.result.'")).firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), "grouped podcast header never appeared")
        header.tap()

        XCTAssertTrue(playerOpened(app, timeout: 25), "tapping a search result did not open the player")
    }
}
