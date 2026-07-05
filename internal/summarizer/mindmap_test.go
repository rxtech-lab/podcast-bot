package summarizer

import (
	"strings"
	"testing"
)

func TestParseMindmapSpecStripsFences(t *testing.T) {
	raw := "```json\n{\"version\":1,\"root\":{\"id\":\"root\",\"title\":\"Topic\",\"children\":[{\"id\":\"n1\",\"title\":\"Theme\"}]}}\n```"
	spec, err := parseMindmapSpec(raw)
	if err != nil {
		t.Fatalf("parseMindmapSpec: %v", err)
	}
	if spec.Root == nil || spec.Root.Title != "Topic" {
		t.Fatalf("unexpected root: %+v", spec.Root)
	}
	if len(spec.Root.Children) != 1 || spec.Root.Children[0].ID != "n1" {
		t.Fatalf("unexpected children: %+v", spec.Root.Children)
	}
}

func TestParseMindmapSpecRejectsGarbage(t *testing.T) {
	if _, err := parseMindmapSpec("not json at all"); err == nil {
		t.Fatal("expected error for non-JSON output")
	}
	if _, err := parseMindmapSpec(""); err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestNormalizeAssignsIDsAndPrunes(t *testing.T) {
	spec := &MindmapSpec{Root: &MindmapNode{
		Title: "  Root  ",
		Children: []*MindmapNode{
			{Title: "A", Children: []*MindmapNode{
				{Title: "A1", Children: []*MindmapNode{
					{Title: "A1a", Children: []*MindmapNode{
						{Title: "too deep"},
					}},
				}},
			}},
			{Title: ""},   // dropped: empty title
			nil,           // dropped: nil node
			{Title: "B"},  // duplicate id below
			{Title: "C", ID: "dup"},
			{Title: "D", ID: "dup"},
		},
	}}
	spec.normalize("fallback")
	if spec.Root.Title != "Root" {
		t.Fatalf("root title = %q", spec.Root.Title)
	}
	if spec.Root.ID == "" {
		t.Fatal("root id not assigned")
	}
	if len(spec.Root.Children) != 4 {
		t.Fatalf("children = %d, want 4", len(spec.Root.Children))
	}
	// Depth cap: the node at depth 4 must have its children removed.
	deep := spec.Root.Children[0].Children[0].Children[0]
	if len(deep.Children) != 0 {
		t.Fatalf("depth cap not applied, children = %+v", deep.Children)
	}
	if err := ValidateMindmapSpec(spec, false); err != nil {
		t.Fatalf("normalized spec should validate: %v", err)
	}
}

func TestNormalizeUsesFallbackTitle(t *testing.T) {
	spec := &MindmapSpec{}
	spec.normalize("My Podcast")
	if spec.Root == nil || spec.Root.Title != "My Podcast" {
		t.Fatalf("fallback title not applied: %+v", spec.Root)
	}
}

func TestValidateMindmapSpecUserLimits(t *testing.T) {
	long := strings.Repeat("x", 150)
	spec := &MindmapSpec{Version: 1, Root: &MindmapNode{ID: "root", Title: long}}
	if err := ValidateMindmapSpec(spec, false); err == nil {
		t.Fatal("generation limits should reject a 150-char title")
	}
	if err := ValidateMindmapSpec(spec, true); err != nil {
		t.Fatalf("user limits should accept a 150-char title: %v", err)
	}
}

func TestValidateMindmapSpecRejectsDuplicateIDs(t *testing.T) {
	spec := &MindmapSpec{Version: 1, Root: &MindmapNode{ID: "root", Title: "t", Children: []*MindmapNode{
		{ID: "a", Title: "x"},
		{ID: "a", Title: "y"},
	}}}
	if err := ValidateMindmapSpec(spec, true); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestValidateMindmapSpecRejectsEmpty(t *testing.T) {
	if err := ValidateMindmapSpec(nil, true); err == nil {
		t.Fatal("expected error for nil spec")
	}
	if err := ValidateMindmapSpec(&MindmapSpec{}, true); err == nil {
		t.Fatal("expected error for missing root")
	}
}
