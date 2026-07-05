package server

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/summarizer"
)

func TestMindmapSVGRendersTree(t *testing.T) {
	spec := &summarizer.MindmapSpec{Version: 1, Root: &summarizer.MindmapNode{
		ID:    "root",
		Title: "Reform <b> & \"c\"",
		Children: []*summarizer.MindmapNode{
			{ID: "a", Title: "Theme A", Note: "detail line", Children: []*summarizer.MindmapNode{
				{ID: "a1", Title: "Point A1"},
			}},
			{ID: "b", Title: "Theme B"},
		},
	}}
	svg := string(mindmapSVG(spec))
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("not a standalone svg document: %.80s…", svg)
	}
	// One rect per node plus the background.
	if got := strings.Count(svg, "<rect "); got != 5 {
		t.Fatalf("rect count = %d, want 5 (background + 4 nodes)", got)
	}
	// One connector per parent→child relationship.
	if got := strings.Count(svg, "<path "); got != 3 {
		t.Fatalf("edge count = %d, want 3", got)
	}
	if !strings.Contains(svg, "Reform &lt;b&gt; &amp; &quot;c&quot;") {
		t.Fatalf("title not XML-escaped: %s", svg)
	}
	if !strings.Contains(svg, "detail line") {
		t.Fatal("note text missing from svg")
	}
	if strings.Contains(svg, "<b>") {
		t.Fatal("unescaped markup leaked into svg")
	}
}

func TestMindmapSVGTruncatesLongTitles(t *testing.T) {
	long := strings.Repeat("哲学", 40)
	spec := &summarizer.MindmapSpec{Version: 1, Root: &summarizer.MindmapNode{ID: "root", Title: long}}
	svg := string(mindmapSVG(spec))
	if strings.Contains(svg, long) {
		t.Fatal("long CJK title was not truncated")
	}
	if !strings.Contains(svg, "…") {
		t.Fatal("truncated title missing ellipsis")
	}
}

func TestMindmapSVGEmptySpec(t *testing.T) {
	if got := mindmapSVG(nil); got != nil {
		t.Fatalf("nil spec svg = %q, want nil", got)
	}
	if got := mindmapSVG(&summarizer.MindmapSpec{}); got != nil {
		t.Fatalf("rootless spec svg = %q, want nil", got)
	}
}

func TestSummaryMarkdownWithMindmapLink(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := t.Context()

	d := createReadyMindmapDiscussion(t, store, "discussion")
	full, err := store.Get(ctx, "anonymous", d.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// No mindmap yet: markdown unchanged.
	if got := srv.summaryMarkdownWithMindmapLink(ctx, full, "body"); got != "body" {
		t.Fatalf("markdown = %q, want unchanged", got)
	}

	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"t"}}`, "m", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	want := "debatepod://discussion/" + d.ID + "/sheet/mindmap"
	got := srv.summaryMarkdownWithMindmapLink(ctx, full, "body")
	if !strings.Contains(got, want) || !strings.HasPrefix(got, "body\n\n") {
		t.Fatalf("markdown = %q, want mindmap deep link appended", got)
	}
	// Idempotent on re-read.
	if again := srv.summaryMarkdownWithMindmapLink(ctx, full, got); again != got {
		t.Fatalf("second injection changed markdown: %q", again)
	}

	// Non-discussion types never get the link.
	debate := createReadyMindmapDiscussion(t, store, "debate")
	fullDebate, err := store.Get(ctx, "anonymous", debate.ID)
	if err != nil {
		t.Fatalf("Get debate: %v", err)
	}
	if got := srv.summaryMarkdownWithMindmapLink(ctx, fullDebate, "body"); got != "body" {
		t.Fatalf("debate markdown = %q, want unchanged", got)
	}
}
