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
// always composites at 1280×720; ffmpeg upscales to the chosen size with a
// Lanczos filter so streams can target higher-resolution clients.
const (
	Resolution720p  = "720p"
	Resolution1080p = "1080p"
	Resolution4K    = "4k"
)

// DebateTopic is the full debate.md content: YAML frontmatter + named markdown sections.
type DebateTopic struct {
	Title             string      `yaml:"title"`
	Language          string      `yaml:"language"`
	TotalMinutes      int         `yaml:"total_minutes"`
	SegmentMaxSeconds int         `yaml:"segment_max_seconds"`
	TTSProvider       string      `yaml:"tts_provider,omitempty"`
	Resolution        string      `yaml:"resolution,omitempty"`
	// Parallel, when true on any debate in the queue, makes the whole queue
	// run concurrently as separate channels (each with its own encoder + HLS
	// stream) instead of the default sequential mode.
	Parallel    bool        `yaml:"parallel,omitempty"`
	Affirmative []AgentSpec `yaml:"affirmative"`
	Negative    []AgentSpec `yaml:"negative"`
	Judge       AgentSpec   `yaml:"judge"`
	Viewers     []AgentSpec `yaml:"viewers"`

	// Body sections, populated from markdown after frontmatter.
	Background     string `yaml:"-"`
	AffirmativePos string `yaml:"-"`
	NegativePos    string `yaml:"-"`
	Rules          string `yaml:"-"`
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
		t.Resolution = Resolution720p
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
	sections := map[string]*string{
		"background":          &t.Background,
		"affirmative position": &t.AffirmativePos,
		"negative position":    &t.NegativePos,
		"rules":                &t.Rules,
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
	for _, v := range t.Viewers {
		if v.Name == "" || v.Model == "" {
			return fmt.Errorf("viewer entry needs name and model")
		}
	}
	return nil
}
