//
//  MindmapTests.swift
//  iOSTests
//
//  Covers the mindmap tree operations (add/rename/delete/parent lookup) and
//  the pure layout engine (frames, fold behavior, content sizing).
//

import XCTest
@testable import iOS

final class MindmapTests: XCTestCase {
    private func sampleTree() -> MindmapNode {
        MindmapNode(id: "root", title: "Topic", children: [
            MindmapNode(id: "a", title: "Theme A", children: [
                MindmapNode(id: "a1", title: "Point A1"),
                MindmapNode(id: "a2", title: "Point A2"),
            ]),
            MindmapNode(id: "b", title: "Theme B"),
        ])
    }

    // MARK: - Tree operations

    func testNodeLookupAndParent() {
        let tree = sampleTree()
        XCTAssertEqual(tree.node(withID: "a1")?.title, "Point A1")
        XCTAssertNil(tree.node(withID: "missing"))
        XCTAssertEqual(tree.parentID(of: "a1"), "a")
        XCTAssertEqual(tree.parentID(of: "a"), "root")
        XCTAssertNil(tree.parentID(of: "root"))
        XCTAssertEqual(tree.nodeCount, 5)
    }

    func testInsertChildAndSibling() {
        var tree = sampleTree()
        XCTAssertTrue(tree.insertChild(MindmapNode(id: "b1", title: "Point B1"), under: "b"))
        XCTAssertEqual(tree.node(withID: "b")?.children.map(\.id), ["b1"])

        XCTAssertTrue(tree.insertSibling(MindmapNode(id: "a1.5", title: "Between"), after: "a1"))
        XCTAssertEqual(tree.node(withID: "a")?.children.map(\.id), ["a1", "a1.5", "a2"])

        XCTAssertFalse(tree.insertChild(MindmapNode(title: "orphan"), under: "missing"))
        XCTAssertFalse(tree.insertSibling(MindmapNode(title: "orphan"), after: "root"))
    }

    func testUpdateAndRemove() {
        var tree = sampleTree()
        XCTAssertTrue(tree.update(id: "a2") { $0.title = "Renamed"; $0.note = "detail" })
        XCTAssertEqual(tree.node(withID: "a2")?.title, "Renamed")
        XCTAssertEqual(tree.node(withID: "a2")?.note, "detail")

        XCTAssertTrue(tree.removeNode(id: "a"))
        XCTAssertNil(tree.node(withID: "a"))
        XCTAssertNil(tree.node(withID: "a1"), "removing a node removes its subtree")
        XCTAssertEqual(tree.nodeCount, 2)
        XCTAssertFalse(tree.removeNode(id: "missing"))
    }

    func testDecodingToleratesMissingChildrenAndNote() throws {
        let json = #"{"version":1,"root":{"id":"root","title":"Topic","children":[{"id":"n1","title":"Leaf"}]}}"#
        let spec = try JSONDecoder().decode(MindmapSpec.self, from: Data(json.utf8))
        XCTAssertEqual(spec.root.children.first?.id, "n1")
        XCTAssertEqual(spec.root.children.first?.children, [])
        XCTAssertNil(spec.root.children.first?.note)
    }

    // MARK: - Layout

    func testLayoutPlacesChildrenRightOfParent() {
        let layout = MindmapLayout.layout(root: sampleTree(), collapsed: [])
        let root = layout.frames["root"]!
        let a = layout.frames["a"]!
        let a1 = layout.frames["a1"]!
        XCTAssertGreaterThan(a.minX, root.maxX)
        XCTAssertGreaterThan(a1.minX, a.maxX)
        XCTAssertEqual(layout.frames.count, 5)
        // Siblings must not overlap vertically.
        let a2 = layout.frames["a2"]!
        XCTAssertGreaterThanOrEqual(a2.minY, a1.maxY)
        // Every visible edge connects laid-out nodes.
        XCTAssertEqual(layout.edges.count, 4)
        XCTAssertTrue(layout.contentSize.width > 0 && layout.contentSize.height > 0)
    }

    func testLayoutCollapsedSubtreeHidesDescendants() {
        let layout = MindmapLayout.layout(root: sampleTree(), collapsed: ["a"])
        XCTAssertNotNil(layout.frames["a"])
        XCTAssertNil(layout.frames["a1"])
        XCTAssertNil(layout.frames["a2"])
        XCTAssertEqual(layout.edges.count, 2)

        let visible = MindmapLayout.visibleNodes(root: sampleTree(), collapsed: ["a"]).map(\.node.id)
        XCTAssertEqual(visible, ["root", "a", "b"])

        let expanded = MindmapLayout.layout(root: sampleTree(), collapsed: [])
        XCTAssertGreaterThan(expanded.contentSize.height, layout.contentSize.height)
    }
}
