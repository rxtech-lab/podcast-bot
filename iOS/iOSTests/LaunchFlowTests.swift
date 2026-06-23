//
//  LaunchFlowTests.swift
//  iOSTests
//
//  Covers the launch-flow decision logic (Welcome / What's New / Paywall combos)
//  and the on-device seen-state persistence.
//

import XCTest
@testable import iOS

final class LaunchFlowPlanTests: XCTestCase {
    private let features = WhatsNewFeature.all

    // MARK: - Combos

    func testFirstLaunchNotPro_showsWelcomeWhatsNewPaywall() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: false,
                                         unseenFeatures: features,
                                         isPro: false)
        XCTAssertEqual(steps, [.welcome, .whatsNew(features), .paywall])
    }

    func testFirstLaunchPro_showsWelcomeAndWhatsNew_noPaywall() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: false,
                                         unseenFeatures: features,
                                         isPro: true)
        XCTAssertEqual(steps, [.welcome, .whatsNew(features)])
    }

    func testReturningWithUnseenFeaturesNotPro_showsOnlyWhatsNew() {
        // Per product decision: returning non-subscribers are NOT auto-shown the
        // paywall — it's first-launch-only.
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: true,
                                         unseenFeatures: features,
                                         isPro: false)
        XCTAssertEqual(steps, [.whatsNew(features)])
    }

    func testReturningWithUnseenFeaturesPro_showsOnlyWhatsNew() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: true,
                                         unseenFeatures: features,
                                         isPro: true)
        XCTAssertEqual(steps, [.whatsNew(features)])
    }

    func testReturningNoUnseenNotPro_showsNothing() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: true,
                                         unseenFeatures: [],
                                         isPro: false)
        XCTAssertTrue(steps.isEmpty)
    }

    func testReturningNoUnseenPro_showsNothing() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: true,
                                         unseenFeatures: [],
                                         isPro: true)
        XCTAssertTrue(steps.isEmpty)
    }

    func testStepOrderingIsWelcomeThenWhatsNewThenPaywall() {
        let steps = LaunchFlowPlan.steps(hasSeenWelcome: false,
                                         unseenFeatures: features,
                                         isPro: false)
        XCTAssertEqual(steps.first, .welcome)
        XCTAssertEqual(steps.last, .paywall)
    }
}

@MainActor
final class LaunchFlowStoreTests: XCTestCase {
    private var suiteName: String!
    private var defaults: UserDefaults!
    private var store: LaunchFlowStore!

    override func setUp() {
        super.setUp()
        suiteName = "LaunchFlowStoreTests.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suiteName)
        store = LaunchFlowStore(defaults: defaults)
    }

    override func tearDown() {
        defaults.removePersistentDomain(forName: suiteName)
        defaults = nil
        store = nil
        suiteName = nil
        super.tearDown()
    }

    func testEmptyStore_allFeaturesUnseen_welcomeNotSeen() {
        XCTAssertFalse(store.hasSeenWelcome)
        XCTAssertEqual(store.unseenFeatures, WhatsNewFeature.all)
    }

    func testMarkWelcomeSeen_persists() {
        store.markWelcomeSeen()
        XCTAssertTrue(store.hasSeenWelcome)
        // A fresh store on the same suite still reads the persisted value.
        let reloaded = LaunchFlowStore(defaults: defaults)
        XCTAssertTrue(reloaded.hasSeenWelcome)
    }

    func testMarkFeaturesSeen_removesFromUnseen() {
        let first = WhatsNewFeature.all.first!
        store.markFeaturesSeen([first.id])
        XCTAssertFalse(store.unseenFeatures.contains(first))
        XCTAssertEqual(store.unseenFeatures.count, WhatsNewFeature.all.count - 1)
    }

    func testMarkFeaturesSeen_isIdempotentAndUnions() {
        let ids = WhatsNewFeature.all.map(\.id)
        store.markFeaturesSeen([ids[0]])
        store.markFeaturesSeen([ids[0]])          // duplicate
        store.markFeaturesSeen(Array(ids.dropFirst())) // the rest
        XCTAssertEqual(store.seenFeatureIDs, Set(ids))
        XCTAssertTrue(store.unseenFeatures.isEmpty)
    }

    func testMarkAllFeaturesSeen_emptyUnseen() {
        store.markFeaturesSeen(WhatsNewFeature.all.map(\.id))
        XCTAssertTrue(store.unseenFeatures.isEmpty)
    }

    func testUnknownSeenIDsDoNotBreakDiff() {
        store.markFeaturesSeen(["a-feature-that-no-longer-exists"])
        // All current features are still unseen; the stale id is harmless.
        XCTAssertEqual(store.unseenFeatures, WhatsNewFeature.all)
    }

    func testMarkFeaturesSeenWithEmptyArray_isNoOp() {
        store.markFeaturesSeen([])
        XCTAssertEqual(store.unseenFeatures, WhatsNewFeature.all)
    }
}
