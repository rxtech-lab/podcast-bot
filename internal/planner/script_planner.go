// Package planner turns a free-text topic into a ready-to-edit discussion
// script (config.DebateTopic). It backs the dashboard's "planning phase":
// the user types a topic, the engine drafts a full panel-discussion roster +
// background, and the user confirms or asks for revisions.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// Planner drafts and revises discussion scripts with a single LLM.
type Planner struct {
	env *config.Env
}

// New builds a Planner from engine env. Returns an error when env is nil so
// the HTTP layer can 503 cleanly rather than panic.
func New(env *config.Env) (*Planner, error) {
	if env == nil {
		return nil, fmt.Errorf("planner requires engine env")
	}
	return &Planner{env: env}, nil
}

// PlanRequest is the input to Generate.
type PlanRequest struct {
	Type        string `json:"type"`
	Topic       string `json:"topic"`
	Language    string `json:"language"`
	Channel     string `json:"channel"`
	Discussants int    `json:"discussants"`
	// Research asks the planner to ground the draft in live web sources.
	// Not yet wired (no built-in web search); when true the response still
	// reports researched=false so the caller can surface that to the user.
	Research bool `json:"research"`
}

// Result is what Generate / Improve return: the structured script, its
// rendered markdown form, and whether live research was actually performed.
type Result struct {
	Script     *config.DebateTopic
	Markdown   string
	Sources    []config.Source
	Researched bool
}

// draft is the creative payload the LLM returns; the planner supplies the
// non-creative scaffolding (type, language, channel, models).
type draft struct {
	Title      string `json:"title"`
	Background string `json:"background"`
	Host       struct {
		Name string `json:"name"`
	} `json:"host"`
	Discussants []struct {
		Name   string `json:"name"`
		Aspect string `json:"aspect"`
	} `json:"discussants"`
}

// Generate drafts a brand-new script from a topic.
func (p *Planner) Generate(ctx context.Context, req PlanRequest) (*Result, error) {
	if req.Type != "" && req.Type != config.ContentTypeDiscussion {
		return nil, fmt.Errorf("only %q planning is supported (got %q)", config.ContentTypeDiscussion, req.Type)
	}
	if strings.TrimSpace(req.Topic) == "" {
		return nil, fmt.Errorf("topic is required")
	}
	n := req.Discussants
	if n < 2 {
		n = 3
	}
	if n > 6 {
		n = 6
	}
	lang := req.Language
	if lang == "" {
		lang = "en-US"
	}

	// Optionally ground the draft in real web sources. Best-effort: when no
	// search backend is configured (or it fails) sources is empty and the plan
	// reports researched=false.
	var sources []config.Source
	if req.Research {
		sources, _ = p.research(ctx, req.Topic)
	}

	user := fmt.Sprintf(`Design a panel discussion about the following topic.

Topic: %s
Language for all names and text: %s
Number of discussants: %d

Return STRICT JSON with this exact shape:
{
  "title": "a concise, engaging title",
  "background": "2-4 paragraphs of neutral background framing the discussion, in the requested language",
  "host": { "name": "moderator's display name" },
  "discussants": [ { "name": "display name", "aspect": "the distinct angle/perspective this person argues from" } ]
}
Each discussant must have a DISTINCT aspect (e.g. economic, ethical, technical, historical, cultural). Use %d discussants.%s`,
		req.Topic, lang, n, n, sourcesPrompt(sources))

	d, err := p.draftJSON(ctx, user)
	if err != nil {
		return nil, err
	}
	return p.assemble(d, lang, req.Channel, sources)
}

// Improve revises an existing script per a free-text instruction.
func (p *Planner) Improve(ctx context.Context, prev *config.DebateTopic, instruction string) (*Result, error) {
	if prev == nil {
		return nil, fmt.Errorf("previousScript is required")
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("instruction is required")
	}
	lang := prev.Language
	if lang == "" {
		lang = "en-US"
	}
	prevJSON, _ := json.Marshal(map[string]any{
		"title":       prev.Title,
		"background":  prev.Background,
		"host":        map[string]string{"name": prev.Host.Name},
		"discussants": discussantViews(prev.Discussants),
	})

	user := fmt.Sprintf(`Here is the current panel-discussion draft as JSON:

%s

Revise it per this instruction: %s

Return STRICT JSON with the SAME shape (title, background, host{name}, discussants[]{name, aspect}). Keep the language as %s. Preserve good parts; change only what the instruction asks for.`,
		string(prevJSON), instruction, lang)

	d, err := p.draftJSON(ctx, user)
	if err != nil {
		return nil, err
	}
	// Improve preserves the prior channel and carries forward any sources the
	// original plan researched, so a chat-edit doesn't drop the references.
	return p.assemble(d, lang, prev.Channel, prev.Sources)
}

func discussantViews(specs []config.AgentSpec) []map[string]string {
	out := make([]map[string]string, len(specs))
	for i, s := range specs {
		out[i] = map[string]string{"name": s.Name, "aspect": s.Aspect}
	}
	return out
}

// draftJSON runs one strict-JSON completion and decodes the creative payload.
func (p *Planner) draftJSON(ctx context.Context, user string) (*draft, error) {
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	const system = "You are a producer who designs balanced, well-cast panel discussions. " +
		"You always reply with strict, valid JSON and nothing else."
	raw, err := client.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("planning completion: %w", err)
	}
	var d draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decode planning JSON: %w (raw: %s)", err, truncate(string(raw), 240))
	}
	if d.Title == "" || len(d.Discussants) < 2 {
		return nil, fmt.Errorf("planner returned an incomplete draft")
	}
	return &d, nil
}

// assemble turns a creative draft into a full, validated DebateTopic, filling
// the non-creative scaffolding (type, language, channel, models, defaults).
func (p *Planner) assemble(d *draft, lang, channel string, sources []config.Source) (*Result, error) {
	model := p.agentModel()
	host := config.AgentSpec{Name: d.Host.Name, Model: model}
	if host.Name == "" {
		host.Name = "Host"
	}
	discussants := make([]config.AgentSpec, 0, len(d.Discussants))
	for _, dd := range d.Discussants {
		discussants = append(discussants, config.AgentSpec{
			Name:   dd.Name,
			Model:  model,
			Aspect: dd.Aspect,
		})
	}
	topic := &config.DebateTopic{
		Title:             d.Title,
		Type:              config.ContentTypeDiscussion,
		Language:          lang,
		TotalMinutes:      30,
		SegmentMaxSeconds: 60,
		TTSProvider:       config.TTSProviderAzure,
		Resolution:        config.Resolution1080p,
		Channel:           defaultChannel(channel),
		Host:              host,
		Discussants:       discussants,
		Commander:         config.AgentSpec{Model: model},
		Storage:           config.StoragePlaintext,
		Background:        d.Background,
		Sources:           sources,
	}
	if err := config.ValidateTopic(topic); err != nil {
		return nil, fmt.Errorf("planner produced an invalid script: %w", err)
	}
	md, err := topic.RenderMarkdown()
	if err != nil {
		return nil, fmt.Errorf("render planned script: %w", err)
	}
	return &Result{Script: topic, Markdown: md, Sources: sources, Researched: len(sources) > 0}, nil
}

func (p *Planner) scriptModel() string {
	if p.env.ScenePlannerModel != "" {
		return p.env.ScenePlannerModel
	}
	return p.env.HostModel
}

// agentModel is the model assigned to the planned agents. Uses the host model
// so the generated roster runs on a sensible default the user can later change.
func (p *Planner) agentModel() string {
	if p.env.HostModel != "" {
		return p.env.HostModel
	}
	return p.scriptModel()
}

func defaultChannel(c string) string {
	if strings.TrimSpace(c) == "" {
		return "default"
	}
	return c
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
