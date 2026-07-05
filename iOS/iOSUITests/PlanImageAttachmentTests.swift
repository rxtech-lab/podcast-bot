//
//  PlanImageAttachmentTests.swift
//  iOSUITests
//
//  E2E test proving an attached image actually reaches the model. The fake LLM
//  (internal/e2e/fakellm.go) treats a prompt containing "this image" as an
//  image probe: it replies with a success marker only when a user turn carries
//  a real multimodal image part, and with "E2E ERROR: no image attachment
//  reached the model." when the image was dropped anywhere along the
//  form → server → persisted turn → LLM history pipeline.
//

import XCTest

final class PlanImageAttachmentTests: E2ETestCase {
    func testAttachedImageReachesModel() throws {
        let app = launch()
        app.buttons["library.create"].tap()
        let newStation = app.buttons["library.new-station"]
        XCTAssertTrue(newStation.waitForExistence(timeout: 5), "new-station menu item not found")
        newStation.tap()

        // Type the image-probe prompt into the server-rendered topic field.
        let topic = app.descendants(matching: .any)
            .matching(identifier: "newPlan.field").firstMatch
        XCTAssertTrue(topic.waitForExistence(timeout: 15), "topic field not found")
        topic.tap()
        topic.typeText("Tell a story from this image")

        // Attach the E2E sample image through the attachments menu. The card
        // sits below the tall topic box, so scroll it on screen first.
        let attach = app.buttons["newPlan.attachments.add"]
        if !attach.waitForExistence(timeout: 3) {
            app.swipeUp()
        }
        XCTAssertTrue(attach.waitForExistence(timeout: 5), "attachments menu not found")
        attach.tap()
        // Menu items usually surface by identifier; fall back to the label.
        var sample = app.buttons["attachments.e2e-sample-image"]
        if !sample.waitForExistence(timeout: 3) {
            sample = app.buttons["Sample Image (E2E)"]
        }
        XCTAssertTrue(sample.waitForExistence(timeout: 5), "E2E sample image menu item not found")
        sample.tap()

        // The injected attachment renders as a ready chip.
        XCTAssertTrue(app.staticTexts["E2E-Sample.png"].waitForExistence(timeout: 5),
                      "sample image chip did not appear")

        app.buttons["newPlan.submit"].tap()

        // The fake LLM acknowledges the image only if the multimodal part
        // survived the whole pipeline. The reply bubble is markdown-rendered
        // and surfaces as a TextView, so match on label across any type.
        let success = app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS 'can see the attached image'"))
            .firstMatch
        XCTAssertTrue(success.waitForExistence(timeout: 40),
                      "model never acknowledged the attached image")
        XCTAssertFalse(
            app.descendants(matching: .any)
                .matching(NSPredicate(format: "label CONTAINS 'no image attachment reached the model'"))
                .firstMatch.exists,
            "fake LLM reported the image was dropped before reaching the model")
    }
}
