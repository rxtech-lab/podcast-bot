import Foundation

/// One node of the discussion mindmap tree. A value type so undo snapshots and
/// SwiftUI diffing are cheap copies.
struct MindmapNode: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var title: String
    var note: String?
    var children: [MindmapNode] = []

    init(id: String = UUID().uuidString, title: String, note: String? = nil, children: [MindmapNode] = []) {
        self.id = id
        self.title = title
        self.note = note
        self.children = children
    }

    enum CodingKeys: String, CodingKey {
        case id, title, note, children
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        title = try container.decode(String.self, forKey: .title)
        note = try container.decodeIfPresent(String.self, forKey: .note)
        children = try container.decodeIfPresent([MindmapNode].self, forKey: .children) ?? []
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(title, forKey: .title)
        if let note, !note.isEmpty {
            try container.encode(note, forKey: .note)
        }
        try container.encode(children, forKey: .children)
    }
}

/// The mindmap tree as stored/served by the backend.
struct MindmapSpec: Codable, Hashable, Sendable {
    var version: Int
    var root: MindmapNode
}

/// Full mindmap payload returned by the mindmap content endpoint.
struct MindmapDocument: Codable, Hashable, Sendable {
    var docType: String?
    var status: SummaryStatus?
    var mindmap: MindmapSpec?
    var generatedAt: String?

    enum CodingKeys: String, CodingKey {
        case docType = "doc_type"
        case status
        case mindmap
        case generatedAt = "generated_at"
    }
}

/// PUT body for persisting an edited mindmap tree.
struct MindmapSaveRequest: Codable, Sendable {
    var mindmap: MindmapSpec
}

// MARK: - Recursive tree operations

extension MindmapNode {
    var nodeCount: Int {
        1 + children.reduce(0) { $0 + $1.nodeCount }
    }

    func node(withID id: String) -> MindmapNode? {
        if self.id == id { return self }
        for child in children {
            if let found = child.node(withID: id) { return found }
        }
        return nil
    }

    /// The id of the node whose `children` contains `id`, or nil for the root
    /// (and for unknown ids).
    func parentID(of id: String) -> String? {
        for child in children {
            if child.id == id { return self.id }
            if let found = child.parentID(of: id) { return found }
        }
        return nil
    }

    /// Applies `transform` to the node with the given id. Returns false when
    /// the id is not in this subtree.
    @discardableResult
    mutating func update(id: String, _ transform: (inout MindmapNode) -> Void) -> Bool {
        if self.id == id {
            transform(&self)
            return true
        }
        for index in children.indices {
            if children[index].update(id: id, transform) { return true }
        }
        return false
    }

    /// Appends `child` to the children of the node with id `parentID`.
    @discardableResult
    mutating func insertChild(_ child: MindmapNode, under parentID: String) -> Bool {
        update(id: parentID) { $0.children.append(child) }
    }

    /// Inserts `node` directly after the node with id `siblingID` in its
    /// parent's children. Returns false for the root or unknown ids.
    @discardableResult
    mutating func insertSibling(_ node: MindmapNode, after siblingID: String) -> Bool {
        if let index = children.firstIndex(where: { $0.id == siblingID }) {
            children.insert(node, at: index + 1)
            return true
        }
        for index in children.indices {
            if children[index].insertSibling(node, after: siblingID) { return true }
        }
        return false
    }

    /// Removes the node with the given id (and its whole subtree). The caller
    /// is responsible for never passing the root's id.
    @discardableResult
    mutating func removeNode(id: String) -> Bool {
        if let index = children.firstIndex(where: { $0.id == id }) {
            children.remove(at: index)
            return true
        }
        for index in children.indices {
            if children[index].removeNode(id: id) { return true }
        }
        return false
    }
}
