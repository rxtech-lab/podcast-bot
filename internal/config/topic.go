package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentSpec describes one agent declared in topic.md frontmatter.
// BaseURL/APIKey are optional per-agent overrides; otherwise the env defaults are used.
type AgentSpec struct {
	Name    string `yaml:"name"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
}

// TTS provider identifiers used in topic.md `tts_provider:` field.
const (
	TTSProviderAzure  = "azure"
	TTSProviderEleven = "eleven"
)

// Output resolutions selectable from topic.md `resolution:` field. The renderer
// composites at 1920×1080 by default; ffmpeg only scales when callers request
// a different delivery size.
const (
	Resolution720p  = "720p"
	Resolution1080p = "1080p"
	Resolution4K    = "4k"
)

// Content types selectable via the `type:` field in topic.md frontmatter. The
// orchestrator picks an agent set + planner based on this value.
const (
	ContentTypeDebate          = "debate"
	ContentTypeSituationPuzzle = "situation-puzzle"
	// ContentTypeSeries is a host-only narrated TV-style episode. Episodes
	// declare show + season + episode in frontmatter; the pipeline writes
	// every episode's assets (scene plan, generated PNGs, music, recap-
	// ready script) into a stable on-disk archive at
	// `<persistent-root>/tv-series/<show>/s<season>/e<episode>/`. Episode
	// N+1 reads that archive to (a) build a "previously on …" preamble
	// via the compression LLM and (b) re-use specific past beats by
	// emitting `<season-S-episode-E-image-N/>` markers in the host's
	// stream.
	ContentTypeSeries = "series"
)

// DebateTopic is the full topic.md content: YAML frontmatter + named markdown
// sections. Despite the name, it now covers every supported content type
// (debate + situation-puzzle); the active subset of fields depends on Type.
type DebateTopic struct {
	Title             string `yaml:"title"`
	Type              string `yaml:"type"`
	Language          string `yaml:"language"`
	TotalMinutes      int    `yaml:"total_minutes"`
	SegmentMaxSeconds int    `yaml:"segment_max_seconds"`
	TTSProvider       string `yaml:"tts_provider,omitempty"`
	Resolution        string `yaml:"resolution,omitempty"`
	// Channel is the id of the TV-style channel this debate belongs to.
	// Channels are defined in channels.json. Multiple debates with the same
	// channel id are queued and play sequentially within that channel; debates
	// on different channels run in parallel as independent video streams.
	// Required — startup fails if the id isn't defined in channels.json.
	Channel string `yaml:"channel"`

	// Debate-only roster.
	Affirmative []AgentSpec `yaml:"affirmative,omitempty"`
	Negative    []AgentSpec `yaml:"negative,omitempty"`
	Judge       AgentSpec   `yaml:"judge,omitempty"`

	// Situation-puzzle-only roster. PuzzleHost is the 出題者 who knows the
	// hidden truth and answers player questions with 是/不是/與此無關.
	// Players are 解題者 trying to deduce the truth.
	PuzzleHost AgentSpec   `yaml:"puzzle_host,omitempty"`
	Players    []AgentSpec `yaml:"players,omitempty"`

	// Series-only roster + metadata. Show is the human-readable show name
	// (slugified for the on-disk archive directory). Season + Episode are
	// 1-based; the recap engine treats lexicographic (season, episode)
	// order as canonical "before this episode" (so s2e1 follows s1e9).
	// SeriesHost is the single narrator agent; series episodes are
	// non-interactive (no players, no Q&A, no live audience).
	Show       string    `yaml:"show,omitempty"`
	Season     int       `yaml:"season,omitempty"`
	Episode    int       `yaml:"episode,omitempty"`
	SeriesHost AgentSpec `yaml:"series_host,omitempty"`

	// Shared across both content types.
	Viewers []AgentSpec `yaml:"viewers,omitempty"`

	// Body sections, populated from markdown after frontmatter.
	// Debate sections:
	Background     string `yaml:"-"`
	AffirmativePos string `yaml:"-"`
	NegativePos    string `yaml:"-"`
	Rules          string `yaml:"-"`
	// Situation-puzzle sections:
	Surface string `yaml:"-"` // 湯面 — visible to everyone
	Truth   string `yaml:"-"` // 湯底 — only the puzzle host's prompt sees it
}

// LoadTopic parses a debate.md file with YAML frontmatter and markdown body.
func LoadTopic(path string) (*DebateTopic, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read debate: %w", err)
	}
	front, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return nil, err
	}
	var t DebateTopic
	if err := yaml.Unmarshal([]byte(front), &t); err != nil {
		return nil, fmt.Errorf("parse debate frontmatter: %w", err)
	}
	parseSections(body, &t)
	if err := validateTopic(&t); err != nil {
		return nil, err
	}
	if t.Language == "" {
		t.Language = "en-US"
	}
	if t.SegmentMaxSeconds == 0 {
		t.SegmentMaxSeconds = 60
	}
	if t.TotalMinutes == 0 {
		t.TotalMinutes = 30
	}
	if t.TTSProvider == "" {
		t.TTSProvider = TTSProviderAzure
	}
	if t.Resolution == "" {
		t.Resolution = Resolution1080p
	}
	return &t, nil
}

func splitFrontmatter(s string) (front, body string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 1<<20), 1<<22)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("topic.md must start with --- frontmatter fence")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", fmt.Errorf("topic.md frontmatter not closed with ---")
	}
	front = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return front, body, nil
}

func parseSections(body string, t *DebateTopic) {
	// Series content can name its synopsis section "Surface" (matches the
	// puzzle convention) or "Series" / "Series summary" (more idiomatic
	// for a TV-series episode). Both feed the same Surface field — the
	// downstream pipeline doesn't care about the heading text.
	sections := map[string]*string{
		"background":           &t.Background,
		"affirmative position": &t.AffirmativePos,
		"negative position":    &t.NegativePos,
		"rules":                &t.Rules,
		"surface":              &t.Surface,
		"series":               &t.Surface,
		"series summary":       &t.Surface,
		"synopsis":             &t.Surface,
		"truth":                &t.Truth,
	}
	var current *string
	var buf strings.Builder
	flush := func() {
		if current != nil {
			*current = strings.TrimSpace(buf.String())
		}
		buf.Reset()
	}
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") {
			flush()
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trim, "## ")))
			current = sections[heading]
			continue
		}
		if current != nil {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
}

func validateTopic(t *DebateTopic) error {
	if t.Title == "" {
		return fmt.Errorf("topic title is required")
	}
	if t.Channel == "" {
		return fmt.Errorf("channel is required (set `channel: <id>` in frontmatter; ids are defined in channels.json)")
	}
	switch t.Type {
	case ContentTypeDebate, ContentTypeSituationPuzzle, ContentTypeSeries:
	default:
		return fmt.Errorf("type must be one of %q, %q, %q (got %q)",
			ContentTypeDebate, ContentTypeSituationPuzzle, ContentTypeSeries, t.Type)
	}
	switch t.TTSProvider {
	case "", TTSProviderAzure, TTSProviderEleven:
	default:
		return fmt.Errorf("tts_provider must be %q or %q (got %q)",
			TTSProviderAzure, TTSProviderEleven, t.TTSProvider)
	}
	switch t.Resolution {
	case "", Resolution720p, Resolution1080p, Resolution4K:
	default:
		return fmt.Errorf("resolution must be one of %q, %q, %q (got %q)",
			Resolution720p, Resolution1080p, Resolution4K, t.Resolution)
	}
	for _, v := range t.Viewers {
		if v.Name == "" || v.Model == "" {
			return fmt.Errorf("viewer entry needs name and model")
		}
	}
	switch t.Type {
	case ContentTypeDebate:
		return validateDebate(t)
	case ContentTypeSituationPuzzle:
		return validateSituationPuzzle(t)
	case ContentTypeSeries:
		return validateSeries(t)
	}
	return nil
}

func validateSeries(t *DebateTopic) error {
	// Series episodes are host-only — no debaters, no judge, no puzzle host,
	// no players. Reject those fields with a clear message rather than
	// silently ignoring them; otherwise a copy-paste from a debate fixture
	// would build extra agents that never speak.
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=series must not declare affirmative/negative/judge — series uses series_host only")
	}
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 {
		return fmt.Errorf("type=series must not declare puzzle_host or players — series uses series_host only")
	}
	if t.SeriesHost.Model == "" {
		return fmt.Errorf("series_host.model is required for type=series")
	}
	if strings.TrimSpace(t.Show) == "" {
		return fmt.Errorf("type=series requires a non-empty `show` frontmatter field (used to namespace the on-disk archive)")
	}
	if t.Season < 1 {
		return fmt.Errorf("type=series requires `season` >= 1 (got %d)", t.Season)
	}
	if t.Episode < 1 {
		return fmt.Errorf("type=series requires `episode` >= 1 (got %d)", t.Episode)
	}
	if strings.TrimSpace(t.Surface) == "" {
		return fmt.Errorf("type=series requires a synopsis section — `## Surface`, `## Series`, `## Series summary`, or `## Synopsis`")
	}
	if strings.TrimSpace(t.Truth) != "" {
		return fmt.Errorf("type=series must not declare a `## Truth` section — series episodes are not puzzles")
	}
	return nil
}

func validateDebate(t *DebateTopic) error {
	if t.PuzzleHost.Model != "" || len(t.Players) > 0 {
		return fmt.Errorf("type=debate must not declare puzzle_host or players")
	}
	if len(t.Affirmative) == 0 {
		return fmt.Errorf("at least one affirmative candidate required")
	}
	if len(t.Negative) == 0 {
		return fmt.Errorf("at least one negative candidate required")
	}
	if t.Judge.Model == "" {
		return fmt.Errorf("judge.model is required")
	}
	for _, s := range t.Affirmative {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("affirmative entry needs name and model")
		}
	}
	for _, s := range t.Negative {
		if s.Name == "" || s.Model == "" {
			return fmt.Errorf("negative entry needs name and model")
		}
	}
	return nil
}

func validateSituationPuzzle(t *DebateTopic) error {
	if len(t.Affirmative) > 0 || len(t.Negative) > 0 || t.Judge.Model != "" {
		return fmt.Errorf("type=situation-puzzle must not declare affirmative/negative/judge — use puzzle_host and players instead")
	}
	if t.PuzzleHost.Model == "" {
		return fmt.Errorf("puzzle_host.model is required for type=situation-puzzle")
	}
	if len(t.Players) == 0 {
		return fmt.Errorf("at least one player required for type=situation-puzzle")
	}
	for _, p := range t.Players {
		if p.Name == "" || p.Model == "" {
			return fmt.Errorf("player entry needs name and model")
		}
	}
	if strings.TrimSpace(t.Surface) == "" {
		return fmt.Errorf("type=situation-puzzle requires a `## Surface` section (湯面)")
	}
	if strings.TrimSpace(t.Truth) == "" {
		return fmt.Errorf("type=situation-puzzle requires a `## Truth` section (湯底)")
	}
	return nil
}
