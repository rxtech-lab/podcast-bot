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

// ProgressEvent is a coarse, human-readable step emitted while the planning
// agent loop runs (searching the web, reading a URL, writing the plan). It backs
// the streaming plan endpoints so clients can show live progress.
type ProgressEvent struct {
	Phase string `json:"phase"` // "search" | "read" | "sources" | "writing"
	Text  string `json:"text"`
}

// Planner drafts and revises discussion scripts with a single LLM.
type Planner struct {
	env           *config.Env
	onProgress    func(ProgressEvent)
	usageRecorder func(llm.Usage)
}

// New builds a Planner from engine env. Returns an error when env is nil so
// the HTTP layer can 503 cleanly rather than panic.
func New(env *config.Env) (*Planner, error) {
	if env == nil {
		return nil, fmt.Errorf("planner requires engine env")
	}
	return &Planner{env: env}, nil
}

// WithProgress registers a callback invoked synchronously, on the calling
// goroutine, as the planning agent loop makes progress. A Planner is created
// per request, so this is request-scoped. Returns the receiver for chaining.
func (p *Planner) WithProgress(fn func(ProgressEvent)) *Planner {
	p.onProgress = fn
	return p
}

// WithUsageRecorder registers a callback invoked for every LLM call the planner
// makes, so the caller can meter and bill the planning phase. A Planner is
// created per request, so this is request-scoped. Returns the receiver for
// chaining. The recorder must be safe for concurrent use.
func (p *Planner) WithUsageRecorder(fn func(llm.Usage)) *Planner {
	p.usageRecorder = fn
	return p
}

func (p *Planner) emit(phase, text string) {
	if p.onProgress != nil {
		p.onProgress(ProgressEvent{Phase: phase, Text: text})
	}
}

// Attachment is a user-uploaded reference. Documents carry markdown converted
// by markitdown; images carry a URL and are sent to the model as image parts.
type Attachment struct {
	Filename string `json:"filename"`
	Markdown string `json:"markdown,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// PodcastReference is a previously generated podcast the user wants the planner
// to use as context for a follow-up episode. Context is server-populated and not
// accepted from clients.
type PodcastReference struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Topic   string `json:"topic"`
	Context string `json:"-"`
}

// PlanRequest is the input to Generate.
type PlanRequest struct {
	Type        string `json:"type"`
	Topic       string `json:"topic"`
	Language    string `json:"language"`
	Channel     string `json:"channel"`
	Discussants int    `json:"discussants"`
	Template    string `json:"template,omitempty"`
	// Research asks the planner to ground the draft in live web sources via
	// Firecrawl search. Any links pasted into the topic are scraped regardless
	// of this flag (when Firecrawl is configured).
	Research bool `json:"research"`
	// Attachments are user-uploaded files to ground the plan.
	Attachments []Attachment `json:"attachments,omitempty"`
	// Reference is an existing podcast to build on. The server fills Context
	// after validating the referenced podcast is visible to the requester.
	Reference *PodcastReference `json:"reference,omitempty"`
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
Each discussant must have a DISTINCT aspect (e.g. economic, ethical, technical, historical, cultural). Use %d discussants.%s%s%s`,
		req.Topic, lang, n, n, referencePrompt(req.Reference), planningRequirementsPrompt(req.Research, extractURLs(req.Topic)), attachmentsPrompt(req.Attachments))

	d, sources, err := p.draftJSON(ctx, user, req.Attachments, planningAgentOptions{
		ResearchRequired: req.Research,
		RequiredURLs:     extractURLs(req.Topic),
	})
	if err != nil {
		return nil, err
	}
	return p.assemble(d, lang, req.Channel, sources)
}

// Improve revises an existing script per a free-text instruction. pastMessages
// are the user's prior revision requests (oldest first), passed so the planner
// can keep the running intent of the conversation in view; pass nil when there
// are none. Attachments are user-uploaded reference files to ground the
// revision; pass nil when there are none.
func (p *Planner) Improve(ctx context.Context, prev *config.DebateTopic, instruction string, pastMessages []string, attachments []Attachment) (*Result, error) {
	if prev == nil {
		return nil, fmt.Errorf("previousScript is required")
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("instruction is required")
	}
	return p.revise(ctx, prev, instruction, pastMessages, prev.Sources, extractURLs(instruction), attachments, false)
}

// AddSources crawls the given URLs via Firecrawl, merges them into the plan's
// existing sources, and re-runs the planner so the background incorporates the
// newly added references. This backs "add a link, save, and re-research".
func (p *Planner) AddSources(ctx context.Context, prev *config.DebateTopic, urls []string) (*Result, error) {
	if prev == nil {
		return nil, fmt.Errorf("previousScript is required")
	}
	urls = dedupeURLs(urls)
	if len(urls) == 0 {
		return nil, fmt.Errorf("at least one url is required")
	}
	p.emit("read", "Reading added sources…")
	added := p.crawlURLs(ctx, urls)
	if len(added) == 0 {
		return nil, fmt.Errorf("none of the added links could be read")
	}
	p.emit("sources", fmt.Sprintf("Found %d added source%s", len(added), plural(len(added))))
	sources := mergeSources(prev.Sources, added)
	instruction := "Incorporate the substance of the newly added sources into the background and, " +
		"where relevant, the discussants' angles. Keep the existing roster and structure unless the " +
		"new material clearly calls for an adjustment."
	return p.revise(ctx, prev, instruction, nil, sources, nil, nil, false)
}

// revise runs one improvement pass against prev with the given instruction,
// the user's prior revision requests (pastMessages, oldest first), grounding
// sources, and attachments, then assembles the result carrying the merged
// sources forward.
func (p *Planner) revise(ctx context.Context, prev *config.DebateTopic, instruction string, pastMessages []string, existingSources []config.Source, requiredURLs []string, attachments []Attachment, requireSuccessfulURLRead bool) (*Result, error) {
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
%s
Revise it per this instruction: %s

Return STRICT JSON with the SAME shape (title, background, host{name}, discussants[]{name, aspect}). Keep the language as %s. Preserve good parts; change only what the instruction asks for.%s%s`,
		string(prevJSON), conversationPrompt(pastMessages), instruction, lang, sourcesPrompt(existingSources)+planningRequirementsPrompt(false, requiredURLs), attachmentsPrompt(attachments))

	d, sources, err := p.draftJSON(ctx, user, attachments, planningAgentOptions{
		RequiredURLs:             requiredURLs,
		ExistingSources:          existingSources,
		RequireSuccessfulURLRead: requireSuccessfulURLRead,
	})
	if err != nil {
		return nil, err
	}
	// Preserve the prior channel and carry the merged sources forward so an
	// edit never drops the references.
	return p.assemble(d, lang, prev.Channel, sources)
}

// conversationPrompt renders the user's prior revision requests (oldest first)
// so the planner keeps the running intent of the editing conversation in view
// rather than treating each instruction in isolation. Returns "" when there is
// no prior history.
func conversationPrompt(pastMessages []string) string {
	var cleaned []string
	for _, m := range pastMessages {
		if m = strings.TrimSpace(m); m != "" {
			cleaned = append(cleaned, m)
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nThe user has already asked for these revisions earlier in this conversation (oldest first):\n")
	for i, m := range cleaned {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, truncate(m, 1000))
	}
	sb.WriteString("Treat them as context for the latest request; don't undo changes they asked for unless the new instruction says so.\n")
	return sb.String()
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

func referencePrompt(ref *PodcastReference) string {
	if ref == nil {
		return ""
	}
	title := strings.TrimSpace(ref.Title)
	if title == "" {
		title = strings.TrimSpace(ref.Topic)
	}
	if title == "" {
		title = "Referenced podcast"
	}
	var sb strings.Builder
	sb.WriteString("\n\nReferenced podcast context:\n")
	sb.WriteString("The new discussion must be a follow-up to this existing podcast. Build on the old topic and arguments, avoid repeating the same episode, and focus the new plan on fresh developments, unresolved questions, or deeper next steps.\n")
	fmt.Fprintf(&sb, "\nTitle: %s\n", title)
	if topic := strings.TrimSpace(ref.Topic); topic != "" && topic != title {
		fmt.Fprintf(&sb, "Original topic: %s\n", topic)
	}
	if ctx := strings.TrimSpace(ref.Context); ctx != "" {
		fmt.Fprintf(&sb, "\nPrior podcast material:\n%s\n", truncate(ctx, 12000))
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

// draftJSON runs the planning agent loop. The model can call research tools for
// multiple rounds, but the planner accepts a result only after create_plan.
func (p *Planner) draftJSON(ctx context.Context, user string, attachments []Attachment, opts planningAgentOptions) (*draft, []config.Source, error) {
	d, sources, err := p.runPlanningAgent(ctx, user, attachments, opts)
	if err != nil {
		return nil, nil, err
	}
	return d, sources, nil
}

// assemble turns a creative draft into a full, validated DebateTopic, filling
// the non-creative scaffolding (type, language, channel, models, defaults).
func (p *Planner) assemble(d *draft, lang, channel string, sources []config.Source) (*Result, error) {
	return p.assembleWithModel(d, lang, channel, sources, p.agentModel())
}

func (p *Planner) assembleWithModel(d *draft, lang, channel string, sources []config.Source, model string) (*Result, error) {
	if strings.TrimSpace(model) == "" {
		model = p.agentModel()
	}
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
