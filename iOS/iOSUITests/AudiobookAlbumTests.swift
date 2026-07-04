//
//  AudiobookAlbumTests.swift
//  iOSUITests
//
//  E2E tests for audiobook chapter batches and albums, driven by the seeded
//  fixtures `test-audiobook` (root, chapters 1-3 generated of 12),
//  `test-audiobook-part2` (batch child, chapters 4-5), and their shared
//  `test-album`.
//

import XCTest

final class AudiobookAlbumTests: E2ETestCase {
    // MARK: - Helpers

    /// The home list collapses album members into one group row; opening it
    /// lands on the album page.
    private func openAlbum(_ app: XCUIApplication) {
        let albumRow = app.buttons["album.row.test-album"]
        XCTAssertTrue(albumRow.waitForExistence(timeout: 20), "album group row never appeared in the library")
        albumRow.tap()
        XCTAssertTrue(app.otherElements["album.view"].waitForExistence(timeout: 15)
                      || app.descendants(matching: .any).matching(identifier: "album.view").firstMatch.waitForExistence(timeout: 5),
                      "album view did not open")
    }

    /// Opens the seeded root audiobook's player by going through the album.
    private func openRootAudiobookPlayer(_ app: XCUIApplication) {
        openAlbum(app)
        let episode = app.descendants(matching: .any)
            .matching(identifier: "album.episode.test-audiobook").firstMatch
        XCTAssertTrue(episode.waitForExistence(timeout: 10), "root audiobook episode row not shown in the album")
        episode.tap()
        XCTAssertTrue(playerOpened(app), "player did not open for test-audiobook")
    }

    // MARK: - 1. Home groups linked podcasts into an album; album page lists episodes in order

    func testHomeGroupsAlbumAndAlbumPageListsEpisodes() throws {
        let app = launch()
        openAlbum(app)

        // Both members are listed; the flat home list must not show them as
        // individual rows (they are collapsed into the album group).
        let rootEpisode = app.descendants(matching: .any)
            .matching(identifier: "album.episode.test-audiobook").firstMatch
        let batchEpisode = app.descendants(matching: .any)
            .matching(identifier: "album.episode.test-audiobook-part2").firstMatch
        XCTAssertTrue(rootEpisode.waitForExistence(timeout: 10), "root episode missing from album page")
        XCTAssertTrue(batchEpisode.waitForExistence(timeout: 5), "batch episode missing from album page")

        // Chapter order: the root batch (chapters 1-3) sits above the
        // follow-up batch (chapters 4-5).
        XCTAssertLessThan(rootEpisode.frame.minY, batchEpisode.frame.minY,
                          "album episodes are not ordered by chapter")

        // Tapping an episode opens the player.
        rootEpisode.tap()
        XCTAssertTrue(playerOpened(app), "album episode did not open the player")
    }

    // MARK: - 2. Chapter checklist: done chapters locked, preselection, check/uncheck, 5-cap

    func testGenerateMoreChaptersChecklist() throws {
        let app = launch()
        openRootAudiobookPlayer(app)

        // The transcript ends with the generate-more call-to-action (7 of the
        // 12 chapters are still pending). It sits at the transcript bottom.
        let generateMore = app.buttons["player.generateMoreChapters"]
        if !generateMore.waitForExistence(timeout: 15) {
            app.swipeUp()
        }
        XCTAssertTrue(generateMore.waitForExistence(timeout: 10),
                      "generate-more footer button never appeared in the transcript")
        generateMore.tap()

        // The checklist sheet loads the chapter progress from the backend.
        let checklist = app.descendants(matching: .any)
            .matching(identifier: "chapters.checklist").firstMatch
        XCTAssertTrue(checklist.waitForExistence(timeout: 10), "chapter checklist sheet did not open")

        // Generated chapters are locked; pending chapters are tappable.
        let doneRow = app.buttons["chapters.row.1"]
        XCTAssertTrue(doneRow.waitForExistence(timeout: 10), "chapter 1 row not shown")
        XCTAssertFalse(doneRow.isEnabled, "generated chapter 1 should be locked")
        let firstPending = app.buttons["chapters.row.6"]
        XCTAssertTrue(firstPending.waitForExistence(timeout: 5), "pending chapter 6 row not shown")
        XCTAssertTrue(firstPending.isEnabled, "pending chapter 6 should be selectable")

        // The default batch size (3) preselects the first pending chapters.
        let generate = app.buttons["chapters.generate"]
        XCTAssertTrue(generate.waitForExistence(timeout: 5), "generate button not shown")
        XCTAssertTrue(waitForLabel(generate, containing: "3"),
                      "expected 3 preselected chapters, label: \(generate.label)")

        // Uncheck one, re-check it.
        firstPending.tap()
        XCTAssertTrue(waitForLabel(generate, containing: "2"),
                      "uncheck did not update the selection, label: \(generate.label)")
        firstPending.tap()
        XCTAssertTrue(waitForLabel(generate, containing: "3"),
                      "re-check did not update the selection, label: \(generate.label)")

        // Selecting beyond 5 is blocked client-side: check chapters 9 and 10
        // (total 5), then chapter 11 must not raise the count.
        app.buttons["chapters.row.9"].tap()
        app.buttons["chapters.row.10"].tap()
        XCTAssertTrue(waitForLabel(generate, containing: "5"),
                      "could not select up to the 5-chapter cap, label: \(generate.label)")
        let overCap = app.buttons["chapters.row.11"]
        if overCap.exists { overCap.tap() }
        XCTAssertTrue(waitForLabel(generate, containing: "5"),
                      "selection exceeded the 5-chapter cap, label: \(generate.label)")

        app.buttons["Cancel"].firstMatch.tap()
    }

    // MARK: - 3. Player toolbar exposes the album

    func testPlayerToolbarOpensAlbum() throws {
        let app = launch()
        openRootAudiobookPlayer(app)

        // The actions menu is server-rendered; retry the open in case the
        // items are still loading.
        var opened = false
        for _ in 0..<4 {
            app.buttons["player.more"].tap()
            let viewAlbum = app.buttons["View Album"].firstMatch
            if viewAlbum.waitForExistence(timeout: 4) {
                viewAlbum.tap()
                opened = true
                break
            }
            dismissMenu(app)
        }
        XCTAssertTrue(opened, "View Album action never appeared in the player menu")

        let albumView = app.descendants(matching: .any)
            .matching(identifier: "album.view").firstMatch
        XCTAssertTrue(albumView.waitForExistence(timeout: 10), "album sheet did not open from the toolbar")
        let batchEpisode = app.descendants(matching: .any)
            .matching(identifier: "album.episode.test-audiobook-part2").firstMatch
        XCTAssertTrue(batchEpisode.waitForExistence(timeout: 8), "album sheet is missing the batch episode")
    }

    // MARK: - 4. Album toolbar: actions menu opens the chapter checklist

    func testAlbumToolbarOpensChapterChecklist() throws {
        let app = launch()
        openAlbum(app)

        let menu = app.buttons["album.more"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10), "album actions menu not shown")

        // Chapter progress loads asynchronously; retry the menu until the
        // generate action is present.
        var opened = false
        for _ in 0..<4 {
            menu.tap()
            let generate = app.buttons["Generate More Chapters"].firstMatch
            if generate.waitForExistence(timeout: 4) {
                generate.tap()
                opened = true
                break
            }
            dismissMenu(app)
        }
        XCTAssertTrue(opened, "Generate More Chapters never appeared in the album menu")

        let checklist = app.descendants(matching: .any)
            .matching(identifier: "chapters.checklist").firstMatch
        XCTAssertTrue(checklist.waitForExistence(timeout: 10), "chapter checklist did not open from the album toolbar")
        XCTAssertTrue(app.buttons["chapters.row.6"].waitForExistence(timeout: 10), "pending chapter row missing")
        app.buttons["Cancel"].firstMatch.tap()
    }

    // MARK: - 5. Create a new album from the library toolbar's + menu

    func testCreateAlbumFromLibraryToolbar() throws {
        let app = launch()

        // The + toolbar button is a dropdown: New Station / New Album.
        app.buttons["library.new"].tap()
        let newAlbum = app.buttons["library.new.album"]
        XCTAssertTrue(newAlbum.waitForExistence(timeout: 5), "new-album menu item not found")
        newAlbum.tap()

        // The sheet renders the server-provided form (GET /api/precheck,
        // new_album); the title field carries a server-declared identifier.
        let titleField = app.descendants(matching: .any)
            .matching(identifier: "newAlbum.title").firstMatch
        XCTAssertTrue(titleField.waitForExistence(timeout: 15), "album name field missing")
        titleField.tap()
        titleField.typeText("My Manual Album")
        app.buttons["newAlbum.create"].tap()

        // Creation opens the (empty) album page so episodes can be added.
        let albumView = app.descendants(matching: .any)
            .matching(identifier: "album.view").firstMatch
        XCTAssertTrue(albumView.waitForExistence(timeout: 15), "album page did not open after creation")
        XCTAssertTrue(app.navigationBars["My Manual Album"].waitForExistence(timeout: 10),
                      "created album title not shown")
    }

    // MARK: - 6. Backend agreement: the chapters endpoint drives the checklist

    func testChapterProgressEndpointMatchesFixtures() throws {
        let url = URL(string: "\(baseURL)/api/discussions/test-audiobook/chapters")!
        var req = URLRequest(url: url)
        req.setValue("Bearer e2e-test-token", forHTTPHeaderField: "Authorization")
        let obj = try syncJSON(req)
        XCTAssertEqual(obj["root_id"] as? String, "test-audiobook")
        XCTAssertEqual(obj["max_batch_size"] as? Int, 5)
        guard let chapters = obj["chapters"] as? [[String: Any]] else {
            return XCTFail("chapters missing from response: \(obj)")
        }
        XCTAssertEqual(chapters.count, 12)
        let statusByIndex = Dictionary(uniqueKeysWithValues: chapters.map {
            (($0["index"] as? Int) ?? 0, ($0["status"] as? String) ?? "")
        })
        for idx in 1...5 {
            XCTAssertEqual(statusByIndex[idx], "done", "chapter \(idx) should be done")
        }
        for idx in 6...12 {
            XCTAssertEqual(statusByIndex[idx], "pending", "chapter \(idx) should be pending")
        }
    }

    // MARK: - Helpers

    /// Waits briefly for an element's label to contain the given substring
    /// (selection counts update asynchronously with animations).
    private func waitForLabel(_ element: XCUIElement, containing text: String, timeout: TimeInterval = 5) -> Bool {
        let predicate = NSPredicate(format: "label CONTAINS %@", text)
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: element)
        return XCTWaiter().wait(for: [expectation], timeout: timeout) == .completed
    }
}
