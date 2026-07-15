//
//  E2ETestCase.swift
//  iOSUITests
//
//  Shared base class for E2E UI tests. Provides launch helpers and backend
//  utilities used by both PodcastTests and DeeplinkTests.
//

import XCTest

class E2ETestCase: XCTestCase {
    var baseURL: String {
        ProcessInfo.processInfo.environment["E2E_API_BASE_URL"] ?? "http://127.0.0.1:8000"
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    // MARK: - Launch helpers

    @discardableResult
    func launch(deepLink: String? = nil, userID: String = "test", resetNewDiscussionSettings: Bool = true,
                noPermission: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        var env = ["E2E_TEST_MODE": "1", "E2E_API_BASE_URL": baseURL, "E2E_USER_ID": userID]
        if let deepLink { env["E2E_DEEP_LINK"] = deepLink }
        // Force the entitlements manager to resolve to `.none` so gated surfaces
        // render disabled without a network round-trip.
        if noPermission { env["E2E_NO_PERMISSION"] = "1" }
        app.launchEnvironment = env
        if resetNewDiscussionSettings {
            app.launchArguments += [
                "-newDiscussion.settings.type", "discussion",
                "-newDiscussion.settings.template", "default",
                "-newDiscussion.settings.discussants", "3",
                "-newDiscussion.settings.language", "en-US",
                "-newDiscussion.settings.generateCover", "NO",
                "-newDiscussion.settings.hasStoredValues", "NO",
            ]
        }
        app.launch()
        return app
    }

    /// The podcast player is open once its toolbar "more" button is present.
    func playerOpened(_ app: XCUIApplication, timeout: TimeInterval = 15) -> Bool {
        app.buttons["player.more"].waitForExistence(timeout: timeout)
    }

    /// Dismisses an open SwiftUI menu by tapping a safe point near the bottom edge.
    func dismissMenu(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.9)).tap()
    }

    func openLibraryRow(_ app: XCUIApplication, id: String, timeout: TimeInterval = 20) {
        let row = findLibraryRow(app, id: id, timeout: timeout)
        guard row.exists, row.isHittable else {
            return XCTFail("library row \(id) never became visible and tappable")
        }
        row.tap()
    }

    func findLibraryRow(_ app: XCUIApplication,
                        id: String,
                        timeout: TimeInterval = 20,
                        maxScrolls: Int = 8) -> XCUIElement {
        let row = app.buttons["discussion.row.\(id)"]
        if row.waitForExistence(timeout: timeout), libraryRowIsReady(row, in: app) {
            return row
        }

        for _ in 0..<maxScrolls {
            app.swipeUp()
            if row.waitForExistence(timeout: 2), libraryRowIsReady(row, in: app) {
                break
            }
        }
        return row
    }

    private func libraryRowIsReady(_ row: XCUIElement, in app: XCUIApplication) -> Bool {
        guard row.exists, row.isHittable else { return false }
        let rowFrame = row.frame
        let appFrame = app.frame
        // Keep the whole row above the floating search/tab controls. XCTest can
        // report a partially covered List row as hittable while tapping its
        // center still lands on the overlay.
        let unobscuredBottom = appFrame.maxY - 100
        return rowFrame.minY >= appFrame.minY && rowFrame.maxY <= unobscuredBottom
    }

    // MARK: - Backend helpers

    /// Mints a share token for a discussion straight from the backend.
    func createShareToken(discussionID: String) throws -> String {
        let url = URL(string: "\(baseURL)/api/discussions/\(discussionID)/shares")!
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("Bearer e2e-test-token", forHTTPHeaderField: "Authorization")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["ttl_seconds": 3600])
        let obj = try syncJSON(req)
        guard let token = obj["token"] as? String, !token.isEmpty else {
            throw NSError(domain: "e2e", code: 1, userInfo: [NSLocalizedDescriptionKey: "no token in share response"])
        }
        return token
    }

    /// GET a discussion as raw JSON text (for backend-side assertions).
    func fetchDiscussionRaw(_ id: String) throws -> String {
        let url = URL(string: "\(baseURL)/api/discussions/\(id)")!
        var req = URLRequest(url: url)
        req.setValue("Bearer e2e-test-token", forHTTPHeaderField: "Authorization")
        let (data, _) = try syncData(req)
        return String(decoding: data, as: UTF8.self)
    }

    func syncJSON(_ req: URLRequest) throws -> [String: Any] {
        let (data, _) = try syncData(req)
        guard let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw NSError(domain: "e2e", code: 2, userInfo: [NSLocalizedDescriptionKey: "non-object JSON"])
        }
        return obj
    }

    func syncData(_ req: URLRequest) throws -> (Data, HTTPURLResponse) {
        let sem = DispatchSemaphore(value: 0)
        var out: (Data, HTTPURLResponse)?
        var failure: Error?
        URLSession.shared.dataTask(with: req) { data, resp, err in
            defer { sem.signal() }
            if let err { failure = err; return }
            guard let data, let http = resp as? HTTPURLResponse else {
                failure = NSError(domain: "e2e", code: 3); return
            }
            out = (data, http)
        }.resume()
        _ = sem.wait(timeout: .now() + 20)
        if let failure { throw failure }
        guard let out else { throw NSError(domain: "e2e", code: 4, userInfo: [NSLocalizedDescriptionKey: "request timed out"]) }
        return out
    }
}
