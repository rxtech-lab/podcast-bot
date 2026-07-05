package server

import (
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/summarizer"
)

// Mindmap SVG rendering for exports (e.g. the Notion embed). Mirrors the iOS
// canvas layout: root on the left, children fanning right, siblings stacked
// vertically, cubic Bézier connectors. Geometry constants match
// iOS MindmapLayout so the exported picture looks like the in-app view.
const (
	mindmapSVGNodeWidth  = 180.0
	mindmapSVGNodeHeight = 52.0
	mindmapSVGHGap       = 64.0
	mindmapSVGVGap       = 16.0
	mindmapSVGPadding    = 32.0

	mindmapSVGTitleSize = 13.0
	mindmapSVGNoteSize  = 10.0
	// Usable text width inside a node (node width minus horizontal padding).
	mindmapSVGTextWidth = mindmapSVGNodeWidth - 20.0

	mindmapSVGAccent    = "#7D4FF5"
	mindmapSVGText      = "#1D1D1F"
	mindmapSVGNote      = "#6E6E73"
	mindmapSVGEdgeColor = "#C7C7CC"
)

type mindmapSVGNode struct {
	node  *summarizer.MindmapNode
	depth int
	x, y  float64 // top-left of the node rect
}

type mindmapSVGEdge struct {
	fromX, fromY float64 // parent right-center
	toX, toY     float64 // child left-center
}

// mindmapSVG renders the full tree (no fold state) to a standalone SVG
// document suitable for embedding as an image.
func mindmapSVG(spec *summarizer.MindmapSpec) []byte {
	if spec == nil || spec.Root == nil {
		return nil
	}

	var nodes []mindmapSVGNode
	var edges []mindmapSVGEdge

	var subtreeHeight func(n *summarizer.MindmapNode) float64
	subtreeHeight = func(n *summarizer.MindmapNode) float64 {
		if len(n.Children) == 0 {
			return mindmapSVGNodeHeight
		}
		total := 0.0
		for _, c := range n.Children {
			if c == nil {
				continue
			}
			total += subtreeHeight(c)
		}
		total += mindmapSVGVGap * float64(len(n.Children)-1)
		if total < mindmapSVGNodeHeight {
			return mindmapSVGNodeHeight
		}
		return total
	}

	var place func(n *summarizer.MindmapNode, depth int, x, top float64)
	place = func(n *summarizer.MindmapNode, depth int, x, top float64) {
		height := subtreeHeight(n)
		y := top + (height-mindmapSVGNodeHeight)/2
		nodes = append(nodes, mindmapSVGNode{node: n, depth: depth, x: x, y: y})
		childX := x + mindmapSVGNodeWidth + mindmapSVGHGap
		childTop := top
		for _, c := range n.Children {
			if c == nil {
				continue
			}
			childHeight := subtreeHeight(c)
			childY := childTop + (childHeight-mindmapSVGNodeHeight)/2
			edges = append(edges, mindmapSVGEdge{
				fromX: x + mindmapSVGNodeWidth,
				fromY: y + mindmapSVGNodeHeight/2,
				toX:   childX,
				toY:   childY + mindmapSVGNodeHeight/2,
			})
			place(c, depth+1, childX, childTop)
			childTop += childHeight + mindmapSVGVGap
		}
	}
	place(spec.Root, 0, mindmapSVGPadding, mindmapSVGPadding)

	width, height := 0.0, 0.0
	for _, n := range nodes {
		if right := n.x + mindmapSVGNodeWidth; right > width {
			width = right
		}
		if bottom := n.y + mindmapSVGNodeHeight; bottom > height {
			height = bottom
		}
	}
	width += mindmapSVGPadding
	height += mindmapSVGPadding

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="-apple-system, 'Helvetica Neue', 'PingFang SC', 'Noto Sans', Arial, sans-serif">`, width, height, width, height)
	fmt.Fprintf(&b, `<rect width="%.0f" height="%.0f" fill="#FFFFFF"/>`, width, height)

	for _, e := range edges {
		control := mindmapSVGHGap / 2
		fmt.Fprintf(&b, `<path d="M %.1f %.1f C %.1f %.1f, %.1f %.1f, %.1f %.1f" fill="none" stroke="%s" stroke-width="1.5"/>`,
			e.fromX, e.fromY,
			e.fromX+control, e.fromY,
			e.toX-control, e.toY,
			e.toX, e.toY,
			mindmapSVGEdgeColor)
	}

	for _, n := range nodes {
		fill, stroke, titleColor, noteColor := mindmapSVGNodeColors(n.depth)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.0f" height="%.0f" rx="12" fill="%s" stroke="%s" stroke-width="1"/>`,
			n.x, n.y, mindmapSVGNodeWidth, mindmapSVGNodeHeight, fill, stroke)

		title := mindmapSVGTruncate(n.node.Title, mindmapSVGTitleSize)
		note := strings.TrimSpace(n.node.Note)
		titleWeight := "normal"
		if n.depth == 0 {
			titleWeight = "bold"
		}
		if note == "" {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="%.0f" font-weight="%s" fill="%s">%s</text>`,
				n.x+10, n.y+mindmapSVGNodeHeight/2+mindmapSVGTitleSize/2-1, mindmapSVGTitleSize, titleWeight, titleColor, xmlEscape(title))
		} else {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="%.0f" font-weight="%s" fill="%s">%s</text>`,
				n.x+10, n.y+21, mindmapSVGTitleSize, titleWeight, titleColor, xmlEscape(title))
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="%.0f" fill="%s">%s</text>`,
				n.x+10, n.y+37, mindmapSVGNoteSize, noteColor, xmlEscape(mindmapSVGTruncate(note, mindmapSVGNoteSize)))
		}
	}

	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func mindmapSVGNodeColors(depth int) (fill, stroke, title, note string) {
	if depth == 0 {
		return mindmapSVGAccent, mindmapSVGAccent, "#FFFFFF", "#E8E2FF"
	}
	// Progressively lighter purple washes for deeper levels, matching the
	// iOS opacity ramp (0.16 → 0.04 over solid white).
	fills := []string{"#E9E1FD", "#F0EBFE", "#F7F4FE", "#FBFAFF"}
	idx := depth - 1
	if idx >= len(fills) {
		idx = len(fills) - 1
	}
	return fills[idx], "#DCD5EE", mindmapSVGText, mindmapSVGNote
}

// mindmapSVGTruncate fits text to the node's usable width using a rough
// per-rune width model (CJK ≈ 1em, Latin ≈ 0.55em), appending an ellipsis
// when it overflows. SVG has no automatic wrapping/clipping.
func mindmapSVGTruncate(text string, fontSize float64) string {
	text = strings.TrimSpace(text)
	budget := mindmapSVGTextWidth
	used := 0.0
	runes := []rune(text)
	for i, r := range runes {
		used += mindmapSVGRuneWidth(r, fontSize)
		if used > budget {
			return strings.TrimSpace(string(runes[:i])) + "…"
		}
	}
	return text
}

func mindmapSVGRuneWidth(r rune, fontSize float64) float64 {
	if r < 0x2E80 { // roughly: Latin, digits, punctuation
		return fontSize * 0.55
	}
	return fontSize // CJK and other full-width scripts
}

func xmlEscape(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(text)
}
