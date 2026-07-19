package planner

import (
	"strings"
	"testing"
)

const splitTestSource = `--- book.pdf ---

Front matter: dedication and copyright.

# Foreword

A word before the story begins.

# The Road North

The journey started in rain. Captain Reyes checked the maps twice.

## A Cold Morning

Snow covered the pass.

# The Clinic

Doctor Mira opened the door and said, "you took your time."

# Epilogue

They went home.`

func TestAudioBookSourceMarkersCatalogsHeadings(t *testing.T) {
	markers := audioBookSourceMarkers(splitTestSource)
	if len(markers) != 5 {
		t.Fatalf("markers = %d, want 5 (got %+v)", len(markers), markers)
	}
	wantTexts := []string{"# Foreword", "# The Road North", "## A Cold Morning", "# The Clinic", "# Epilogue"}
	for i, m := range markers {
		if m.Index != i+1 {
			t.Fatalf("marker %d has index %d", i, m.Index)
		}
		if m.Text != wantTexts[i] {
			t.Fatalf("marker %d text = %q, want %q", i, m.Text, wantTexts[i])
		}
		if !strings.HasPrefix(splitTestSource[m.Offset:], strings.TrimSpace(m.Text)) {
			t.Fatalf("marker %d offset %d does not point at %q", i, m.Offset, m.Text)
		}
	}
	// Determinism: the digest and the splitter must derive identical catalogs.
	again := audioBookSourceMarkers(splitTestSource)
	for i := range markers {
		if markers[i] != again[i] {
			t.Fatalf("catalog not deterministic at %d: %+v vs %+v", i, markers[i], again[i])
		}
	}
}

func TestAudioBookSourceMarkersFallsBackToParagraphAnchors(t *testing.T) {
	source := strings.Repeat("A paragraph of plain prose without any headings at all.\n\n", 30)
	markers := audioBookSourceMarkers(source)
	if len(markers) == 0 {
		t.Fatal("expected paragraph anchors for a headingless source")
	}
	for i, m := range markers {
		if m.Index != i+1 {
			t.Fatalf("anchor %d has index %d", i, m.Index)
		}
	}
}

func TestSplitAudioBookSourceByIndex(t *testing.T) {
	markers := audioBookSourceMarkers(splitTestSource)
	chapters := []audioBookDraftChapter{
		{Title: "Foreword", Summary: "s", StartIndex: 1},
		{Title: "Road", Summary: "s", StartIndex: 2},
		{Title: "Clinic", Summary: "s", StartIndex: 4},
		{Title: "Epilogue", Summary: "s", StartIndex: 5},
	}
	slices, err := splitAudioBookSource(splitTestSource, markers, chapters)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(slices) != 4 {
		t.Fatalf("slices = %d, want 4", len(slices))
	}
	// Chapter 1 clamps to the top of the document: front matter included.
	if slices[0].Start != 0 || !strings.Contains(slices[0].Content, "Front matter") {
		t.Fatalf("chapter 1 should start at 0 with front matter, got start=%d content=%q", slices[0].Start, slices[0].Content[:40])
	}
	// Middle chapter spans up to the next marker (includes its subsections).
	if !strings.Contains(slices[1].Content, "A Cold Morning") || strings.Contains(slices[1].Content, "Doctor Mira") {
		t.Fatalf("chapter 2 slice wrong: %q", slices[1].Content)
	}
	// Last chapter runs to EOF.
	if !strings.HasSuffix(strings.TrimSpace(slices[3].Content), "They went home.") {
		t.Fatalf("last chapter should run to EOF: %q", slices[3].Content)
	}
}

func TestSplitAudioBookSourceByMarkerString(t *testing.T) {
	markers := audioBookSourceMarkers(splitTestSource)
	chapters := []audioBookDraftChapter{
		{Title: "Start", Summary: "s"}, // chapter 1 may omit markers
		{Title: "Clinic", Summary: "s", StartMarker: "# The Clinic"},
	}
	slices, err := splitAudioBookSource(splitTestSource, markers, chapters)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if !strings.HasPrefix(slices[1].Content, "# The Clinic") {
		t.Fatalf("chapter 2 should start at the clinic heading: %q", slices[1].Content[:40])
	}
}

func TestSplitAudioBookSourceFuzzyMarker(t *testing.T) {
	markers := audioBookSourceMarkers(splitTestSource)
	chapters := []audioBookDraftChapter{
		{Title: "Start", Summary: "s"},
		{Title: "Clinic", Summary: "s", StartMarker: "the   clinic"}, // case + whitespace normalized, unique line match
	}
	slices, err := splitAudioBookSource(splitTestSource, markers, chapters)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if !strings.HasPrefix(slices[1].Content, "# The Clinic") {
		t.Fatalf("fuzzy marker should resolve to the clinic heading: %q", slices[1].Content[:40])
	}
}

func TestSplitAudioBookSourceErrors(t *testing.T) {
	markers := audioBookSourceMarkers(splitTestSource)
	cases := []struct {
		name     string
		chapters []audioBookDraftChapter
		wantMsg  string
	}{
		{
			name: "non-monotonic",
			chapters: []audioBookDraftChapter{
				{Title: "A", Summary: "s", StartIndex: 4},
				{Title: "B", Summary: "s", StartIndex: 2},
			},
			wantMsg: "strictly increasing",
		},
		{
			name: "index out of range",
			chapters: []audioBookDraftChapter{
				{Title: "A", Summary: "s", StartIndex: 1},
				{Title: "B", Summary: "s", StartIndex: 99},
			},
			wantMsg: "outside the Source markers catalog",
		},
		{
			name: "marker not found",
			chapters: []audioBookDraftChapter{
				{Title: "A", Summary: "s", StartIndex: 1},
				{Title: "B", Summary: "s", StartMarker: "no such line anywhere"},
			},
			wantMsg: "was not found in the source",
		},
		{
			name: "missing marker after first",
			chapters: []audioBookDraftChapter{
				{Title: "A", Summary: "s", StartIndex: 1},
				{Title: "B", Summary: "s"},
			},
			wantMsg: "neither start_index nor start_marker",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := splitAudioBookSource(splitTestSource, markers, tc.chapters)
			if err == nil {
				t.Fatal("expected error")
			}
			if _, ok := err.(*audioBookSplitError); !ok {
				t.Fatalf("error should be a model-facing audioBookSplitError, got %T", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestAudioBookSourceFromAttachmentsSkipsImages(t *testing.T) {
	atts := []Attachment{
		{Filename: "cover.png", MIMEType: "image/png", URL: "https://x/cover.png"},
		{Filename: "book.pdf", Markdown: "# Chapter\n\nText."},
		{Filename: "empty.pdf", Markdown: "   "},
	}
	source := AudioBookSourceFromAttachments(atts)
	if !strings.Contains(source, "--- book.pdf ---") || !strings.Contains(source, "# Chapter") {
		t.Fatalf("source missing document content: %q", source)
	}
	if strings.Contains(source, "cover.png") || strings.Contains(source, "empty.pdf") {
		t.Fatalf("source should skip images and empty documents: %q", source)
	}
}

func TestAudioBookSourceDigestRendersCatalog(t *testing.T) {
	digest := audioBookSourceDigest(splitTestSource)
	for _, want := range []string{"Converted length:", "Source markers", "[1] # Foreword", "[5] # Epilogue", "Bounded excerpt"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("digest missing %q:\n%s", want, digest)
		}
	}
}
