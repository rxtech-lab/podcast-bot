import CoreGraphics
import Foundation

/// Pure right-branching horizontal tree layout: the root sits at the left
/// edge, children fan out to the right, siblings stack vertically. Collapsed
/// subtrees take only their node's own height. All frames are in an unscaled
/// content coordinate space; the canvas view applies zoom/pan on top.
enum MindmapLayout {
    static let nodeSize = CGSize(width: 180, height: 52)
    static let hGap: CGFloat = 64
    static let vGap: CGFloat = 16
    static let padding: CGFloat = 32

    struct Result {
        var frames: [String: CGRect] = [:]
        var edges: [Edge] = []
        var contentSize: CGSize = .zero
    }

    struct Edge: Hashable {
        var fromID: String
        var toID: String
        var from: CGPoint // parent's right-center
        var to: CGPoint   // child's left-center
    }

    /// Nodes rendered given the fold state, in depth-first order with their
    /// depth (root = 0) for per-depth styling.
    static func visibleNodes(root: MindmapNode, collapsed: Set<String>) -> [(node: MindmapNode, depth: Int)] {
        var out: [(MindmapNode, Int)] = []
        func walk(_ node: MindmapNode, depth: Int) {
            out.append((node, depth))
            guard !collapsed.contains(node.id) else { return }
            for child in node.children {
                walk(child, depth: depth + 1)
            }
        }
        walk(root, depth: 0)
        return out
    }

    static func layout(root: MindmapNode, collapsed: Set<String>) -> Result {
        var result = Result()

        func subtreeHeight(_ node: MindmapNode) -> CGFloat {
            if node.children.isEmpty || collapsed.contains(node.id) {
                return nodeSize.height
            }
            let childrenHeight = node.children.reduce(CGFloat.zero) { $0 + subtreeHeight($1) }
                + vGap * CGFloat(node.children.count - 1)
            return max(nodeSize.height, childrenHeight)
        }

        // Places node with its subtree occupying the vertical span starting at
        // `top`; the node itself centers on that span.
        func place(_ node: MindmapNode, x: CGFloat, top: CGFloat) {
            let height = subtreeHeight(node)
            let frame = CGRect(
                x: x,
                y: top + (height - nodeSize.height) / 2,
                width: nodeSize.width,
                height: nodeSize.height
            )
            result.frames[node.id] = frame
            guard !node.children.isEmpty, !collapsed.contains(node.id) else { return }
            let childX = x + nodeSize.width + hGap
            var childTop = top
            for child in node.children {
                place(child, x: childX, top: childTop)
                let childFrame = result.frames[child.id] ?? .zero
                result.edges.append(Edge(
                    fromID: node.id,
                    toID: child.id,
                    from: CGPoint(x: frame.maxX, y: frame.midY),
                    to: CGPoint(x: childFrame.minX, y: childFrame.midY)
                ))
                childTop += subtreeHeight(child) + vGap
            }
        }

        place(root, x: padding, top: padding)

        var maxX: CGFloat = 0
        var maxY: CGFloat = 0
        for frame in result.frames.values {
            maxX = max(maxX, frame.maxX)
            maxY = max(maxY, frame.maxY)
        }
        result.contentSize = CGSize(width: maxX + padding, height: maxY + padding)
        return result
    }
}
