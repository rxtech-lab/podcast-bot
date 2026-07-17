import XCTest
@testable import iOS

final class MessageListPinningControllerTests: XCTestCase {
    func testCompletedConversationCanRehydrateLatestUserPin() {
        var controller = MessageListPinningController<String>()

        let action = controller.handleLastMessageChange(
            id: "latest-user-message",
            isUserMessage: true,
            isStreaming: false,
            isAtBottom: true
        )

        XCTAssertEqual(action, .pinUserMessageToTop("latest-user-message"))
        XCTAssertTrue(controller.isPinningUserMessage)
        XCTAssertEqual(controller.pinnedUserMessageID, "latest-user-message")
    }

    func testStreamingCompletionKeepsFinalizedUserMessageTracked() {
        var controller = MessageListPinningController<String>()

        _ = controller.handleLastMessageChange(
            id: "local-user-message",
            isUserMessage: true,
            isStreaming: true,
            isAtBottom: true
        )
        controller.releasePin()
        controller.handleUserMessageIDReplacement(
            from: "local-user-message",
            to: "persisted-user-message",
            streamWasFinishing: true
        )

        XCTAssertFalse(controller.isPinningUserMessage)
        XCTAssertEqual(controller.pinnedUserMessageID, "persisted-user-message")
    }

    func testFinalizedUserMessageDoesNotCreateAPinWithoutAnExistingTurn() {
        var controller = MessageListPinningController<String>()

        controller.handleUserMessageIDReplacement(
            from: "local-user-message",
            to: "persisted-user-message",
            streamWasFinishing: true
        )

        XCTAssertNil(controller.pinnedUserMessageID)
    }

    func testUserMessageReplacementOutsideStreamCompletionDoesNotMovePin() {
        var controller = MessageListPinningController<String>()

        _ = controller.handleLastMessageChange(
            id: "existing-user-message",
            isUserMessage: true,
            isStreaming: false,
            isAtBottom: true
        )
        controller.handleUserMessageIDReplacement(
            from: "existing-user-message",
            to: "different-conversation-user-message",
            streamWasFinishing: false
        )

        XCTAssertEqual(controller.pinnedUserMessageID, "existing-user-message")
    }
}
