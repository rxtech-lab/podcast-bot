package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

const maxDeckSourceChars = 45_000

// DeckInput is the compact source used to create an exportable slide deck.
type DeckInput struct {
	Title           string
	Topic           string
	Language        string
	SummaryMarkdown string
}

// DeckSpec is the JSON contract passed to the Node pptxgenjs renderer.
type DeckSpec struct {
	Title    string      `json:"title"`
	Subtitle string      `json:"subtitle,omitempty"`
	Slides   []DeckSlide `json:"slides"`
}

// DeckSlide is one slide in the generated deck.
type DeckSlide struct {
	Title           string               `json:"title"`
	Kicker          string               `json:"kicker,omitempty"`
	Summary         string               `json:"summary,omitempty"`
	Bullets         []string             `json:"bullets,omitempty"`
	Takeaway        string               `json:"takeaway,omitempty"`
	SpeakerOpinions []DeckSpeakerOpinion `json:"speakerOpinions,omitempty"`
	Visual          DeckSlideVisual      `json:"visual,omitempty"`
	Notes           string               `json:"notes,omitempty"`
}

// DeckSpeakerOpinion captures a short, attributed stance for visual opinion
// cards in the PPT renderer.
type DeckSpeakerOpinion struct {
	Speaker  string `json:"speaker"`
	Opinion  string `json:"opinion"`
	Evidence string `json:"evidence,omitempty"`
}

// DeckSlideVisual gives the renderer enough semantic direction to draw a simple
// chart-like visual without depending on generated images.
type DeckSlideVisual struct {
	Kind  string   `json:"kind,omitempty"`
	Title string   `json:"title,omitempty"`
	Data  []string `json:"data,omitempty"`
}

// DeckGenerator creates a concise slide-deck spec from the finished Markdown
// summary. Rendering that spec to PPTX is handled outside Go by pptxgenjs.
type DeckGenerator struct {
	client *llm.Client
	env    *config.Env
}

// NewDeckGenerator builds a deck generator using PodcastSummaryPPTModel, falling
// back to the normal summary model, then HostModel.
func NewDeckGenerator(env *config.Env) *DeckGenerator {
	if env == nil {
		return &DeckGenerator{}
	}
	model := strings.TrimSpace(env.PodcastSummaryPPTModel)
	if model == "" {
		model = strings.TrimSpace(env.PodcastSummaryModel)
	}
	if model == "" {
		model = strings.TrimSpace(env.HostModel)
	}
	client := llm.New(env.OpenAIBaseURL, env.OpenAIKey, model)
	return &DeckGenerator{client: client, env: env}
}

// Model returns the model id the deck generator will use.
func (g *DeckGenerator) Model() string {
	if g == nil || g.client == nil {
		return ""
	}
	return g.client.Model()
}

// WithUsageRecorder returns a generator whose LLM calls report usage.
func (g *DeckGenerator) WithUsageRecorder(record func(llm.Usage)) *DeckGenerator {
	if g == nil || g.client == nil {
		return nil
	}
	next := *g
	next.client = g.client.WithUsageRecorder(record)
	if g.env != nil {
		next.client = next.client.WithPricing(g.env.LLMInputCostPerMillion, g.env.LLMOutputCostPerMillion)
	}
	return &next
}

// Generate creates and validates a DeckSpec. The model is asked for plain JSON
// only; callers persist the JSON as the "ppt" summary document body.
func (g *DeckGenerator) Generate(ctx context.Context, in DeckInput) (*DeckSpec, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("deck generator not configured")
	}
	stream, err := g.client.Stream(ctx, deckSystemPrompt(in.Language), []llm.Message{{
		Role:    llm.RoleUser,
		Content: buildDeckPrompt(in),
	}}, nil)
	if err != nil {
		return nil, fmt.Errorf("deck generator: %w", err)
	}
	var b strings.Builder
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		b.WriteString(d.TextChunk)
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("deck generator: %w", err)
	}
	spec, err := parseDeckSpec(b.String())
	if err != nil {
		return nil, err
	}
	spec.normalize(in.Title)
	if err := spec.validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

func deckSystemPrompt(language string) string {
	lang := strings.TrimSpace(language)
	if lang == "" {
		lang = "the podcast's language"
	}
	return `Create a concise slide deck JSON from a podcast summary.

Rules:
- Return only valid JSON. No Markdown fences.
- Write in ` + lang + `.
- Keep it simple but useful: 6 to 8 slides, 3 to 5 specific bullets per slide.
- Build content slides around the argument, not generic recap. Every slide should make one clear point.
- Each slide needs a short section label, one-sentence summary, concrete bullets, one takeaway, and a simple visual direction.
- Prefer details from the summary: causes, consequences, tradeoffs, actions, examples, speaker disagreements, or numbers when present.
- Include speakerOpinions whenever the summary names participants or positions. Use 1 to 3 items with the speaker's actual stance and evidence/reasoning; do not invent views.
- Use visual.kind to request one simple renderer-friendly visual: "spectrum", "compare", "timeline", "stack", "cycle", or "metric". Put 2 to 4 short labels or values in visual.data.
- Make bullets and visual.data complementary: bullets explain, visual.data labels the diagram.
- No long paragraphs. No citations unless already present in the summary.
- JSON shape: {"title":"...","subtitle":"...","slides":[{"title":"...","kicker":"...","summary":"...","bullets":["..."],"takeaway":"...","speakerOpinions":[{"speaker":"...","opinion":"...","evidence":"..."}],"visual":{"kind":"compare","title":"...","data":["..."]},"notes":"..."}]}.`
}

func buildDeckPrompt(in DeckInput) string {
	var sb strings.Builder
	sb.WriteString("Create a concise slide deck JSON for this podcast summary.\n\n")
	if title := strings.TrimSpace(in.Title); title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", title)
	}
	if topic := strings.TrimSpace(in.Topic); topic != "" {
		fmt.Fprintf(&sb, "Topic: %s\n", topic)
	}
	if lang := strings.TrimSpace(in.Language); lang != "" {
		fmt.Fprintf(&sb, "Language: %s\n", lang)
	}
	summary := strings.TrimSpace(in.SummaryMarkdown)
	if len(summary) > maxDeckSourceChars {
		summary = summary[:maxDeckSourceChars]
	}
	sb.WriteString("\nSummary Markdown:\n")
	sb.WriteString(summary)
	return sb.String()
}

func parseDeckSpec(raw string) (*DeckSpec, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j >= i {
		raw = raw[i : j+1]
	}
	if raw == "" {
		return nil, errors.New("deck generator produced no JSON")
	}
	var spec DeckSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("deck generator produced invalid JSON: %w", err)
	}
	return &spec, nil
}

func (s *DeckSpec) normalize(fallbackTitle string) {
	if s == nil {
		return
	}
	s.Title = strings.TrimSpace(s.Title)
	if s.Title == "" {
		s.Title = strings.TrimSpace(fallbackTitle)
	}
	if s.Title == "" {
		s.Title = "Podcast Summary"
	}
	s.Subtitle = strings.TrimSpace(s.Subtitle)
	out := make([]DeckSlide, 0, len(s.Slides))
	for _, slide := range s.Slides {
		slide.Title = strings.TrimSpace(slide.Title)
		if slide.Title == "" {
			continue
		}
		slide.Kicker = trimText(slide.Kicker, 48)
		slide.Summary = trimText(slide.Summary, 220)
		slide.Takeaway = trimText(slide.Takeaway, 180)
		slide.Notes = trimText(slide.Notes, 320)
		slide.Visual.Kind = trimText(slide.Visual.Kind, 28)
		slide.Visual.Title = trimText(slide.Visual.Title, 90)
		visualData := make([]string, 0, len(slide.Visual.Data))
		for _, item := range slide.Visual.Data {
			item = trimText(item, 64)
			if item == "" {
				continue
			}
			visualData = append(visualData, item)
			if len(visualData) >= 4 {
				break
			}
		}
		slide.Visual.Data = visualData
		opinions := make([]DeckSpeakerOpinion, 0, len(slide.SpeakerOpinions))
		for _, opinion := range slide.SpeakerOpinions {
			opinion.Speaker = trimText(opinion.Speaker, 44)
			opinion.Opinion = trimText(opinion.Opinion, 140)
			opinion.Evidence = trimText(opinion.Evidence, 110)
			if opinion.Speaker == "" || opinion.Opinion == "" {
				continue
			}
			opinions = append(opinions, opinion)
			if len(opinions) >= 3 {
				break
			}
		}
		slide.SpeakerOpinions = opinions
		bullets := make([]string, 0, len(slide.Bullets))
		for _, bullet := range slide.Bullets {
			bullet = trimText(bullet, 170)
			if bullet == "" {
				continue
			}
			bullets = append(bullets, bullet)
			if len(bullets) >= 5 {
				break
			}
		}
		slide.Bullets = bullets
		out = append(out, slide)
		if len(out) >= 8 {
			break
		}
	}
	s.Slides = out
}

func (s *DeckSpec) validate() error {
	if s == nil {
		return errors.New("deck spec is nil")
	}
	if strings.TrimSpace(s.Title) == "" {
		return errors.New("deck spec missing title")
	}
	if len(s.Slides) == 0 {
		return errors.New("deck spec missing slides")
	}
	for i, slide := range s.Slides {
		if strings.TrimSpace(slide.Title) == "" {
			return fmt.Errorf("deck slide %d missing title", i+1)
		}
		if len(slide.Bullets) == 0 {
			return fmt.Errorf("deck slide %d missing bullets", i+1)
		}
	}
	return nil
}

func trimText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return strings.TrimSpace(string(runes[:max]))
}
