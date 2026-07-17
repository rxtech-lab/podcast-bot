package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// Chunk sizing (characters, ≈4 chars/token). Transcript chunks stay small so
// a retrieval hit pinpoints a moment in the episode; source chunks are larger
// because prose documents carry more context per passage.
const (
	transcriptChunkTarget = 1200
	transcriptChunkMax    = 2000
	sourceChunkTarget     = 2000
	sourceChunkOverlap    = 200
)

// chunkTranscript groups consecutive transcript lines into retrieval chunks.
// Each line is rendered as "Speaker (role): text" so the embedded passage
// carries who said it; the chunk meta records the covered time/line range.
// The previous chunk's last line is repeated at the start of the next chunk
// so a thought split across a boundary still matches on both sides.
func chunkTranscript(lines []DiscussionLine) []ChunkInput {
	type entry struct {
		text    string
		speaker string
		startMS int64
		line    int
	}
	entries := make([]entry, 0, len(lines))
	for i, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		rendered := l.Speaker
		if role := strings.TrimSpace(l.Role); role != "" {
			rendered += " (" + role + ")"
		}
		if rendered == "" {
			rendered = "Speaker"
		}
		entries = append(entries, entry{
			text:    rendered + ": " + text,
			speaker: strings.TrimSpace(l.Speaker),
			startMS: l.StartMS,
			line:    i,
		})
	}
	build := func(buf []entry, index int) ChunkInput {
		texts := make([]string, len(buf))
		speakerSet := map[string]bool{}
		speakers := make([]string, 0, 4)
		for i, e := range buf {
			texts[i] = e.text
			if e.speaker != "" && !speakerSet[e.speaker] {
				speakerSet[e.speaker] = true
				speakers = append(speakers, e.speaker)
			}
		}
		return ChunkInput{
			Kind:       ChunkKindTranscript,
			ChunkIndex: index,
			Text:       strings.Join(texts, "\n"),
			Meta: ChunkMeta{
				Speakers:  speakers,
				StartMS:   buf[0].startMS,
				EndMS:     buf[len(buf)-1].startMS,
				LineStart: buf[0].line,
				LineEnd:   buf[len(buf)-1].line,
			},
		}
	}
	chunks := make([]ChunkInput, 0)
	var buf []entry
	var size int
	for _, e := range entries {
		if len(e.text) > transcriptChunkMax {
			e.text = e.text[:transcriptChunkMax]
		}
		if size+len(e.text) > transcriptChunkTarget && size > 0 {
			chunks = append(chunks, build(buf, len(chunks)))
			// Seed the next chunk with the last line for boundary overlap,
			// unless that line alone already fills the target.
			last := buf[len(buf)-1]
			buf, size = nil, 0
			if len(last.text) < transcriptChunkTarget {
				buf = append(buf, last)
				size = len(last.text)
			}
		}
		buf = append(buf, e)
		size += len(e.text)
	}
	// The final buffer always holds at least one line beyond the seeded
	// overlap (seeding happens only right before appending the next line),
	// so the tail is never redundant.
	if len(buf) > 0 {
		chunks = append(chunks, build(buf, len(chunks)))
	}
	return chunks
}

// chunkSources splits each source's fetched Markdown into overlapping
// passages prefixed with the source title, falling back to the snippet when
// no Markdown was captured. Chunk indexes continue from startIndex so
// transcript + source chunks share one sequence per discussion.
func chunkSources(sources []config.Source, startIndex int) []ChunkInput {
	chunks := make([]ChunkInput, 0)
	for _, src := range sources {
		body := strings.TrimSpace(src.Markdown)
		if body == "" {
			body = strings.TrimSpace(src.Snippet)
		}
		if body == "" {
			continue
		}
		title := strings.TrimSpace(src.Title)
		prefix := ""
		if title != "" {
			prefix = "# " + title + "\n"
		}
		for _, part := range splitTextChunks(body, sourceChunkTarget, sourceChunkOverlap) {
			chunks = append(chunks, ChunkInput{
				Kind:       ChunkKindSource,
				ChunkIndex: startIndex + len(chunks),
				Text:       prefix + part,
				Meta: ChunkMeta{
					SourceURL:   strings.TrimSpace(src.URL),
					SourceTitle: title,
				},
			})
		}
	}
	return chunks
}

// splitTextChunks splits text into ~target-char pieces, preferring paragraph
// boundaries, then line breaks, then word boundaries, with `overlap` trailing
// characters repeated at the start of the following piece.
func splitTextChunks(text string, target, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= target {
		return []string{text}
	}
	out := make([]string, 0, len(text)/target+1)
	for len(text) > 0 {
		if len(text) <= target {
			out = append(out, text)
			break
		}
		cut := target
		if idx := strings.LastIndex(text[:cut], "\n\n"); idx > target/2 {
			cut = idx
		} else if idx := strings.LastIndex(text[:cut], "\n"); idx > target/2 {
			cut = idx
		} else if idx := strings.LastIndex(text[:cut], " "); idx > target/2 {
			cut = idx
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		next := cut - overlap
		if next < cut/2 {
			next = cut
		}
		// Advance from a word boundary so the overlap never starts mid-word.
		if next > 0 && next < len(text) {
			if idx := strings.IndexByte(text[next:], ' '); idx >= 0 && idx < overlap {
				next += idx + 1
			}
		}
		text = strings.TrimSpace(text[next:])
	}
	return out
}

// discussionContentHash fingerprints the indexable content of a discussion so
// a re-index can be skipped when nothing changed. It covers the transcript
// line texts (with speakers and timings) and each source's identity + body.
func discussionContentHash(lines []DiscussionLine, sources []config.Source) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l.Speaker))
		h.Write([]byte{0})
		h.Write([]byte(l.Text))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(l.StartMS, 10)))
		h.Write([]byte{1})
	}
	for _, s := range sources {
		h.Write([]byte(s.URL))
		h.Write([]byte{0})
		h.Write([]byte(s.Title))
		h.Write([]byte{0})
		h.Write([]byte(fmt.Sprintf("%d:%d", len(s.Markdown), len(s.Snippet))))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}
