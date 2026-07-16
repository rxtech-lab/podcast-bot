import Foundation
import Observation

/// Drives the mindmap editor sheet: loads the tree, applies edits with undo
/// snapshots, and persists changes with a debounced whole-tree PUT.
@MainActor
@Observable
final class MindmapViewModel {
    enum Phase: Equatable {
        case loading
        case failed(String)
        case ready
    }

    enum SaveState: Equatable {
        case idle
        case saving
        case saved
        case failed(String)
    }

    let discussionID: String
    let isEditable: Bool
    let language: String?

    private(set) var phase: Phase = .loading
    var root: MindmapNode?
    var selectedID: String?
    var collapsedIDs: Set<String> = []
    private(set) var saveState: SaveState = .idle
    private(set) var isDirty = false
    private(set) var undoStack: [MindmapNode] = []

    var canUndo: Bool { !undoStack.isEmpty }

    var selectedNode: MindmapNode? {
        guard let selectedID else { return nil }
        return root?.node(withID: selectedID)
    }

    var selectedIsRoot: Bool {
        selectedID != nil && selectedID == root?.id
    }

    private let api: APIClient
    private var saveTask: Task<Void, Never>?
    private static let undoLimit = 20
    private static let autosaveDelay: Duration = .seconds(2)

    init(discussionID: String, isEditable: Bool, language: String? = nil, api: APIClient) {
        self.discussionID = discussionID
        self.isEditable = isEditable
        self.language = language
        self.api = api
    }

    func load() async {
        phase = .loading
        do {
            let document = try await api.mindmap(id: discussionID, language: language)
            guard let spec = document.mindmap else {
                phase = .failed(String(localized: "The mindmap is not ready yet."))
                return
            }
            root = spec.root
            phase = .ready
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            phase = .failed((error as? APIError)?.errorDescription ?? error.localizedDescription)
        }
    }

    // MARK: - Fold state

    func toggleCollapsed(_ id: String) {
        if collapsedIDs.contains(id) {
            collapsedIDs.remove(id)
        } else {
            collapsedIDs.insert(id)
        }
    }

    // MARK: - Edits

    /// Adds a child under the given node and returns the new node's id so the
    /// view can immediately open the rename prompt for it.
    @discardableResult
    func addChild(under parentID: String) -> String? {
        guard isEditable, var tree = root else { return nil }
        let child = MindmapNode(title: String(localized: "New idea"))
        snapshotForUndo()
        guard tree.insertChild(child, under: parentID) else {
            undoStack.removeLast()
            return nil
        }
        root = tree
        collapsedIDs.remove(parentID)
        selectedID = child.id
        markEdited()
        return child.id
    }

    @discardableResult
    func addSibling(of siblingID: String) -> String? {
        guard isEditable, var tree = root, siblingID != tree.id else { return nil }
        let node = MindmapNode(title: String(localized: "New idea"))
        snapshotForUndo()
        guard tree.insertSibling(node, after: siblingID) else {
            undoStack.removeLast()
            return nil
        }
        root = tree
        selectedID = node.id
        markEdited()
        return node.id
    }

    func rename(id: String, title: String, note: String) {
        guard isEditable, var tree = root else { return }
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedTitle.isEmpty else { return }
        let trimmedNote = note.trimmingCharacters(in: .whitespacesAndNewlines)
        snapshotForUndo()
        guard tree.update(id: id, {
            $0.title = trimmedTitle
            $0.note = trimmedNote.isEmpty ? nil : trimmedNote
        }) else {
            undoStack.removeLast()
            return
        }
        root = tree
        markEdited()
    }

    func delete(id: String) {
        guard isEditable, var tree = root, id != tree.id else { return }
        snapshotForUndo()
        guard tree.removeNode(id: id) else {
            undoStack.removeLast()
            return
        }
        root = tree
        if selectedID == id { selectedID = nil }
        markEdited()
    }

    func undo() {
        guard isEditable, let previous = undoStack.popLast() else { return }
        root = previous
        if let selectedID, previous.node(withID: selectedID) == nil {
            self.selectedID = nil
        }
        markEdited()
    }

    // MARK: - Persistence

    /// Flushes any pending edit immediately; called from the Done button so a
    /// dismissal never loses the debounce window's changes.
    func saveNow() async {
        saveTask?.cancel()
        saveTask = nil
        guard isDirty else { return }
        await save()
    }

    private func snapshotForUndo() {
        guard let root else { return }
        undoStack.append(root)
        if undoStack.count > Self.undoLimit {
            undoStack.removeFirst()
        }
    }

    private func markEdited() {
        isDirty = true
        scheduleAutosave()
    }

    private func scheduleAutosave() {
        saveTask?.cancel()
        saveTask = Task { [weak self] in
            try? await Task.sleep(for: Self.autosaveDelay)
            guard !Task.isCancelled else { return }
            await self?.save()
        }
    }

    private func save() async {
        guard let root else { return }
        saveState = .saving
        do {
            _ = try await api.saveMindmap(id: discussionID, spec: MindmapSpec(version: 1, root: root))
            isDirty = false
            saveState = .saved
        } catch {
            guard !APIClient.isCancellation(error) else { return }
            saveState = .failed((error as? APIError)?.errorDescription ?? error.localizedDescription)
        }
    }
}
