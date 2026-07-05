import SwiftUI

/// The interactive mindmap surface: draws the laid-out tree (curved edges in a
/// single Canvas + positioned node views) and layers pinch-zoom, pan, and
/// double-tap-to-fit on top. Fold/unfold and selection are delegated to the
/// view model; all geometry lives in an unscaled content space transformed as
/// a whole.
struct MindmapCanvasView: View {
    @Bindable var model: MindmapViewModel

    @State private var steadyScale: CGFloat = 1
    @State private var steadyOffset: CGSize = .zero
    @GestureState private var gestureScale: CGFloat = 1
    @GestureState private var gestureOffset: CGSize = .zero
    @State private var hasFittedInitially = false

    private static let minScale: CGFloat = 0.3
    private static let maxScale: CGFloat = 2.5

    private var scale: CGFloat { steadyScale * gestureScale }
    private var offset: CGSize {
        CGSize(width: steadyOffset.width + gestureOffset.width,
               height: steadyOffset.height + gestureOffset.height)
    }

    var body: some View {
        GeometryReader { proxy in
            if let root = model.root {
                let layout = MindmapLayout.layout(root: root, collapsed: model.collapsedIDs)
                canvas(root: root, layout: layout)
                    .contentShape(Rectangle())
                    .gesture(panGesture.simultaneously(with: zoomGesture(viewport: proxy.size, contentSize: layout.contentSize)))
                    .onTapGesture(count: 2) {
                        withAnimation(.snappy) {
                            fit(contentSize: layout.contentSize, in: proxy.size)
                        }
                    }
                    .onTapGesture {
                        model.selectedID = nil
                    }
                    .onAppear {
                        guard !hasFittedInitially else { return }
                        hasFittedInitially = true
                        fit(contentSize: layout.contentSize, in: proxy.size)
                    }
            }
        }
        .clipped()
    }

    private func canvas(root: MindmapNode, layout: MindmapLayout.Result) -> some View {
        ZStack(alignment: .topLeading) {
            Canvas { context, _ in
                for edge in layout.edges {
                    var path = Path()
                    path.move(to: edge.from)
                    let controlOffset = MindmapLayout.hGap / 2
                    path.addCurve(
                        to: edge.to,
                        control1: CGPoint(x: edge.from.x + controlOffset, y: edge.from.y),
                        control2: CGPoint(x: edge.to.x - controlOffset, y: edge.to.y)
                    )
                    context.stroke(path, with: .color(Theme.divider), lineWidth: 1.5)
                }
            }
            .frame(width: layout.contentSize.width, height: layout.contentSize.height)

            ForEach(MindmapLayout.visibleNodes(root: root, collapsed: model.collapsedIDs), id: \.node.id) { entry in
                let frame = layout.frames[entry.node.id] ?? .zero
                MindmapNodeView(
                    node: entry.node,
                    depth: entry.depth,
                    isSelected: model.selectedID == entry.node.id,
                    isCollapsed: model.collapsedIDs.contains(entry.node.id),
                    onTap: {
                        model.selectedID = model.selectedID == entry.node.id ? nil : entry.node.id
                    },
                    onToggleCollapse: {
                        withAnimation(.snappy) {
                            model.toggleCollapsed(entry.node.id)
                        }
                    }
                )
                .frame(width: frame.width, height: frame.height)
                .position(x: frame.midX, y: frame.midY)
            }
        }
        .frame(width: layout.contentSize.width, height: layout.contentSize.height, alignment: .topLeading)
        .scaleEffect(scale, anchor: .topLeading)
        .offset(offset)
        .animation(.snappy, value: model.collapsedIDs)
        .animation(.snappy, value: model.root)
    }

    private var panGesture: some Gesture {
        DragGesture(minimumDistance: 1)
            .updating($gestureOffset) { value, state, _ in
                state = value.translation
            }
            .onEnded { value in
                steadyOffset.width += value.translation.width
                steadyOffset.height += value.translation.height
            }
    }

    private func zoomGesture(viewport: CGSize, contentSize: CGSize) -> some Gesture {
        MagnifyGesture()
            .updating($gestureScale) { value, state, _ in
                state = value.magnification
            }
            .onEnded { value in
                let proposed = steadyScale * value.magnification
                steadyScale = min(max(proposed, Self.minScale), Self.maxScale)
            }
    }

    /// Scales the whole tree to fit the viewport (never above 1:1) and centers it.
    private func fit(contentSize: CGSize, in viewport: CGSize) {
        guard contentSize.width > 0, contentSize.height > 0,
              viewport.width > 0, viewport.height > 0 else { return }
        let fitScale = min(viewport.width / contentSize.width,
                           viewport.height / contentSize.height,
                           1)
        steadyScale = max(fitScale, Self.minScale)
        steadyOffset = CGSize(
            width: (viewport.width - contentSize.width * steadyScale) / 2,
            height: (viewport.height - contentSize.height * steadyScale) / 2
        )
    }
}

/// A single mindmap node: a rounded card with a 2-line title, depth-based
/// tinting, a selection ring, and — when the node has children — a trailing
/// fold/unfold badge showing the hidden-descendant count when collapsed.
struct MindmapNodeView: View {
    let node: MindmapNode
    let depth: Int
    let isSelected: Bool
    let isCollapsed: Bool
    let onTap: () -> Void
    let onToggleCollapse: () -> Void

    private var isRoot: Bool { depth == 0 }

    private var fillColor: Color {
        if isRoot { return Theme.accent }
        let opacity = max(0.16 - Double(depth - 1) * 0.05, 0.04)
        return Theme.accent.opacity(opacity)
    }

    var body: some View {
        HStack(spacing: 6) {
            VStack(alignment: .leading, spacing: 2) {
                Text(node.title)
                    .font(isRoot ? .footnote.bold() : .footnote)
                    .foregroundStyle(isRoot ? Color.white : Color.primary)
                    .lineLimit(node.note?.isEmpty == false ? 1 : 2)
                    .multilineTextAlignment(.leading)
                if let note = node.note, !note.isEmpty {
                    Text(note)
                        .font(.caption2)
                        .foregroundStyle(isRoot ? Color.white.opacity(0.8) : Theme.secondaryText)
                        .lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            if !node.children.isEmpty {
                Button(action: onToggleCollapse) {
                    Group {
                        if isCollapsed {
                            Text("\(node.nodeCount - 1)")
                                .font(.caption2.bold())
                                .monospacedDigit()
                        } else {
                            Image(systemName: "chevron.left")
                                .font(.caption2.bold())
                        }
                    }
                    .foregroundStyle(isRoot ? Theme.accent : Color.white)
                    .frame(width: 20, height: 20)
                    .background(isRoot ? Color.white : Theme.accent, in: Circle())
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(fillColor, in: RoundedRectangle(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .strokeBorder(isSelected ? Theme.accent : Theme.divider.opacity(0.6),
                              lineWidth: isSelected ? 2.5 : 1)
        }
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onTapGesture(perform: onTap)
        .contextMenu {
            if let note = node.note, !note.isEmpty {
                Section(node.title) {
                    Text(note)
                }
            }
        }
    }
}
