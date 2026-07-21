package planner

import (
	"fmt"
	"strings"
)

// Limits for the source-marker catalog rendered into the audiobook planning
// digest. The catalog must stay bounded (it is prompt text), yet dense enough
// that the model can anchor every chapter: headings first, evenly spaced
// paragraph anchors when a source has too few headings.
const (
	audioBookMaxMarkers        = 300
	audioBookMinHeadingMarkers = 3
	audioBookParagraphAnchors  = 40
	audioBookMarkerTextLimit   = 180
	audioBookAnchorTextLimit   = 120
)

// sourceMarker is one referenceable position in the concatenated audiobook
// source. Index is 1-based and stable: the digest and the splitter derive the
// catalog identically from the same persisted source text, so an index the
// model saw always resolves to the same offset here.
type sourceMarker struct {
	Index  int
	Offset int
	Text   string
}

// chapterSlice is one chapter's real text sliced out of the source.
type chapterSlice struct {
	Index   int // 1-based chapter number, aligned with the draft's chapter list
	Start   int // byte offset into the source (chapter 1 always clamps to 0)
	Content string
}

// audioBookSplitError carries a model-facing message so write_plan/update_plan
// dispatch can surface it as a retryable tool error.
type audioBookSplitError struct{ msg string }

func (e *audioBookSplitError) Error() string { return e.msg }

// AudioBookSourceFromAttachments concatenates non-image document attachments'
// converted markdown in upload order. The digest shown to the model and the
// splitter both derive from this exact concatenation — keep them in lockstep.
func AudioBookSourceFromAttachments(attachments []Attachment) string {
	var parts []string
	for i, a := range attachments {
		if a.isImage() {
			continue
		}
		md := strings.TrimSpace(a.Markdown)
		if md == "" {
			continue
		}
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = fmt.Sprintf("document %d", i+1)
		}
		parts = append(parts, fmt.Sprintf("--- %s ---\n\n%s", name, md))
	}
	return strings.Join(parts, "\n\n")
}

// audioBookSourceMarkers builds the deterministic marker catalog for a source:
// every markdown ATX heading line (capped at audioBookMaxMarkers); when the
// source has fewer than audioBookMinHeadingMarkers headings, evenly spaced
// paragraph-start anchors are added so headingless documents remain
// addressable.
func audioBookSourceMarkers(source string) []sourceMarker {
	var markers []sourceMarker
	for _, ln := range sourceLines(source) {
		trim := strings.TrimSpace(ln.text)
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		markers = append(markers, sourceMarker{Offset: ln.offset, Text: truncate(trim, audioBookMarkerTextLimit)})
		if len(markers) >= audioBookMaxMarkers {
			break
		}
	}
	if len(markers) < audioBookMinHeadingMarkers {
		markers = mergeMarkers(markers, paragraphAnchors(source))
	}
	for i := range markers {
		markers[i].Index = i + 1
	}
	return markers
}

type sourceLine struct {
	offset int
	text   string
}

func sourceLines(source string) []sourceLine {
	var lines []sourceLine
	offset := 0
	for {
		i := strings.IndexByte(source[offset:], '\n')
		if i < 0 {
			lines = append(lines, sourceLine{offset: offset, text: source[offset:]})
			break
		}
		lines = append(lines, sourceLine{offset: offset, text: source[offset : offset+i]})
		offset += i + 1
	}
	return lines
}

// paragraphAnchors picks up to audioBookParagraphAnchors evenly spaced
// paragraph starts (a non-blank line following a blank line or the document
// start) and uses each paragraph's opening text as the marker.
func paragraphAnchors(source string) []sourceMarker {
	lines := sourceLines(source)
	var starts []sourceLine
	prevBlank := true
	for _, ln := range lines {
		blank := strings.TrimSpace(ln.text) == ""
		if !blank && prevBlank {
			starts = append(starts, ln)
		}
		prevBlank = blank
	}
	if len(starts) == 0 {
		return nil
	}
	step := 1
	if len(starts) > audioBookParagraphAnchors {
		step = len(starts) / audioBookParagraphAnchors
	}
	var markers []sourceMarker
	for i := 0; i < len(starts); i += step {
		ln := starts[i]
		markers = append(markers, sourceMarker{
			Offset: ln.offset,
			Text:   truncate(strings.TrimSpace(ln.text), audioBookAnchorTextLimit),
		})
		if len(markers) >= audioBookParagraphAnchors {
			break
		}
	}
	return markers
}

// mergeMarkers merges two offset-sorted marker sets, dropping duplicates at
// the same offset.
func mergeMarkers(a, b []sourceMarker) []sourceMarker {
	seen := make(map[int]bool, len(a))
	merged := make([]sourceMarker, 0, len(a)+len(b))
	for _, m := range append(a, b...) {
		if seen[m.Offset] {
			continue
		}
		seen[m.Offset] = true
		merged = append(merged, m)
	}
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0 && merged[j].Offset < merged[j-1].Offset; j-- {
			merged[j], merged[j-1] = merged[j-1], merged[j]
		}
	}
	return merged
}

// renderSourceMarkers renders the catalog block for the planning digest.
func renderSourceMarkers(markers []sourceMarker) string {
	if len(markers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Source markers (set each chapter's start_index to the marker where its text begins):\n")
	for _, m := range markers {
		fmt.Fprintf(&sb, "[%d] %s\n", m.Index, m.Text)
	}
	if len(markers) >= audioBookMaxMarkers {
		sb.WriteString("(catalog truncated; for chapters starting beyond the last marker, copy a verbatim start_marker line from the source instead)\n")
	}
	return sb.String()
}

// draftChaptersCarryMarkers reports whether any draft chapter references the
// source (start_index or start_marker). Chapter 1 alone carrying nothing still
// counts as unmarked.
func draftChaptersCarryMarkers(chapters []audioBookDraftChapter) bool {
	for _, ch := range chapters {
		if ch.StartIndex > 0 || strings.TrimSpace(ch.StartMarker) != "" {
			return true
		}
	}
	return false
}

// splitAudioBookSource resolves each draft chapter's start position and slices
// the source into per-chapter markdown. Resolution order per chapter: catalog
// start_index, exact start_marker substring, then a unique
// whitespace/case-normalized line match. Chapter 1 always slices from offset 0
// (front matter included); its marker, when given, participates only in
// ordering validation. Offsets must be strictly increasing; the last chapter
// runs to EOF. All errors are model-facing retry messages.
func splitAudioBookSource(source string, markers []sourceMarker, chapters []audioBookDraftChapter) ([]chapterSlice, error) {
	if strings.TrimSpace(source) == "" {
		return nil, &audioBookSplitError{msg: "no source document is available to split"}
	}
	if len(chapters) == 0 {
		return nil, &audioBookSplitError{msg: "the plan has no chapters to split the source into"}
	}
	offsets := make([]int, len(chapters))
	for i, ch := range chapters {
		off, err := resolveChapterStart(source, markers, ch, i+1)
		if err != nil {
			return nil, err
		}
		offsets[i] = off
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] <= offsets[i-1] {
			return nil, &audioBookSplitError{msg: fmt.Sprintf(
				"chapter %d starts at or before chapter %d in the source; chapter start markers must be strictly increasing — pick a later start_index for chapter %d",
				i+1, i, i+1)}
		}
	}
	slices := make([]chapterSlice, len(chapters))
	for i := range chapters {
		start := offsets[i]
		if i == 0 {
			start = 0
		}
		end := len(source)
		if i+1 < len(chapters) {
			end = offsets[i+1]
		}
		content := strings.TrimSpace(source[start:end])
		if content == "" {
			return nil, &audioBookSplitError{msg: fmt.Sprintf(
				"chapter %d would contain no source text; adjust its start_index (or the next chapter's) so every chapter covers real content", i+1)}
		}
		slices[i] = chapterSlice{Index: i + 1, Start: start, Content: content}
	}
	return slices, nil
}

func resolveChapterStart(source string, markers []sourceMarker, ch audioBookDraftChapter, number int) (int, error) {
	if ch.StartIndex > 0 {
		if ch.StartIndex > len(markers) {
			return 0, &audioBookSplitError{msg: fmt.Sprintf(
				"chapter %d: start_index %d is outside the Source markers catalog (1..%d); use a listed index or a verbatim start_marker",
				number, ch.StartIndex, len(markers))}
		}
		return markers[ch.StartIndex-1].Offset, nil
	}
	marker := strings.TrimSpace(ch.StartMarker)
	if marker == "" {
		if number == 1 {
			return 0, nil
		}
		return 0, &audioBookSplitError{msg: fmt.Sprintf(
			"chapter %d has neither start_index nor start_marker; every chapter after the first must anchor to the Source markers catalog", number)}
	}
	if off := strings.Index(source, marker); off >= 0 {
		return off, nil
	}
	off, err := fuzzyMarkerOffset(source, marker, number)
	if err != nil {
		return 0, err
	}
	return off, nil
}

// fuzzyMarkerOffset falls back to a whitespace-collapsed, case-insensitive
// line scan and requires the match to be unique.
func fuzzyMarkerOffset(source, marker string, number int) (int, error) {
	want := normalizeMarkerText(marker)
	if want == "" {
		return 0, &audioBookSplitError{msg: fmt.Sprintf("chapter %d: start_marker is empty after normalization", number)}
	}
	matches := 0
	offset := -1
	for _, ln := range sourceLines(source) {
		got := normalizeMarkerText(ln.text)
		if got == "" || !strings.Contains(got, want) {
			continue
		}
		matches++
		if offset < 0 {
			offset = ln.offset
		}
		if matches > 1 {
			return 0, &audioBookSplitError{msg: fmt.Sprintf(
				"chapter %d: start_marker %q matches more than one place in the source; copy a longer verbatim line so the match is unique", number, truncate(marker, 120))}
		}
	}
	if matches == 0 {
		return 0, &audioBookSplitError{msg: fmt.Sprintf(
			"chapter %d: start_marker %q was not found in the source; copy a heading or line exactly as it appears (or use a start_index from the Source markers catalog)", number, truncate(marker, 120))}
	}
	return offset, nil
}

func normalizeMarkerText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
