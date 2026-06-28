//
//  DeeplinkTests.swift
//  iOSUITests
//
//  E2E tests covering deep link routing: share tokens, direct discussion links,
//  and ownership/visibility access control.
//

import XCTest

final class DeeplinkTests: E2ETestCase {
    // MARK: - 3. Ready plan → share → join via deep link

    func testReadyPlanShareAndJoinViaDeepLink() throws {
        let token = try createShareToken(discussionID: "test-ready")
        let app = launch(deepLink: "debatepod://s/\(token)")
        XCTAssertTrue(playerOpened(app, timeout: 25), "joining a shared ready podcast did not open the player")
    }

    // MARK: - 4. Ongoing plan → share → join via deep link

    func testOngoingPlanShareAndJoinViaDeepLink() throws {
        let token = try createShareToken(discussionID: "test-ongoing")
        let app = launch(deepLink: "debatepod://s/\(token)")
        XCTAssertFalse(playerOpened(app, timeout: 25), "joining a shared ongoing podcast did not open the player")
    }

    // MARK: - 6. Deep link to a private podcast not owned by the user → denied

    func testDeepLinkNotOwnedPrivateDenied() throws {
        let app = launch(deepLink: "debatepod://d/test2-private")
        // A private podcast owned by someone else must not open.
        XCTAssertFalse(playerOpened(app, timeout: 8),
                       "a non-owned private podcast should not be openable")
    }

    // MARK: - 7. Deep link to a public podcast not owned by the user → allowed

    func testDeepLinkNotOwnedPublicAllowed() throws {
        let app = launch(deepLink: "debatepod://d/test2-public")
        XCTAssertTrue(playerOpened(app, timeout: 25),
                      "a public podcast should open via deep link")
    }

    // MARK: - 8. Deep link to a podcast owned by the user → allowed

    func testDeepLinkOwned() throws {
        let app = launch(deepLink: "debatepod://d/test-ready")
        XCTAssertTrue(playerOpened(app, timeout: 25),
                      "the user's own podcast should open via deep link")
    }
}
