package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// newsDraft is the creative payload the LLM returns for a news-podcast plan;
// the planner supplies the non-creative scaffolding (type, language, channel,
// models). Mirrors newsPlanSchema() in templates.go.
type newsDraft struct {
	Title      string `json:"title"`
	Background string `json:"background"`
	Anchor     struct {
		Name string `json:"name"`
	} `json:"anchor"`
	Commentators []struct {
		Name  string `json:"name"`
		Focus string `json:"focus"`
	} `json:"commentators"`
	Stories []struct {
		Headline string   `json:"headline"`
		Summary  string   `json:"summary"`
		KeyFacts []string `json:"key_facts"`
	} `json:"stories"`
}

func decodeNewsDraft(raw string) (*newsDraft, error) {
	var d newsDraft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("decode news plan args: %w", err)
	}
	if strings.TrimSpace(d.Title) == "" || len(d.Commentators) == 0 || len(d.Stories) == 0 {
		return nil, fmt.Errorf("news plan returned an incomplete draft (need title, commentators, and stories)")
	}
	return &d, nil
}

// assembleNewsWithModel turns a news draft into a full, validated DebateTopic.
// The anchor maps onto Host, commentators onto Discussants (beat → Aspect) so
// the news broadcast rides the discussion live runtime.
func (p *Planner) assembleNewsWithModel(d *newsDraft, lang, channel string, sources []config.Source, model string) (*Result, error) {
	if strings.TrimSpace(model) == "" {
		model = p.agentModel()
	}
	anchor := config.AgentSpec{Name: strings.TrimSpace(d.Anchor.Name), Model: model}
	if anchor.Name == "" {
		anchor.Name = "Anchor"
	}
	commentators := make([]config.AgentSpec, 0, len(d.Commentators))
	for _, c := range d.Commentators {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		commentators = append(commentators, config.AgentSpec{
			Name:   name,
			Model:  model,
			Aspect: strings.TrimSpace(c.Focus),
		})
	}
	stories := make([]config.NewsStory, 0, len(d.Stories))
	for _, s := range d.Stories {
		if strings.TrimSpace(s.Headline) == "" || strings.TrimSpace(s.Summary) == "" {
			continue
		}
		facts := make([]string, 0, len(s.KeyFacts))
		for _, f := range s.KeyFacts {
			if f = strings.TrimSpace(f); f != "" {
				facts = append(facts, f)
			}
		}
		stories = append(stories, config.NewsStory{
			Headline: strings.TrimSpace(s.Headline),
			Summary:  strings.TrimSpace(s.Summary),
			KeyFacts: facts,
		})
	}
	totalMinutes := 30
	if p.env.E2EMode {
		// Keep E2E broadcasts tiny (see assembleWithModel).
		totalMinutes = 1
	}
	topic := &config.DebateTopic{
		Title:             strings.TrimSpace(d.Title),
		Type:              config.ContentTypeNews,
		Language:          lang,
		TotalMinutes:      totalMinutes,
		SegmentMaxSeconds: 60,
		TTSProvider:       config.TTSProviderAzure,
		Resolution:        config.Resolution1080p,
		Channel:           defaultChannel(channel),
		Host:              anchor,
		Discussants:       commentators,
		Commander:         config.AgentSpec{Model: model},
		Storage:           config.StoragePlaintext,
		NewsStories:       stories,
		Background:        strings.TrimSpace(d.Background),
		Sources:           sources,
	}
	if err := config.ValidateTopic(topic); err != nil {
		return nil, fmt.Errorf("planner produced an invalid news script: %w", err)
	}
	md, err := topic.RenderMarkdown()
	if err != nil {
		return nil, fmt.Errorf("render planned news script: %w", err)
	}
	return &Result{Script: topic, Markdown: md, Sources: sources, Researched: len(sources) > 0}, nil
}

// generateNews drafts a brand-new news-podcast script from a topic (the
// one-shot, non-conversational path).
func (p *Planner) generateNews(ctx context.Context, req PlanRequest) (*Result, error) {
	if strings.TrimSpace(req.Topic) == "" {
		return nil, fmt.Errorf("topic is required")
	}
	n := req.Discussants
	if n < 1 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	lang := req.Language
	if lang == "" {
		lang = "en-US"
	}

	user := fmt.Sprintf(`Design a radio-news broadcast about the following topic.

Topic: %s
Language for all names and text: %s
Number of commentators: %d

Return STRICT JSON with this exact shape:
{
  "title": "a concise broadcast title — fewer than 10 words",
  "background": "1-3 neutral paragraphs of shared context for today's broadcast, in the requested language",
  "anchor": { "name": "the anchor's display name" },
  "commentators": [ { "name": "display name", "focus": "this commentator's beat or analytical angle" } ],
  "stories": [ { "headline": "on-air headline", "summary": "2-4 sentences the anchor reads from; include publication dates and outlet attributions", "key_facts": ["concrete, dated, attributed facts"] } ]
}
Each commentator must have a DISTINCT beat that maps onto the rundown. Use %d commentators. Prefer sources published within the last 72 hours; include publication dates in summaries and key_facts.%s%s%s%s`,
		req.Topic, lang, n, n, newsTemplatePrompt(req.Template), referencePrompt(req.Reference), planningRequirementsPrompt(req.Research, extractURLs(req.Topic), req.Template), attachmentsPrompt(req.Attachments))

	d, sources, err := p.draftNewsJSON(ctx, user, req.Attachments, planningAgentOptions{
		ResearchRequired: req.Research,
		RequiredURLs:     extractURLs(req.Topic),
		Template:         req.Template,
	})
	if err != nil {
		return nil, err
	}
	return p.assembleNewsWithModel(d, lang, req.Channel, sources, p.agentModel())
}

func newsTemplatePrompt(template string) string {
	instructions := NewsTemplateInstructions(template)
	if instructions == "" {
		return ""
	}
	return "\n\nTemplate instructions:\n" + instructions
}
