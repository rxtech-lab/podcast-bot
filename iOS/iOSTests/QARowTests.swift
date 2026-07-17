import XCTest
@testable import iOS

final class QARowTests: XCTestCase {
    func testUserTextPartOptsIntoPinnedUserMessageBehavior() {
        let part = QAPart(kind: "text", id: "user-message", role: "user", text: "Question")

        XCTAssertTrue(QARow(id: part.id, content: .part(part)).isUserMessage)
    }

    func testAssistantAndLoadingRowsDoNotOptIntoPinnedUserMessageBehavior() {
        let assistant = QAPart(kind: "text", id: "assistant-message", role: "assistant", text: "Answer")

        XCTAssertFalse(QARow(id: assistant.id, content: .part(assistant)).isUserMessage)
        XCTAssertFalse(QARow(id: "loading", content: .loading).isUserMessage)
    }
}
