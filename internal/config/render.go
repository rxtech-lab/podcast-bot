package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidateTopic runs the same type-aware validation LoadTopic applies, exported
// so callers that build a DebateTopic in memory (the planning endpoint, the
// JSON job-submit path) can reject malformed topics before rendering them.
func ValidateTopic(t *DebateTopic) error { return validateTopic(t) }

// RenderMarkdown serialises a DebateTopic back into the `---` frontmatter +
// `## Section` markdown form LoadTopic parses. It is the inverse of LoadTopic:
// writing the result to disk and re-loading it yields an equivalent topic
// (see render_test.go). Only the fields relevant to t.Type are emitted, so a
// discussion topic never carries empty debate/puzzle/series keys.
//
// Body sections (Background/Surface/…) are markdown, not frontmatter, and are
// emitted with headings whose lowercased text matches parseSections' keys.
func (t *DebateTopic) RenderMarkdown() (string, error) {
	fm := newFrontmatter()
	// Common scalars — always present so a round-trip preserves the defaults
	// LoadTopic would otherwise re-apply.
	fm.add("title", t.Title)
	fm.add("type", t.Type)
	fm.add("language", t.Language)
	fm.add("total_minutes", t.TotalMinutes)
	fm.add("segment_max_seconds", t.SegmentMaxSeconds)
	fm.add("channel", t.Channel)
	fm.addIf("tts_provider", t.TTSProvider, t.TTSProvider != "")
	fm.addIf("resolution", t.Resolution, t.Resolution != "")

	switch t.Type {
	case ContentTypeDebate:
		fm.add("affirmative", t.Affirmative)
		fm.add("negative", t.Negative)
		fm.add("judge", t.Judge)
	case ContentTypeSituationPuzzle:
		fm.add("puzzle_host", t.PuzzleHost)
		fm.add("players", t.Players)
	case ContentTypeSeries:
		fm.add("show", t.Show)
		fm.add("season", t.Season)
		fm.add("episode", t.Episode)
		fm.add("series_host", t.SeriesHost)
	case ContentTypeDiscussion:
		fm.add("host", t.Host)
		fm.add("discussants", t.Discussants)
		fm.add("commander", t.Commander)
		fm.addIf("storage", t.Storage, t.Storage != "")
	}
	if len(t.Viewers) > 0 {
		fm.add("viewers", t.Viewers)
	}

	front, err := fm.marshal()
	if err != nil {
		return "", fmt.Errorf("render frontmatter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(front)
	if !strings.HasSuffix(front, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("---\n")

	// Body sections, in the order LoadTopic recognises them. Heading text is
	// title-cased for readability; parseSections lowercases before matching.
	writeSection(&b, "Background", t.Background)
	writeSection(&b, "Affirmative Position", t.AffirmativePos)
	writeSection(&b, "Negative Position", t.NegativePos)
	writeSection(&b, "Rules", t.Rules)
	writeSection(&b, "Surface", t.Surface)
	writeSection(&b, "Truth", t.Truth)

	return b.String(), nil
}

func writeSection(b *strings.Builder, heading, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	b.WriteString("\n## ")
	b.WriteString(heading)
	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteByte('\n')
}

// frontmatter is an insertion-ordered YAML mapping builder. yaml.v3 maps don't
// preserve key order and `omitempty` doesn't drop zero structs, so we assemble
// an explicit MappingNode instead — giving full control over which keys appear
// and in what order.
type frontmatter struct {
	root *yaml.Node
	err  error
}

func newFrontmatter() *frontmatter {
	return &frontmatter{root: &yaml.Node{Kind: yaml.MappingNode}}
}

func (f *frontmatter) add(key string, val any) {
	if f.err != nil {
		return
	}
	v := &yaml.Node{}
	if err := v.Encode(val); err != nil {
		f.err = err
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	f.root.Content = append(f.root.Content, k, v)
}

func (f *frontmatter) addIf(key string, val any, cond bool) {
	if cond {
		f.add(key, val)
	}
}

func (f *frontmatter) marshal() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	out, err := yaml.Marshal(f.root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
