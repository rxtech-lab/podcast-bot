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

// audioBookMaxChapters is a sanity bound on how many chapters a plan may hold
// (protects prompt size against LLM runaways). It is NOT the per-generation
// limit: generation batches are capped separately at the HTTP layer
// (audioBookMaxBatchChapters in internal/server).
const audioBookMaxChapters = 40

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

// Attachment is a user-uploaded reference. Documents carry complete markdown
// converted by markitdown; service pagination is resolved before this model is
// constructed. Images carry a URL and are sent to the model as image parts.
// Key is the S3 object key of the upload; the server uses it to re-sign a fresh
// image URL whenever a persisted conversation turn is replayed to the model
// (presigned URLs expire, keys don't).
type Attachment struct {
	Filename string `json:"filename"`
	Markdown string `json:"markdown,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Key      string `json:"key,omitempty"`
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

type audioBookDraft struct {
	Title          string `json:"title"`
	Style          string `json:"style"`
	OverallSummary string `json:"overall_summary"`
	Narrator       struct {
		Name   string `json:"name"`
		Gender string `json:"gender"`
	} `json:"narrator"`
	Speakers []struct {
		Name        string `json:"name"`
		Gender      string `json:"gender"`
		Description string `json:"description"`
	} `json:"speakers"`
	Chapters []audioBookDraftChapter `json:"chapters"`
}

// audioBookDraftChapter is one chapter in the LLM's audiobook draft.
// StartIndex references an entry in the server-provided source marker catalog
// (0 = unset); StartMarker is a verbatim line copied from the source, used
// when no catalog entry fits. Both are optional so legacy drafts still decode.
type audioBookDraftChapter struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Mode        string   `json:"mode"`
	Speakers    []string `json:"speakers"`
	StartIndex  int      `json:"start_index,omitempty"`
	StartMarker string   `json:"start_marker,omitempty"`
}

// Generate drafts a brand-new script from a topic.
func (p *Planner) Generate(ctx context.Context, req PlanRequest) (*Result, error) {
	switch strings.TrimSpace(req.Type) {
	case "", config.ContentTypeDiscussion:
		return p.generateDiscussion(ctx, req)
	case config.ContentTypeAudioBook:
		return p.generateAudioBook(ctx, req)
	case config.ContentTypeNews:
		return p.generateNews(ctx, req)
	default:
		return nil, fmt.Errorf("only %q, %q, or %q planning is supported (got %q)", config.ContentTypeDiscussion, config.ContentTypeAudioBook, config.ContentTypeNews, req.Type)
	}
}

func (p *Planner) generateDiscussion(ctx context.Context, req PlanRequest) (*Result, error) {
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
  "title": "a concise, engaging title — fewer than 10 words",
  "background": "2-4 paragraphs of neutral background framing the discussion, in the requested language",
  "host": { "name": "moderator's display name" },
  "discussants": [ { "name": "display name", "aspect": "the distinct angle/perspective this person argues from" } ]
}
Each discussant must have a DISTINCT aspect (e.g. economic, ethical, technical, historical, cultural). Use %d discussants.%s%s%s%s`,
		req.Topic, lang, n, n, templatePrompt(req.Template), referencePrompt(req.Reference), planningRequirementsPrompt(req.Research, extractURLs(req.Topic), req.Template), attachmentsPrompt(req.Attachments))

	d, sources, err := p.draftJSON(ctx, user, req.Attachments, planningAgentOptions{
		ResearchRequired: req.Research,
		RequiredURLs:     extractURLs(req.Topic),
		Template:         req.Template,
	})
	if err != nil {
		return nil, err
	}
	return p.assemble(d, lang, req.Channel, sources)
}

func (p *Planner) generateAudioBook(ctx context.Context, req PlanRequest) (*Result, error) {
	if strings.TrimSpace(req.Topic) == "" {
		return nil, fmt.Errorf("topic is required")
	}
	lang := req.Language
	if lang == "" {
		lang = "en-US"
	}
	user := fmt.Sprintf(`Design a narrated audiobook plan from the user's source material.

Topic or instruction: %s
Language for all names and text: %s

Return STRICT JSON with this exact shape:
{
  "title": "a concise audiobook title — fewer than 10 words",
  "style": "news" | "conversational" | "audiobook" | "podcast" | "meeting",
  "overall_summary": "Compact Markdown summary of the source and the proposed audiobook direction. Do not include the chapter list here.",
  "narrator": { "name": "narrator/main host display name", "gender": "REQUIRED: exactly \"male\" or \"female\"" },
  "speakers": [ { "name": "recurring speaking character or guest voice from the book/source; never repeat the narrator/main host", "gender": "REQUIRED: exactly \"male\" or \"female\"", "description": "a concrete voice-casting brief for this speaker" } ],
  "chapters": [ { "title": "chapter title without a Chapter 1 prefix", "summary": "one or two concise sentences describing what this chapter should narrate", "mode": "narration" | "dialogue", "speakers": ["additional guest/character speakers who talk in this chapter; do not list the narrator"] } ]
}

Use dedicated chapter objects instead of embedding chapters in overall_summary. Create one chapter per natural chapter or major section of the source. Prefer 3-5 chapters for short sources; long books may have as many chapters as the source genuinely has, up to %d. Keep each chapter summary to one or two concise sentences.
Style direction: choose one style from news, conversational, audiobook, podcast, or meeting. Record the user's requested style when they ask for one, or infer the best fit from the source. If the user asks for people talking, two people talking, an interview, Q&A, a conversation, or one main speaker with others asking questions, choose "conversational". In conversational, podcast, meeting, and news styles the piece is a genuine multi-voice conversation: the narrator/main host anchors it, but the other speakers actively talk in their own turns — asking, answering, clarifying, challenging, adding — not merely being quoted by the narrator. Do not add the narrator/main host again to "speakers". In audiobook style, keep the narrator primary and use other speakers only for characters or quoted voices.
Multi-speaker direction:
- For "conversational", "podcast", "meeting", and "news" styles you MUST define at least one additional speaker, and MOST chapters (at least all but one) MUST use "mode": "dialogue" with those speakers listed in the chapter "speakers". This is not conditional on the source already being a conversation — reframe prose sources into a back-and-forth discussion between the narrator and the guest(s). A conversational plan whose chapters are all "narration" is wrong.
- For "audiobook" style, keep chapters mostly "narration"; only mark a chapter "dialogue" when the source contains actual character conversation or a Q&A exchange.
- A "dialogue" chapter is a real exchange where the narrator/main host and the listed guest speakers each get several turns — never a monologue that quotes the others. Leave "speakers" empty only for "narration" chapters the narrator reads alone.
- Only reference speaker names that appear in the top-level "speakers" list, and never list the narrator in chapter "speakers".
Voice casting direction:
- Before writing chapters, identify the source cast: named characters, interviewees, quoted speakers, and recurring point-of-view voices that speak or are directly quoted in the book/source.
- Include most of the book/source's speaking cast in the top-level "speakers" list: all central and recurring voices plus chapter-critical one-off voices. Omit only unnamed, background, or truly incidental speakers. Do not shrink a real book cast down to one generic guest or narrator-only plan.
- Create one dedicated entry in "speakers" for each included character or guest who speaks anywhere in the audiobook. If a chapter's dialogue involves a character, that character MUST have their own "speakers" entry — never fold two characters into one shared voice.
- Each included speaker should be referenced in at least one chapter's "speakers" list when that character or guest speaks in that chapter.
- Every speaker entry MUST include "gender", exactly "male" or "female". Infer it from the source material; if ambiguous, pick the most plausible gender and keep it consistent across chapters. Never leave gender empty — a female character must be cast with a female voice and a male character with a male voice.
- The narrator MUST also include "gender" ("male" or "female").
- Make each speaker "description" a concrete voice-casting brief: approximate age, vocal tone and register, personality, and speaking energy (e.g. "elderly male mentor, deep gravelly voice, slow and warm").
For long uploaded documents, use only the bounded source digests supplied below. Do not ask for or require the full document in the prompt. Keep chapter summaries concise while still giving the generation stage enough direction to narrate each chapter in order.%s%s`,
		req.Topic, lang, audioBookMaxChapters, templatePrompt(req.Template), audioBookAttachmentsPrompt(req.Attachments))

	raw, sources, err := p.draftAudioBookJSON(ctx, user, planningAgentOptions{
		ResearchRequired: req.Research,
		RequiredURLs:     extractURLs(req.Topic),
		Template:         req.Template,
	})
	if err != nil {
		return nil, err
	}
	return p.assembleAudioBookWithModel(raw, lang, req.Channel, sources, p.agentModel())
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
		string(prevJSON), conversationPrompt(pastMessages), instruction, lang, sourcesPrompt(existingSources)+planningRequirementsPrompt(false, requiredURLs, ""), attachmentsPrompt(attachments))

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
		if strings.TrimSpace(a.Markdown) == "" && strings.TrimSpace(a.URL) == "" {
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
		markdown := strings.TrimSpace(a.Markdown)
		if markdown != "" {
			fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", name, truncate(markdown, 6000))
			continue
		}
		fmt.Fprintf(&sb, "\n--- %s ---\nShared webpage: %s\nRead this URL before writing the plan.\n", name, strings.TrimSpace(a.URL))
	}
	return sb.String()
}

func attachmentsPromptForType(contentType string, attachments []Attachment) string {
	if contentType == config.ContentTypeAudioBook {
		return audioBookAttachmentsPrompt(attachments)
	}
	return attachmentsPrompt(attachments)
}

func audioBookAttachmentsPrompt(attachments []Attachment) string {
	source := AudioBookSourceFromAttachments(attachments)
	note := unreadableAttachmentsNote(attachments)
	if source == "" {
		return note
	}
	var sb strings.Builder
	sb.WriteString("\n\nUploaded source digest for audiobook planning. The full converted documents remain server-side; use this bounded digest to avoid overloading context, and anchor every chapter's start_index to the Source markers catalog below so the server can slice the real text at your boundaries:\n\n")
	sb.WriteString(audioBookSourceDigest(source))
	sb.WriteByte('\n')
	sb.WriteString(note)
	return sb.String()
}

// unreadableAttachmentsNote covers document attachments whose text never
// reached the server (failed/empty conversion, or a URL-only reference the
// audiobook pipeline cannot fetch). Without it the model has no idea an
// attachment existed and answers as if nothing was uploaded.
func unreadableAttachmentsNote(attachments []Attachment) string {
	var names []string
	for i, a := range attachments {
		if a.isImage() || a.isAudio() || strings.TrimSpace(a.Markdown) != "" {
			continue
		}
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = fmt.Sprintf("document %d", i+1)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	return "\n\nThe user attached " + strings.Join(names, ", ") +
		", but no readable text reached the server for it. Tell the user the attachment could not be read and ask them to re-upload a text-based copy (or paste the content) before planning chapters — do not pretend the source is available.\n"
}

// audioBookSourceDigest renders the bounded planning digest over the exact
// concatenated source the splitter will later slice: total length, the indexed
// Source markers catalog, and a short orientation excerpt.
func audioBookSourceDigest(source string) string {
	text := strings.TrimSpace(source)
	if text == "" {
		return "(empty document)"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Converted length: %d characters.\n", len(text))
	if catalog := renderSourceMarkers(audioBookSourceMarkers(source)); catalog != "" {
		sb.WriteString(catalog)
	}
	cleaned := strings.Join(strings.Fields(text), " ")
	if cleaned != "" {
		sb.WriteString("Bounded excerpt for orientation:\n")
		sb.WriteString(truncate(cleaned, 2400))
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func templatePrompt(template string) string {
	instructions := TemplateInstructions(template)
	if instructions == "" {
		return ""
	}
	return "\n\nTemplate instructions:\n" + instructions
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

// UserTurnMessage builds the LLM user message for one persisted conversation
// turn. Image attachments become multimodal image parts so the model can
// inspect them on every rebuild of the history; turns without images stay
// plain-text Content (identical to the previous behavior).
func UserTurnMessage(text string, attachments []Attachment) llm.Message {
	if len(imageAttachments(attachments)) == 0 {
		return llm.Message{Role: llm.RoleUser, Content: text}
	}
	return llm.Message{Role: llm.RoleUser, Parts: attachmentInputParts(text, attachments)}
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

// isAudio marks voice-message/audio attachments, which are replayed for
// humans and never carry converted text.
func (a Attachment) isAudio() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.MIMEType)), "audio/")
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
	raw, sources, err := p.runPlanningAgent(ctx, user, attachments, opts)
	if err != nil {
		return nil, nil, err
	}
	d, err := decodeDraft(raw)
	if err != nil {
		return nil, nil, err
	}
	return d, sources, nil
}

// draftNewsJSON runs the same planning agent loop with the news create_plan
// schema + decoder.
func (p *Planner) draftNewsJSON(ctx context.Context, user string, attachments []Attachment, opts planningAgentOptions) (*newsDraft, []config.Source, error) {
	opts.ContentType = config.ContentTypeNews
	raw, sources, err := p.runPlanningAgent(ctx, user, attachments, opts)
	if err != nil {
		return nil, nil, err
	}
	d, err := decodeNewsDraft(raw)
	if err != nil {
		return nil, nil, err
	}
	return d, sources, nil
}

func (p *Planner) draftAudioBookJSON(ctx context.Context, user string, opts planningAgentOptions) (*audioBookDraft, []config.Source, error) {
	client := llm.New(p.env.OpenAIBaseURL, p.env.OpenAIKey, p.scriptModel())
	if p.usageRecorder != nil {
		client = client.
			WithUsageRecorder(p.usageRecorder).
			WithPricing(p.env.LLMInputCostPerMillion, p.env.LLMOutputCostPerMillion)
	}
	p.emit("thinking", "Outlining the audiobook…")
	stream, err := client.Stream(ctx, `You are an audiobook planning agent. Return one strict JSON object matching the user's requested schema. Do not wrap it in markdown.`, []llm.Message{{Role: llm.RoleUser, Content: user}}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("audiobook planning: %w", err)
	}
	var text strings.Builder
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		text.WriteString(d.TextChunk)
	}
	if err := stream.Err(); err != nil {
		return nil, nil, fmt.Errorf("audiobook planning: %w", err)
	}
	raw := strings.TrimSpace(text.String())
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var d audioBookDraft
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &d); err != nil {
		return nil, nil, fmt.Errorf("decode audiobook plan: %w", err)
	}
	if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.OverallSummary) == "" || len(d.Chapters) == 0 {
		return nil, nil, fmt.Errorf("audiobook plan returned an incomplete draft")
	}
	if strings.TrimSpace(d.Style) == "" {
		d.Style = inferAudioBookStyle(&d)
	}
	return &d, nil, nil
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
	totalMinutes := 30
	if p.env.E2EMode {
		// Keep E2E podcasts tiny: a sub-90s target makes the discussion planner
		// transition straight from openings to closings, so generation finishes in
		// a handful of fake turns instead of dozens.
		totalMinutes = 1
	}
	topic := &config.DebateTopic{
		Title:             d.Title,
		Type:              config.ContentTypeDiscussion,
		Language:          lang,
		TotalMinutes:      totalMinutes,
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

func (p *Planner) assembleAudioBookWithModel(d *audioBookDraft, lang, channel string, sources []config.Source, model string) (*Result, error) {
	if strings.TrimSpace(model) == "" {
		model = p.agentModel()
	}
	narrator := config.AgentSpec{Name: d.Narrator.Name, Model: model, Gender: normalizeSpeakerGender(d.Narrator.Gender)}
	if strings.TrimSpace(narrator.Name) == "" {
		narrator.Name = "Narrator"
	}
	narrator.Name = strings.TrimSpace(narrator.Name)
	narratorKey := normalizedSpeakerName(narrator.Name)
	speakers := make([]config.AudioBookSpeaker, 0, len(d.Speakers))
	speakerNames := make(map[string]string, len(d.Speakers))
	for _, s := range d.Speakers {
		name := strings.TrimSpace(s.Name)
		key := normalizedSpeakerName(name)
		if name == "" || key == narratorKey || speakerNames[key] != "" {
			continue
		}
		speakers = append(speakers, config.AudioBookSpeaker{
			Name:        name,
			Gender:      normalizeSpeakerGender(s.Gender),
			Description: strings.TrimSpace(s.Description),
			Model:       model,
		})
		speakerNames[key] = name
	}
	chapters := make([]config.AudioBookChapter, 0, len(d.Chapters))
	for _, ch := range d.Chapters {
		if strings.TrimSpace(ch.Title) == "" || strings.TrimSpace(ch.Summary) == "" {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(ch.Mode))
		// Keep only known speaker names; a dialogue chapter needs at least
		// one valid speaker, otherwise it collapses back to narration.
		var chSpeakers []string
		chSpeakerSeen := make(map[string]bool, len(ch.Speakers))
		for _, name := range ch.Speakers {
			key := normalizedSpeakerName(name)
			if key == "" || key == narratorKey || chSpeakerSeen[key] {
				continue
			}
			if canonical := speakerNames[key]; canonical != "" {
				chSpeakers = append(chSpeakers, canonical)
				chSpeakerSeen[key] = true
			}
		}
		if mode == config.AudioBookModeDialogue && len(chSpeakers) == 0 {
			mode = config.AudioBookModeNarration
		}
		if mode != config.AudioBookModeDialogue {
			mode = config.AudioBookModeNarration
			chSpeakers = nil
		}
		chapters = append(chapters, config.AudioBookChapter{
			Title:    strings.TrimSpace(ch.Title),
			Summary:  strings.TrimSpace(ch.Summary),
			Mode:     mode,
			Speakers: chSpeakers,
		})
		if len(chapters) == audioBookMaxChapters {
			break
		}
	}
	// TotalMinutes is display-only for audiobooks: generation runs on a derived
	// batch script whose minutes are recomputed from the batch's chapter count
	// (see internal/server deriveAudioBookBatchScript).
	totalMinutes := len(chapters) * 8
	if totalMinutes < 15 {
		totalMinutes = 15
	}
	if p.env.E2EMode {
		totalMinutes = 1
	}
	title := strings.TrimSpace(d.Title)
	if len(chapters) == 1 {
		title = strings.TrimSpace(chapters[0].Title)
	}
	topic := &config.DebateTopic{
		Title:             title,
		Type:              config.ContentTypeAudioBook,
		Language:          lang,
		TotalMinutes:      totalMinutes,
		SegmentMaxSeconds: 60,
		TTSProvider:       config.TTSProviderAzure,
		Resolution:        config.Resolution1080p,
		Channel:           defaultChannel(channel),
		AudioBookHost:     narrator,
		AudioBookStyle:    normalizeAudioBookStyle(d.Style),
		AudioBookSpeakers: speakers,
		AudioBookChapters: chapters,
		Background:        strings.TrimSpace(d.OverallSummary),
		Surface:           renderAudioBookOutline(d.OverallSummary, chapters, narrator.Name),
		Sources:           sources,
	}
	if err := config.ValidateTopic(topic); err != nil {
		return nil, fmt.Errorf("planner produced an invalid audiobook script: %w", err)
	}
	md, err := topic.RenderMarkdown()
	if err != nil {
		return nil, fmt.Errorf("render planned audiobook script: %w", err)
	}
	return &Result{Script: topic, Markdown: md, Sources: sources, Researched: len(sources) > 0}, nil
}

func normalizeAudioBookStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case config.AudioBookStyleNews:
		return config.AudioBookStyleNews
	case config.AudioBookStyleConversational:
		return config.AudioBookStyleConversational
	case config.AudioBookStylePodcast:
		return config.AudioBookStylePodcast
	case config.AudioBookStyleMeeting:
		return config.AudioBookStyleMeeting
	case "audio_book", "audio-book", config.AudioBookStyleAudioBook:
		return config.AudioBookStyleAudioBook
	default:
		return config.AudioBookStyleAudioBook
	}
}

func normalizedSpeakerName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

// normalizeSpeakerGender maps the planner's free-form gender onto the
// lowercase "male"/"female" the voice picker matches against Azure's voice
// genders (case-insensitively). Anything unrecognisable becomes "" so the
// picker falls back to inferring gender from the speaker's name.
func normalizeSpeakerGender(gender string) string {
	switch strings.ToLower(strings.TrimSpace(gender)) {
	case "male", "m", "man", "boy":
		return "male"
	case "female", "f", "woman", "girl":
		return "female"
	default:
		return ""
	}
}

func inferAudioBookStyle(d *audioBookDraft) string {
	if d == nil {
		return config.AudioBookStyleAudioBook
	}
	if len(d.Speakers) >= 2 {
		return config.AudioBookStyleConversational
	}
	for _, ch := range d.Chapters {
		if strings.EqualFold(strings.TrimSpace(ch.Mode), config.AudioBookModeDialogue) || len(ch.Speakers) > 0 {
			return config.AudioBookStyleConversational
		}
	}
	return config.AudioBookStyleAudioBook
}

func renderAudioBookOutline(summary string, chapters []config.AudioBookChapter, narratorName string) string {
	return RenderAudioBookOutlineIndexed(summary, chapters, nil, narratorName)
}

// RenderAudioBookOutlineIndexed renders the audiobook outline with explicit
// global chapter numbers. numbers[i] is the 1-based position of chapters[i] in
// the full plan; pass nil to number sequentially from 1. Batch generation uses
// this so a batch covering chapters 6-8 still narrates "Chapter 6".
func RenderAudioBookOutlineIndexed(summary string, chapters []config.AudioBookChapter, numbers []int, narratorName string) string {
	var sb strings.Builder
	sb.WriteString("# Audiobook Outline\n\n")
	if strings.TrimSpace(summary) != "" {
		// h3, not h2: this outline lives inside the plan's `## Surface`
		// section, and parseSections treats any `## ` line as a section
		// boundary — an h2 here truncates Surface on the script.md
		// round-trip.
		sb.WriteString("### Overall Summary\n\n")
		sb.WriteString(strings.TrimSpace(summary))
		sb.WriteString("\n\n")
	}
	for i, ch := range chapters {
		number := i + 1
		if i < len(numbers) {
			number = numbers[i]
		}
		fmt.Fprintf(&sb, "### Chapter %d: %s\n\n", number, strings.TrimSpace(ch.Title))
		if ch.Mode == config.AudioBookModeDialogue && len(ch.Speakers) > 0 {
			mainSpeaker := strings.TrimSpace(narratorName)
			if mainSpeaker == "" {
				mainSpeaker = "the narrator"
			}
			fmt.Fprintf(&sb, "_Dialogue chapter — main speaker: %s; guest speakers: %s_\n\n", mainSpeaker, strings.Join(ch.Speakers, ", "))
		}
		fmt.Fprintf(&sb, "%s\n\n", strings.TrimSpace(ch.Summary))
	}
	return strings.TrimSpace(sb.String())
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
