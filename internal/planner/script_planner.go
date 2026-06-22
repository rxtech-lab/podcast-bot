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

// Attachment is a user-uploaded reference. Documents carry markdown converted
// by markitdown; images carry a URL and are sent to the model as image parts.
type Attachment struct {
	Filename string `json:"filename"`
	Markdown string `json:"markdown,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// PlanRequest is the input to Generate.
type PlanRequest struct {
	Type        string `json:"type"`
	Topic       string `json:"topic"`
	Language    string `json:"language"`
	Channel     string `json:"channel"`
	Discussants int    `json:"discussants"`
	// Research asks the planner to ground the draft in live web sources via
	// Firecrawl search. Any links pasted into the topic are scraped regardless
	// of this flag (when Firecrawl is configured).
	Research bool `json:"research"`
	// Attachments are user-uploaded files to ground the plan.
	Attachments []Attachment `json:"attachments,omitempty"`
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

	// Ground the draft in real web sources. Best-effort: when Firecrawl is not
	// configured (or it fails) sources is empty and the plan reports
	// researched=false. Topic search is gated on req.Research; any links the
	// user pasted into the topic are always scraped when Firecrawl is set.
	var sources []config.Source
	if req.Research {
		sources, _ = p.research(ctx, req.Topic)
	}
	if pasted := p.scrapeURLs(ctx, extractURLs(req.Topic)); len(pasted) > 0 {
		sources = mergeSources(sources, pasted)
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
Each discussant must have a DISTINCT aspect (e.g. economic, ethical, technical, historical, cultural). Use %d discussants.%s%s`,
		req.Topic, lang, n, n, sourcesPrompt(sources), attachmentsPrompt(req.Attachments))

	d, err := p.draftJSON(ctx, user, req.Attachments)
	if err != nil {
		return nil, err
	}
	return p.assemble(d, lang, req.Channel, sources)
}

// Improve revises an existing script per a free-text instruction. Attachments
// are user-uploaded reference files to ground the revision;
// pass nil when there are none.
func (p *Planner) Improve(ctx context.Context, prev *config.DebateTopic, instruction string, attachments []Attachment) (*Result, error) {
	if prev == nil {
		return nil, fmt.Errorf("previousScript is required")
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("instruction is required")
	}
	// Carry the prior sources forward, then fold in any links the user pasted
	// into the instruction so a chat-edit can cite a freshly mentioned page.
	sources := prev.Sources
	if pasted := p.scrapeURLs(ctx, extractURLs(instruction)); len(pasted) > 0 {
		sources = mergeSources(sources, pasted)
	}
	return p.revise(ctx, prev, instruction, sources, attachments)
}

// AddSources scrapes the given URLs via Firecrawl, merges them into the plan's
// existing sources, and re-runs the planner so the background incorporates the
// newly added references. This backs "add a link, save, and re-research".
func (p *Planner) AddSources(ctx context.Context, prev *config.DebateTopic, urls []string) (*Result, error) {
	if prev == nil {
		return nil, fmt.Errorf("previousScript is required")
	}
	scraped := p.scrapeURLs(ctx, urls)
	if len(scraped) == 0 {
		return nil, fmt.Errorf("none of the added links could be read")
	}
	sources := mergeSources(prev.Sources, scraped)
	instruction := "Incorporate the substance of the newly added sources into the background and, " +
		"where relevant, the discussants' angles. Keep the existing roster and structure unless the " +
		"new material clearly calls for an adjustment."
	return p.revise(ctx, prev, instruction, sources, nil)
}

// revise runs one improvement pass against prev with the given instruction,
// grounding sources, and attachments, then assembles the result carrying the
// merged sources forward.
func (p *Planner) revise(ctx context.Context, prev *config.DebateTopic, instruction string, sources []config.Source, attachments []Attachment) (*Result, error) {
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

Return STRICT JSON with the SAME shape (title, background, host{name}, discussants[]{name, aspect}). Keep the language as %s. Preserve good parts; change only what the instruction asks for.%s%s`,
		string(prevJSON), instruction, lang, sourcesPrompt(sources), attachmentsPrompt(attachments))

	d, err := p.draftJSON(ctx, user, attachments)
	if err != nil {
		return nil, err
	}
	// Preserve the prior channel and carry the merged sources forward so an
	// edit never drops the references.
	return p.assemble(d, lang, prev.Channel, sources)
}

// attachmentsPrompt renders uploaded files as a compact block so the LLM
// grounds the plan in the user's documents.
func attachmentsPrompt(attachments []Attachment) string {
	var rendered []Attachment
	for _, a := range attachments {
		if a.isImage() {
			continue
		}
		if strings.TrimSpace(a.Markdown) == "" {
			continue
		}
		rendered = append(rendered, a)
	}
	if len(rendered) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nThe user uploaded these reference documents; ground the discussion in their content:\n")
	for i, a := range rendered {
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = fmt.Sprintf("document %d", i+1)
		}
		fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", name, truncate(strings.TrimSpace(a.Markdown), 6000))
	}
	return sb.String()
}

func attachmentInputParts(user string, attachments []Attachment) []llm.InputPart {
	parts := []llm.InputPart{{Text: user}}
	images := imageAttachments(attachments)
	if len(images) == 0 {
		return parts
	}
	parts = append(parts, llm.InputPart{Text: "\n\nThe user uploaded these images. Inspect them directly and ground the discussion in visible details when relevant."})
	for i, image := range images {
		name := strings.TrimSpace(image.Filename)
		if name == "" {
			name = fmt.Sprintf("image %d", i+1)
		}
		parts = append(parts,
			llm.InputPart{Text: "\n\n--- " + name + " ---"},
			llm.InputPart{ImageURL: image.URL, Detail: "auto"},
		)
	}
	return parts
}

func imageAttachments(attachments []Attachment) []Attachment {
	var images []Attachment
	for _, a := range attachments {
		if a.isImage() && strings.TrimSpace(a.URL) != "" {
			images = append(images, a)
		}
	}
	return images
}

func (a Attachment) isImage() bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.MIMEType)), "image/") {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(a.Filename))
	switch {
	case strings.HasSuffix(name, ".png"),
		strings.HasSuffix(name, ".jpg"),
		strings.HasSuffix(name, ".jpeg"),
		strings.HasSuffix(name, ".gif"),
		strings.HasSuffix(name, ".webp"),
		strings.HasSuffix(name, ".heic"),
		strings.HasSuffix(name, ".heif"):
		return true
	default:
		return false
	}
}

func discussantViews(specs []config.AgentSpec) []map[string]string {
	out := make([]map[string]string, len(specs))
	for i, s := range specs {
		out[i] = map[string]string{"name": s.Name, "aspect": s.Aspect}
	}
	return out
}

// draftJSON runs one strict-JSON completion and decodes the creative payload.
func (p *Planner) draftJSON(ctx context.Context, user string, attachments []Attachment) (*draft, error) {
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	const system = "You are a producer who designs balanced, well-cast panel discussions. " +
		"You always reply with strict, valid JSON and nothing else."
	raw, err := client.JSONParts(ctx, system, attachmentInputParts(user, attachments))
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
