import SwiftUI
import UniformTypeIdentifiers

/// Client-side SVG export of the mindmap tree. Reuses MindmapLayout for
/// geometry and mirrors the server's mindmap_svg.go renderer (colors, fonts,
/// truncation) so an export looks the same no matter where it is produced.
/// The full tree is always exported, ignoring the on-screen fold state.
enum MindmapSVGExporter {
    private static let titleSize: CGFloat = 13
    private static let noteSize: CGFloat = 10
    /// Usable text width inside a node (node width minus horizontal padding).
    private static let textWidth = MindmapLayout.nodeSize.width - 20

    private static let accent = "#7D4FF5"
    private static let textColor = "#1D1D1F"
    private static let noteColor = "#6E6E73"
    private static let edgeColor = "#C7C7CC"

    static func svg(root: MindmapNode) -> String {
        let layout = MindmapLayout.layout(root: root, collapsed: [])
        let width = layout.contentSize.width
        let height = layout.contentSize.height

        var out = String(
            format: #"<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="-apple-system, 'Helvetica Neue', 'PingFang SC', 'Noto Sans', Arial, sans-serif">"#,
            width, height, width, height
        )
        out += String(format: ##"<rect width="%.0f" height="%.0f" fill="#FFFFFF"/>"##, width, height)

        let control = MindmapLayout.hGap / 2
        for edge in layout.edges {
            out += String(
                format: #"<path d="M %.1f %.1f C %.1f %.1f, %.1f %.1f, %.1f %.1f" fill="none" stroke="%@" stroke-width="1.5"/>"#,
                edge.from.x, edge.from.y,
                edge.from.x + control, edge.from.y,
                edge.to.x - control, edge.to.y,
                edge.to.x, edge.to.y,
                edgeColor
            )
        }

        for entry in MindmapLayout.visibleNodes(root: root, collapsed: []) {
            guard let frame = layout.frames[entry.node.id] else { continue }
            let colors = nodeColors(depth: entry.depth)
            out += String(
                format: #"<rect x="%.1f" y="%.1f" width="%.0f" height="%.0f" rx="12" fill="%@" stroke="%@" stroke-width="1"/>"#,
                frame.minX, frame.minY, frame.width, frame.height, colors.fill, colors.stroke
            )

            let title = truncate(entry.node.title, fontSize: titleSize)
            let note = (entry.node.note ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            let titleWeight = entry.depth == 0 ? "bold" : "normal"
            if note.isEmpty {
                out += String(
                    format: #"<text x="%.1f" y="%.1f" font-size="%.0f" font-weight="%@" fill="%@">%@</text>"#,
                    frame.minX + 10, frame.midY + titleSize / 2 - 1, titleSize, titleWeight, colors.title, xmlEscape(title)
                )
            } else {
                out += String(
                    format: #"<text x="%.1f" y="%.1f" font-size="%.0f" font-weight="%@" fill="%@">%@</text>"#,
                    frame.minX + 10, frame.minY + 21, titleSize, titleWeight, colors.title, xmlEscape(title)
                )
                out += String(
                    format: #"<text x="%.1f" y="%.1f" font-size="%.0f" fill="%@">%@</text>"#,
                    frame.minX + 10, frame.minY + 37, noteSize, colors.note, xmlEscape(truncate(note, fontSize: noteSize))
                )
            }
        }

        out += "</svg>"
        return out
    }

    private static func nodeColors(depth: Int) -> (fill: String, stroke: String, title: String, note: String) {
        if depth == 0 {
            return (accent, accent, "#FFFFFF", "#E8E2FF")
        }
        // Progressively lighter purple washes for deeper levels, matching the
        // in-app opacity ramp (0.16 → 0.04 over solid white).
        let fills = ["#E9E1FD", "#F0EBFE", "#F7F4FE", "#FBFAFF"]
        let index = min(depth - 1, fills.count - 1)
        return (fills[index], "#DCD5EE", textColor, noteColor)
    }

    /// Fits text to the node's usable width using a rough per-rune width model
    /// (CJK ≈ 1em, Latin ≈ 0.55em), appending an ellipsis when it overflows.
    /// SVG has no automatic wrapping/clipping.
    private static func truncate(_ text: String, fontSize: CGFloat) -> String {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        var used: CGFloat = 0
        for (offset, scalar) in text.unicodeScalars.enumerated() {
            used += scalar.value < 0x2E80 ? fontSize * 0.55 : fontSize
            if used > textWidth {
                let runes = Array(text.unicodeScalars)
                var truncated = String(String.UnicodeScalarView(runes[..<offset]))
                truncated = truncated.trimmingCharacters(in: .whitespaces)
                return truncated + "…"
            }
        }
        return text
    }

    private static func xmlEscape(_ text: String) -> String {
        text
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
            .replacingOccurrences(of: "\"", with: "&quot;")
            .replacingOccurrences(of: "'", with: "&apos;")
    }
}

/// Wraps generated SVG markup for `.fileExporter` (Save to Files).
struct MindmapSVGDocument: FileDocument {
    static let readableContentTypes: [UTType] = [.svg]

    var data: Data

    init(svg: String) {
        data = Data(svg.utf8)
    }

    init(configuration: ReadConfiguration) throws {
        data = configuration.file.regularFileContents ?? Data()
    }

    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: data)
    }
}

/// Static full-tree rendering of the mindmap for rasterizing with
/// ImageRenderer (the photo library cannot store SVGs, so Save to Camera Roll
/// gets a PNG of the same picture). Mirrors MindmapCanvasView/MindmapNodeView
/// minus all interactivity and fold state; render with a forced light color
/// scheme so the image matches the SVG export.
struct MindmapExportRenderView: View {
    let root: MindmapNode

    var body: some View {
        let layout = MindmapLayout.layout(root: root, collapsed: [])
        ZStack(alignment: .topLeading) {
            Color.white

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

            ForEach(MindmapLayout.visibleNodes(root: root, collapsed: []), id: \.node.id) { entry in
                let frame = layout.frames[entry.node.id] ?? .zero
                card(entry.node, depth: entry.depth)
                    .frame(width: frame.width, height: frame.height)
                    .position(x: frame.midX, y: frame.midY)
            }
        }
        .frame(width: layout.contentSize.width, height: layout.contentSize.height, alignment: .topLeading)
    }

    private func card(_ node: MindmapNode, depth: Int) -> some View {
        let isRoot = depth == 0
        let fill = isRoot
            ? Theme.accent
            : Theme.accent.opacity(max(0.16 - Double(depth - 1) * 0.05, 0.04))
        return VStack(alignment: .leading, spacing: 2) {
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
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(fill, in: RoundedRectangle(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .strokeBorder(Theme.divider.opacity(0.6), lineWidth: 1)
        }
    }
}
