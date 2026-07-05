//
//  MarketplaceTests.swift
//  iOSUITests
//
//  E2E tests for marketplace navigation.
//

import XCTest

final class MarketplaceTests: E2ETestCase {
    func testMarketplacePodcastDetailsTapOpensPlayer() throws {
        let app = launch()

        openMarketplace(app)

        let details = findMarketStationDetails(app, id: "test-market-podcast")
        XCTAssertTrue(details.waitForExistence(timeout: 20), "market podcast details target never appeared")
        details.tap()

        XCTAssertTrue(playerOpened(app), "tapping the market podcast details did not open the player")
    }

    func testMarketplaceAlbumEpisodeTapOpensPlayer() throws {
        let app = launch()

        openMarketplace(app)

        let album = findMarketAlbumDetails(app, id: "test-market-album")
        XCTAssertTrue(album.waitForExistence(timeout: 20), "market album details target never appeared")
        album.tap()

        let albumView = app.descendants(matching: .any)
            .matching(identifier: "album.view").firstMatch
        XCTAssertTrue(albumView.waitForExistence(timeout: 10), "market album did not open")

        let episode = app.descendants(matching: .any)
            .matching(identifier: "album.episode.test-market-public").firstMatch
        XCTAssertTrue(episode.waitForExistence(timeout: 8), "public album episode missing")
        episode.tap()

        XCTAssertTrue(playerOpened(app), "tapping the market album episode did not open the player")
    }

    private func openMarketplace(_ app: XCUIApplication) {
        let market = app.buttons["library.market"].firstMatch
        XCTAssertTrue(market.waitForExistence(timeout: 10), "market toolbar button never appeared")
        market.tap()
    }

    private func findMarketStationDetails(_ app: XCUIApplication, id: String) -> XCUIElement {
        findMarketDetails(app, identifier: "market.station.\(id).details")
    }

    private func findMarketAlbumDetails(_ app: XCUIApplication, id: String) -> XCUIElement {
        findMarketDetails(app, identifier: "market.album.\(id).details")
    }

    private func findMarketDetails(_ app: XCUIApplication, identifier: String) -> XCUIElement {
        let element = app.descendants(matching: .any)
            .matching(identifier: identifier).firstMatch
        if element.waitForExistence(timeout: 5) {
            return element
        }
        for _ in 0..<4 {
            app.swipeUp()
            if element.waitForExistence(timeout: 2) {
                break
            }
        }
        return element
    }
}
