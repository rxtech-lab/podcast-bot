import XCTest
@testable import iOS

final class WhatsNewFeaturePresentationTests: XCTestCase {
    func testFourFeaturesRemainOneFeaturePerPage() {
        let features = Array(WhatsNewFeature.all.prefix(4))

        let pages = WhatsNewFeature.presentationPages(for: features)

        XCTAssertEqual(pages.map(\.count), [1, 1, 1, 1])
        XCTAssertEqual(pages.flatMap { $0 }, features)
    }

    func testMoreThanFourFeaturesUseBalancedMultiFeaturePages() {
        let features = Array(WhatsNewFeature.all.prefix(5))

        let pages = WhatsNewFeature.presentationPages(for: features)

        XCTAssertEqual(pages.map(\.count), [3, 2])
        XCTAssertEqual(pages.flatMap { $0 }, features)
    }

    func testLargeFeatureSetCapsPagesAtFourAndPreservesOrder() {
        let features = WhatsNewFeature.all

        let pages = WhatsNewFeature.presentationPages(for: features)

        XCTAssertEqual(pages.map(\.count), [4, 4, 3])
        XCTAssertEqual(pages.flatMap { $0 }, features)
    }
}
