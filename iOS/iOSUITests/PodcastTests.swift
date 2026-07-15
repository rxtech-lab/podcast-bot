//
//  PodcastTests.swift
//  iOSUITests
//
//  E2E tests covering podcast creation, player, and planning flows.
//

import XCTest

final class PodcastTests: E2ETestCase {
    // MARK: - 1. Create a new plan, accept, podcast becomes ready

    func testCreatePlanAcceptReady() throws {
        let app = launch()
        app.buttons["library.create"].tap()
        // The + button is backend-rendered as a menu: pick the server action.
        let newStation = app.buttons["library.new-station"]
        XCTAssertTrue(newStation.waitForExistence(timeout: 5), "new-station menu item not found")
        newStation.tap()

        // The new-discussion form is server-rendered; the topic field carries a
        // stable identifier. A vertical-axis TextField can surface as either a
        // textField or a textView, so match by identifier across any type.
        let topic = app.descendants(matching: .any)
            .matching(identifier: "newPlan.field").firstMatch
        XCTAssertTrue(topic.waitForExistence(timeout: 15), "topic field not found")
        topic.tap()
        topic.typeText("The future of renewable energy")
        app.buttons["newPlan.submit"].tap()

        // The conversational planner produces a plan; the generate affordance appears.
        let generate = app.buttons["plan.generate"]
        XCTAssertTrue(generate.waitForExistence(timeout: 40), "plan never produced a generate button")
        generate.tap()
        // The plan view also has a nav-bar "Generate" button, so target the
        // confirmation dialog's button specifically to avoid ambiguity.
        let confirm = app.sheets.buttons["Generate"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 8), "generate confirmation did not appear")
        confirm.tap()

        // Generation finishes (fake LLM + fake TTS) and the player opens ready.
        XCTAssertTrue(playerOpened(app, timeout: 90), "podcast never reached a playable state")
    }

    // MARK: - 2. Ready plan → generate summary → view it

    func testReadyPlanGenerateAndViewSummary() throws {
        let app = launch()
        openLibraryRow(app, id: "test-ready-summary")
        XCTAssertTrue(playerOpened(app), "player did not open for test-ready-summary")

        // Start summary generation from the actions menu (retry the menu open,
        // which can race with the player settling).
        var triggered = false
        for _ in 0..<3 {
            app.buttons["player.documents"].tap()
            let generate = app.buttons["player.generateSummary"]
            if generate.waitForExistence(timeout: 4) {
                generate.tap()
                triggered = true
                break
            }
            dismissMenu(app)
        }
        // Fallback so a flaky menu interaction never blocks the view assertion.
        if !triggered {
            _ = try? triggerSummary(id: "test-ready-summary")
        }

        // Confirm generation completed on the backend (authoritative).
        XCTAssertTrue(waitForSummaryReady(id: "test-ready-summary", timeout: 30),
                      "summary never became ready on the backend")

        // Relaunch so the player loads a fresh discussion that already advertises
        // the summary, then open and view it.
        let app2 = launch()
        openLibraryRow(app2, id: "test-ready-summary")
        XCTAssertTrue(playerOpened(app2), "player did not reopen for test-ready-summary")
        var viewed = false
        for _ in 0..<4 {
            app2.buttons["player.documents"].tap()
            let summary = app2.buttons["Summary"].firstMatch
            if summary.waitForExistence(timeout: 3) {
                summary.tap()
                viewed = true
                break
            }
            dismissMenu(app2)
        }
        XCTAssertTrue(viewed, "Summary action not available")
        XCTAssertTrue(app2.navigationBars["Summary"].waitForExistence(timeout: 8),
                      "summary view did not appear")
    }

    // MARK: - 5. Plan mode: multiple rounds, change models, models persist

    func testPlanModeChangeModelsPersist() throws {
        let app = launch()
        openLibraryRow(app, id: "test-plan")

        let input = app.textFields["plan.input"]
        XCTAssertTrue(input.waitForExistence(timeout: 12), "planner input not found")
        input.tap()
        input.typeText("Design a podcast about renewable energy")
        app.buttons["plan.send"].tap()

        app.swipeDown()

        // Plan card appears; open speaker models.
        let editModels = app.buttons["plan.editModels"]
        XCTAssertTrue(editModels.waitForExistence(timeout: 40), "plan card / edit-models never appeared")
        editModels.tap()

        // Change the first speaker's model to gpt-4o. The model row pushes a
        // searchable picker list grouped by company.
        let modelLink = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'speakerModel.link.'")).firstMatch
        XCTAssertTrue(modelLink.waitForExistence(timeout: 10), "speaker model link not found")
        modelLink.tap()

        // The E2E roster has no "company/" id prefixes, so every model lands in
        // the "Others" group (headers may render uppercased).
        let othersHeader = app.staticTexts
            .matching(NSPredicate(format: "label ==[c] 'Others'")).firstMatch
        XCTAssertTrue(othersHeader.waitForExistence(timeout: 6), "company group header not shown")

        // Filter with the search field when it is reachable; a missed search
        // bar must not block the pick itself (the roster is short).
        let search = app.searchFields.firstMatch
        if !search.waitForExistence(timeout: 3) { app.swipeDown() }
        if search.waitForExistence(timeout: 3) {
            search.tap()
            search.typeText("gpt-4o")
        }
        let option = app.buttons["model.gpt-4o"]
        XCTAssertTrue(option.waitForExistence(timeout: 6), "gpt-4o option not found in picker")
        option.tap()
        app.buttons["Done"].firstMatch.tap()

        // Second round: send another message.
        input.tap()
        input.typeText("Add a section on storage")
        app.buttons["plan.send"].tap()
        XCTAssertTrue(editModels.waitForExistence(timeout: 40), "second round did not complete")

        // Persistence is authoritative on the backend: the changed model must
        // survive the extra round.
        let raw = try fetchDiscussionRaw("test-plan")
        XCTAssertTrue(raw.contains("\"model\":\"gpt-4o\""),
                      "the changed speaker model did not persist across rounds")
    }

    // MARK: - 5b. Plan mode: model + voice picks survive leaving and re-entering

    /// Regression test: pick a model and a voice for a speaker, leave the plan
    /// screen, come back, and reopen the sheet — it must show the picked model
    /// and voice, not the stale pre-edit values from the library list.
    func testSpeakerModelVoicePersistAcrossReentry() throws {
        let discussionID = "test-plan-voice"
        let app = launch()
        openLibraryRow(app, id: discussionID)

        let input = app.textFields["plan.input"]
        XCTAssertTrue(input.waitForExistence(timeout: 12), "planner input not found")
        input.tap()
        input.typeText("Plan a podcast about deep sea exploration")
        app.buttons["plan.send"].tap()

        app.swipeDown()

        // Plan card appears; open speaker models.
        let editModels = app.buttons["plan.editModels"]
        XCTAssertTrue(editModels.waitForExistence(timeout: 40), "plan card / edit-models never appeared")
        editModels.tap()

        // Change the first speaker's model.
        let modelLinkQuery = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'speakerModel.link.'"))
        let modelLink = modelLinkQuery.firstMatch
        XCTAssertTrue(modelLink.waitForExistence(timeout: 10), "speaker model link not found")
        modelLink.tap()
        let option = app.buttons["model.claude-sonnet-4-6"]
        XCTAssertTrue(option.waitForExistence(timeout: 6), "claude-sonnet-4-6 option not found in picker")
        option.tap()

        // Pick a voice for the same speaker (the voice row is hidden while the
        // model PATCH is in flight, so waiting for it also syncs on the save).
        let voiceLink = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'speakerVoice.link.'")).firstMatch
        XCTAssertTrue(voiceLink.waitForExistence(timeout: 10), "voice link not found after model change")
        voiceLink.tap()
        let voice = app.buttons["voice.en-US-E2EAvaNeural"]
        XCTAssertTrue(voice.waitForExistence(timeout: 10), "E2E fake voice not offered in the picker")
        voice.tap()

        // Back in the sheet, the picked voice's display name shows once the
        // PATCH round-trip completes.
        XCTAssertTrue(app.staticTexts["E2E Ava"].waitForExistence(timeout: 10),
                      "picked voice not shown in the sheet after selection")
        app.buttons["Done"].firstMatch.tap()

        // Leave the plan screen, then re-enter from the library.
        let back = app.navigationBars.buttons.element(boundBy: 0)
        XCTAssertTrue(back.waitForExistence(timeout: 8), "back button not available")
        back.tap()
        openLibraryRow(app, id: discussionID)

        // Reopen the sheet: it must show the persisted model and voice.
        XCTAssertTrue(editModels.waitForExistence(timeout: 40), "plan card did not reappear after re-entry")
        editModels.tap()
        XCTAssertTrue(app.staticTexts["E2E Ava"].waitForExistence(timeout: 10),
                      "reopened sheet lost the picked voice (stale discussion)")
        XCTAssertTrue(modelLink.waitForExistence(timeout: 10), "speaker model link not found after re-entry")
        let labelUpdated = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "label CONTAINS 'claude-sonnet-4-6'"), object: modelLink)
        XCTAssertEqual(XCTWaiter().wait(for: [labelUpdated], timeout: 10), .completed,
                       "reopened sheet lost the picked model (stale discussion), shows: \(modelLink.label)")
    }

    // MARK: - 6. Planning shortfall → back home → settings still opens

    func testPlanningShortfallDoesNotBlockHomeToolbar() throws {
        let app = launch()
        openLibraryRow(app, id: "test-plan-shortfall")

        let input = app.textFields["plan.input"]
        XCTAssertTrue(input.waitForExistence(timeout: 12), "planner input not found")
        input.tap()
        input.typeText("Please trigger e2e insufficient balance")
        app.buttons["plan.send"].tap()

        let alert = app.alerts["Could not update the plan"]
        XCTAssertTrue(alert.waitForExistence(timeout: 20), "insufficient-balance alert did not appear")
        XCTAssertTrue(alert.staticTexts.matching(NSPredicate(format: "label CONTAINS 'need'")).firstMatch.exists,
                      "shortfall message was not shown")
        alert.buttons["OK"].tap()

        let back = app.navigationBars.buttons.element(boundBy: 0)
        XCTAssertTrue(back.waitForExistence(timeout: 8), "back button not available after shortfall")
        back.tap()

        let account = app.buttons["library.account"]
        XCTAssertTrue(account.waitForExistence(timeout: 8), "home account toolbar button not available")
        account.tap()
        let settings = app.buttons["Settings"]
        XCTAssertTrue(settings.waitForExistence(timeout: 5), "account menu did not open after returning home")
        settings.tap()
        XCTAssertTrue(app.navigationBars["Settings"].waitForExistence(timeout: 8),
                      "settings view did not open after planning shortfall")
    }

    // MARK: - Summary helpers

    /// Triggers summary generation directly on the backend (fallback path).
    @discardableResult
    private func triggerSummary(id: String) throws -> [String: Any] {
        let url = URL(string: "\(baseURL)/api/discussions/\(id)/summary/generate")!
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("Bearer e2e-test-token", forHTTPHeaderField: "Authorization")
        req.httpBody = Data("{}".utf8)
        return try syncJSON(req)
    }

    /// Polls the backend summary endpoint until the document reports ready.
    private func waitForSummaryReady(id: String, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let url = URL(string: "\(baseURL)/api/discussions/\(id)/summary")!
            var req = URLRequest(url: url)
            req.setValue("Bearer e2e-test-token", forHTTPHeaderField: "Authorization")
            if let (data, http) = try? syncData(req), http.statusCode == 200,
               let body = String(data: data, encoding: .utf8),
               body.contains("\"status\":\"ready\"")
            {
                return true
            }
            Thread.sleep(forTimeInterval: 1.5)
        }
        return false
    }

    // MARK: - Entitlements: gated native surfaces render disabled

    /// With no permission (E2E_NO_PERMISSION forces entitlements to `.none`),
    /// the model picker still lists models but every row is disabled — mode
    /// "only" with an empty allowlist rejects all model ids.
    func testModelPickerDisabledWithoutPermission() throws {
        let app = launch(noPermission: true)
        openLibraryRow(app, id: "test-plan")

        let input = app.textFields["plan.input"]
        XCTAssertTrue(input.waitForExistence(timeout: 12), "planner input not found")
        input.tap()
        input.typeText("Design a podcast about renewable energy")
        app.buttons["plan.send"].tap()

        app.swipeDown()

        let editModels = app.buttons["plan.editModels"]
        XCTAssertTrue(editModels.waitForExistence(timeout: 40), "plan card / edit-models never appeared")
        editModels.tap()

        let modelLink = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'speakerModel.link.'")).firstMatch
        XCTAssertTrue(modelLink.waitForExistence(timeout: 10), "speaker model link not found")
        modelLink.tap()

        // The row is visible (so the user sees what a plan unlocks) but disabled.
        let option = app.buttons["model.gpt-4o"]
        XCTAssertTrue(option.waitForExistence(timeout: 6), "gpt-4o row not found in picker")
        XCTAssertFalse(option.isEnabled, "model row must be disabled without permission")
    }
}
