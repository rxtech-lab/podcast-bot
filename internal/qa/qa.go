// Package qa implements the podcast Q&A / global chat agent: a lean
// conversational loop (modeled on the planner's conversation loop) whose
// tools retrieve from the vectorized podcast content instead of planning.
// The server layer owns persistence, billing, and streaming; this package
// owns the model loop, the tool contract, and history compaction.
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
)

// maxTurnRounds caps the assistant↔tool ping-pong within one turn.
const maxTurnRounds = 8

// Scope selects the tool set and system prompt.
const (
	ScopePodcast = "podcast"
	ScopeGlobal  = "global"
)

// ConvEventKind identifies what a ConvEvent carries. The names mirror the
// planner's conversation events so the server translation layer stays uniform.
type ConvEventKind string

const (
	ConvText       ConvEventKind = "text"
	ConvToolStart  ConvEventKind = "tool_start"
	ConvToolDelta  ConvEventKind = "tool_delta"
	ConvAssistant  ConvEventKind = "assistant"
	ConvToolCall   ConvEventKind = "tool_call"
	ConvToolResult ConvEventKind = "tool_result"
	// ConvCard is a show_* tool producing a dedicated client card
	// (podcast / transcript / sources) alongside its tool result.
	ConvCard ConvEventKind = "card"
)

// ConvEvent is the single event type the loop emits to its sink.
type ConvEvent struct {
	Kind ConvEventKind

	Text       string
	Calls      []llm.ToolCall
	ToolName   string
	ToolCallID string
	Call       llm.ToolCall

	Output  string
	IsError bool

	// Card payload for ConvCard events.
	Card *Card
}

// Card kinds rendered as dedicated views in the chat.
const (
	CardPodcast           = "podcast" // legacy persisted single-podcast card
	CardPodcasts          = "podcasts"
	CardPodcastHighlights = "podcast_highlights"
	CardHighlightLines    = "highlight_lines"
	CardTranscript        = "transcript"
	CardSources           = "sources"
	CardMindmap           = "mindmap"
	CardPPT               = "ppt"
)

// Card is the structured payload behind a show_* tool call.
type Card struct {
	Kind       string                  `json:"kind"`
	Podcast    *PodcastInfo            `json:"podcast,omitempty"`
	Podcasts   []PodcastInfo           `json:"podcasts,omitempty"`
	Highlights []PodcastHighlightGroup `json:"highlights,omitempty"`
	Transcript *TranscriptSlice        `json:"transcript,omitempty"`
	Sources    []SourceInfo            `json:"sources,omitempty"`
	Document   *DocumentInfo           `json:"document,omitempty"`
}

// CoverInfo is the presentation-safe cover payload used by podcast cards.
type CoverInfo struct {
	Type          string `json:"type,omitempty"`
	ImageURL      string `json:"image_url,omitempty"`
	GradientStart string `json:"gradient_start,omitempty"`
	GradientEnd   string `json:"gradient_end,omitempty"`
}

// PodcastInfo is the podcast identity surfaced to the model and the client.
type PodcastInfo struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Topic           string     `json:"topic,omitempty"`
	Status          string     `json:"status,omitempty"`
	Language        string     `json:"language,omitempty"`
	DurationSeconds float64    `json:"duration_seconds,omitempty"`
	CreatedAt       time.Time  `json:"created_at,omitempty"`
	Cover           *CoverInfo `json:"cover,omitempty"`
}

// ContentHit is one retrieved content chunk.
type ContentHit struct {
	DiscussionID string
	PodcastTitle string
	Kind         string // "transcript" | "source"
	Text         string
	Similarity   float64
	StartMS      int64
	EndMS        int64
	Speakers     []string
	SourceURL    string
	SourceTitle  string
}

// SummaryInfo is one owner-scoped generated summary returned to the model.
type SummaryInfo struct {
	DiscussionID string
	PodcastTitle string
	Markdown     string
}

// DocumentInfo is presentation-safe metadata for a generated discussion
// document. The client fetches the actual mindmap/PPT only when it opens.
type DocumentInfo struct {
	DiscussionID string `json:"discussion_id"`
	Title        string `json:"title"`
}

// SourceInfo is one research source of a podcast.
type SourceInfo struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// TranscriptLine is one spoken line inside a TranscriptSlice.
type TranscriptLine struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms"`
}

// TranscriptSlice is a time-bounded excerpt of one podcast's transcript.
type TranscriptSlice struct {
	DiscussionID string           `json:"discussion_id"`
	Title        string           `json:"title,omitempty"`
	StartMS      int64            `json:"start_ms"`
	EndMS        int64            `json:"end_ms"`
	Lines        []TranscriptLine `json:"lines"`
}

// PodcastHighlightGroup keeps several validated quotes attached to their
// podcast so one tool call can render every result together.
type PodcastHighlightGroup struct {
	Podcast PodcastInfo      `json:"podcast"`
	Lines   []TranscriptLine `json:"lines"`
}

// Retriever is the server-implemented seam the tools call into. An empty
// discussionID means "across the whole library" and is only used in global
// scope; per-podcast scope always passes the conversation's discussion.
type Retriever interface {
	SearchSummaries(ctx context.Context, discussionID, query string, limit int) ([]SummaryInfo, error)
	SearchContent(ctx context.Context, discussionID, query string, limit int) ([]ContentHit, error)
	SearchPodcasts(ctx context.Context, query string, limit int) ([]PodcastInfo, error)
	GetPodcast(ctx context.Context, discussionID string) (*PodcastInfo, error)
	GetPodcasts(ctx context.Context, discussionIDs []string) ([]PodcastInfo, error)
	GetSources(ctx context.Context, discussionID string) ([]SourceInfo, error)
	TranscriptRange(ctx context.Context, discussionID string, startMS, endMS int64) (*TranscriptSlice, error)
	GetDocument(ctx context.Context, discussionID, documentType string) (*DocumentInfo, error)
}

// Options carries the per-turn scaffolding.
type Options struct {
	Scope        string // ScopePodcast | ScopeGlobal
	DiscussionID string // required for ScopePodcast
	Language     string

	// Podcast-scope context injected into the system prompt.
	PodcastTitle    string
	PodcastTopic    string
	SummaryMarkdown string
}

const podcastSystemBase = `You are a knowledgeable Q&A assistant for one specific podcast episode the user has already generated and listened to.

You run as an agent loop with tools:
- search_summary: read the podcast's generated summary. This is your primary way to answer general content questions.
- search_content: semantic search over the transcript and research sources for exact details, quotes, speakers, timestamps, or gaps in the summary.
- get_sources: list the research sources behind this podcast.
- show_highlight_lines: display one or more transcript-grounded quotes together.
- show_transcript: display a transcript excerpt card to the user for a given time range.
- show_sources: display source cards to the user.
- display_mindmap: display the generated mindmap.
- display_ppt: display the generated slide deck.

Guidelines:
- Answer questions about the podcast's content, arguments, speakers, and source material. For general content questions, call search_summary first and answer from it when sufficient. Call search_content only when the user needs transcript-level precision or the summary does not contain the answer.
- When search_content supplies speaker/timestamp evidence, cite who said it and roughly when.
- When the user asks for exact quotes or highlights, collect them and make one show_highlight_lines call. It is terminal: use no assistant prose in the same turn and do not repeat the displayed quote. For a longer passage, call show_transcript with the time range; when they ask about sources or evidence, call get_sources or show_sources.
- If retrieval returns nothing relevant, say so honestly instead of inventing content.
- Keep answers conversational and concise. Reply in the requested language.`

const globalSystemBase = `You are a knowledgeable assistant for the user's whole podcast library. Every podcast was generated by the user in this app.

You run as an agent loop with tools:
- search_podcasts: find podcasts by title/topic keywords.
- search_summary: search generated podcast summaries, optionally restricted to one podcast.
- search_content: semantic search over the transcripts and source material of every podcast (optionally restricted to one podcast via discussion_id).
- get_sources: list one podcast's research sources.
- display_podcasts: display all matching podcasts together in a tappable grid.
- show_podcasts: display highlights for one or more podcasts together.
- show_highlight_lines: display one or more transcript-grounded quotes together.
- show_transcript: display a transcript excerpt card for one podcast and time range.
- show_sources: display source cards for one podcast.
- display_mindmap: display one podcast's generated mindmap.
- display_ppt: display one podcast's generated slide deck.

Guidelines:
- Use search_summary first for general questions about content, themes, arguments, comparisons, or conclusions. Use search_content only for transcript-level details, exact quotes, speakers/timestamps, or when summaries do not answer the question. Use search_podcasts for title/topic lookup.
- Collect every relevant result before presenting it. Use one batch presentation tool call containing all podcasts or quotes; never emit one presentation call per podcast.
- Use display_podcasts for search/list results, show_podcasts when the user asks for episode highlights, and show_highlight_lines when the user asks to see exact quotes.
- Presentation tools are terminal. Call exactly one of them with no assistant prose in the same turn. The card is the answer: never repeat podcast titles, metadata, or quotes before or after the call.
- In prose-only answers, cite the podcast title and, where relevant, the speaker/timestamp. Once you choose a presentation tool, the card carries that context and you must not repeat it.
- If nothing in the library matches, say so honestly.
- Keep answers conversational and concise. Reply in the requested language.`

// systemPrompt assembles the scope prompt plus per-conversation context.
func systemPrompt(opts Options) string {
	var sb strings.Builder
	if opts.Scope == ScopeGlobal {
		sb.WriteString(globalSystemBase)
	} else {
		sb.WriteString(podcastSystemBase)
		sb.WriteString("\n\nPodcast context:\n")
		if opts.PodcastTitle != "" {
			sb.WriteString("- Title: " + opts.PodcastTitle + "\n")
		}
		if opts.PodcastTopic != "" {
			sb.WriteString("- Topic: " + opts.PodcastTopic + "\n")
		}
		if summary := strings.TrimSpace(opts.SummaryMarkdown); summary != "" {
			if len(summary) > 4000 {
				summary = summary[:4000] + "…"
			}
			sb.WriteString("\nEpisode summary:\n" + summary + "\n")
		}
	}
	lang := strings.TrimSpace(opts.Language)
	if lang == "" {
		lang = "en-US"
	}
	sb.WriteString("\nRequested language: " + lang)
	return sb.String()
}

// RunTurn runs one Q&A turn: it streams the model over the rebuilt history,
// dispatches tools through the retriever, and emits events to emit. History
// must already be a valid OpenAI message sequence.
func RunTurn(ctx context.Context, client *llm.Client, retriever Retriever, history []llm.Message, opts Options, emit func(ConvEvent)) error {
	session := &session{retriever: retriever, opts: opts}
	msgs := append([]llm.Message(nil), history...)
	system := systemPrompt(opts)
	tools := toolSet(opts.Scope)

	for round := 0; round < maxTurnRounds; round++ {
		stream, err := client.Stream(ctx, system, msgs, tools)
		if err != nil {
			return fmt.Errorf("qa turn: %w", err)
		}

		var assistantText strings.Builder
		var tcDeltas []llm.DeltaToolCall
		activeTool := ""
		activeToolID := ""
		toolIDs := map[int]string{}
		toolNames := map[int]string{}
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			if d.TextChunk != "" {
				assistantText.WriteString(d.TextChunk)
				emit(ConvEvent{Kind: ConvText, Text: d.TextChunk})
			}
			if d.ToolCall != nil {
				tcDeltas = append(tcDeltas, *d.ToolCall)
				if d.ToolCall.ID != "" {
					toolIDs[d.ToolCall.Index] = d.ToolCall.ID
				}
				if d.ToolCall.Name != "" {
					toolNames[d.ToolCall.Index] = d.ToolCall.Name
				}
				if d.ToolCall.Name != "" && d.ToolCall.Name != activeTool {
					activeTool = d.ToolCall.Name
					activeToolID = toolIDs[d.ToolCall.Index]
					if activeToolID == "" {
						activeToolID = d.ToolCall.ID
					}
					emit(ConvEvent{Kind: ConvToolStart, ToolName: activeTool, ToolCallID: activeToolID})
				}
				if d.ToolCall.Arguments != "" {
					toolID := toolIDs[d.ToolCall.Index]
					if toolID == "" {
						toolID = activeToolID
					}
					toolName := toolNames[d.ToolCall.Index]
					if toolName == "" {
						toolName = activeTool
					}
					emit(ConvEvent{Kind: ConvToolDelta, ToolName: toolName, ToolCallID: toolID, Text: d.ToolCall.Arguments})
				}
			}
		}
		if err := stream.Err(); err != nil {
			return fmt.Errorf("qa turn: %w", err)
		}

		calls := llm.AssembleToolCalls(tcDeltas)
		text := assistantText.String()

		if len(calls) == 0 {
			if strings.TrimSpace(text) != "" {
				emit(ConvEvent{Kind: ConvAssistant, Text: text})
			}
			return nil
		}

		emit(ConvEvent{Kind: ConvAssistant, Text: text, Calls: calls})
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: calls})

		terminalPresentation := false
		for _, tc := range calls {
			emit(ConvEvent{Kind: ConvToolCall, Call: tc, ToolName: tc.Name})
			output, card, isErr := session.dispatch(ctx, tc)
			if card != nil {
				emit(ConvEvent{Kind: ConvCard, Call: tc, ToolName: tc.Name, Output: output, Card: card})
				terminalPresentation = terminalPresentation || isBatchPresentationTool(tc.Name)
			} else {
				emit(ConvEvent{Kind: ConvToolResult, Call: tc, Output: output, IsError: isErr})
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: output, ToolCallID: tc.ID})
		}
		if terminalPresentation {
			return nil
		}
	}
	return nil
}

func isBatchPresentationTool(name string) bool {
	switch name {
	case "display_podcasts", "show_podcasts", "show_highlight_lines", "display_mindmap", "display_ppt":
		return true
	default:
		return false
	}
}

type session struct {
	retriever Retriever
	opts      Options
}

const (
	maxDisplayedPodcasts = 8
	maxHighlightLines    = 12
)

type highlightSelection struct {
	DiscussionID string `json:"discussion_id"`
	StartMS      int64  `json:"start_ms"`
	EndMS        int64  `json:"end_ms"`
	Quote        string `json:"quote"`
}

type podcastHighlightSelection struct {
	DiscussionID string               `json:"discussion_id"`
	Highlights   []highlightSelection `json:"highlights"`
}

// scopedDiscussionID resolves which discussion a tool call targets: the
// conversation's own podcast in podcast scope, or the model-provided
// discussion_id in global scope.
func (s *session) scopedDiscussionID(args string, required bool) (string, error) {
	if s.opts.Scope != ScopeGlobal {
		return s.opts.DiscussionID, nil
	}
	id := optionalStringArg(args, "discussion_id")
	if id == "" && required {
		return "", fmt.Errorf("discussion_id is required")
	}
	return id, nil
}

func (s *session) dispatch(ctx context.Context, tc llm.ToolCall) (output string, card *Card, isErr bool) {
	switch tc.Name {
	case "search_summary":
		query := optionalStringArg(tc.Arguments, "query")
		discussionID, _ := s.scopedDiscussionID(tc.Arguments, false)
		if s.opts.Scope == ScopeGlobal && query == "" && discussionID == "" {
			return "query or discussion_id is required", nil, true
		}
		summaries, err := s.retriever.SearchSummaries(ctx, discussionID, query, 5)
		if err != nil {
			return "search_summary failed: " + err.Error(), nil, true
		}
		if len(summaries) == 0 {
			return "No matching generated summary found. Try search_content for transcript-level results.", nil, false
		}
		return summaryDigest(summaries, s.opts.Scope == ScopeGlobal), nil, false
	case "search_content":
		query := optionalStringArg(tc.Arguments, "query")
		if query == "" {
			return "query is required", nil, true
		}
		discussionID, _ := s.scopedDiscussionID(tc.Arguments, false)
		hits, err := s.retriever.SearchContent(ctx, discussionID, query, 8)
		if err != nil {
			return "search_content failed: " + err.Error(), nil, true
		}
		if len(hits) == 0 {
			return "No matching content found. Answer from what you already know about the conversation, or tell the user nothing matched.", nil, false
		}
		return contentDigest(hits, s.opts.Scope == ScopeGlobal), nil, false
	case "search_podcasts":
		query := optionalStringArg(tc.Arguments, "query")
		if query == "" {
			return "query is required", nil, true
		}
		podcasts, err := s.retriever.SearchPodcasts(ctx, query, 10)
		if err != nil {
			return "search_podcasts failed: " + err.Error(), nil, true
		}
		if len(podcasts) == 0 {
			return "No podcasts matched. Try search_content for content-level matches.", nil, false
		}
		return podcastDigest(podcasts), nil, false
	case "display_podcasts":
		ids := uniqueNonEmpty(stringsArgOptional(tc.Arguments, "discussion_ids"), maxDisplayedPodcasts)
		if len(ids) == 0 {
			return "discussion_ids is required", nil, true
		}
		podcasts, err := s.retriever.GetPodcasts(ctx, ids)
		if err != nil {
			return "display_podcasts failed: " + err.Error(), nil, true
		}
		if len(podcasts) == 0 {
			return "display_podcasts failed: podcasts not found", nil, true
		}
		return "Podcast grid shown to the user. Do not repeat its podcast titles or metadata.",
			&Card{Kind: CardPodcasts, Podcasts: podcasts}, false
	case "show_podcasts":
		var args struct {
			Podcasts []podcastHighlightSelection `json:"podcasts"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil || len(args.Podcasts) == 0 {
			return "podcasts is required", nil, true
		}
		groups, err := s.podcastHighlightGroups(ctx, args.Podcasts)
		if err != nil {
			return "show_podcasts failed: " + err.Error(), nil, true
		}
		return "Podcast highlight cards shown to the user. Do not repeat their titles, metadata, or quotes.",
			&Card{Kind: CardPodcastHighlights, Highlights: groups}, false
	case "show_highlight_lines":
		var args struct {
			Highlights []highlightSelection `json:"highlights"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil || len(args.Highlights) == 0 {
			return "highlights is required", nil, true
		}
		groups, err := s.highlightLineGroups(ctx, args.Highlights)
		if err != nil {
			return "show_highlight_lines failed: " + err.Error(), nil, true
		}
		return "Highlight lines shown to the user. Do not repeat their podcast titles or quotes.",
			&Card{Kind: CardHighlightLines, Highlights: groups}, false
	case "get_sources":
		discussionID, err := s.scopedDiscussionID(tc.Arguments, true)
		if err != nil {
			return err.Error(), nil, true
		}
		sources, err := s.retriever.GetSources(ctx, discussionID)
		if err != nil {
			return "get_sources failed: " + err.Error(), nil, true
		}
		if len(sources) == 0 {
			return "This podcast has no research sources.", nil, false
		}
		return sourcesDigest(sources), nil, false
	case "show_podcast":
		discussionID, err := s.scopedDiscussionID(tc.Arguments, true)
		if err != nil {
			return err.Error(), nil, true
		}
		p, err := s.retriever.GetPodcast(ctx, discussionID)
		if err != nil || p == nil {
			return "show_podcast failed: podcast not found", nil, true
		}
		return "Podcast card shown to the user: " + p.Title + ". Continue your answer without restating its metadata.",
			&Card{Kind: CardPodcast, Podcast: p}, false
	case "show_transcript":
		discussionID, err := s.scopedDiscussionID(tc.Arguments, true)
		if err != nil {
			return err.Error(), nil, true
		}
		startMS := int64Arg(tc.Arguments, "start_ms")
		endMS := int64Arg(tc.Arguments, "end_ms")
		if endMS <= startMS {
			return "end_ms must be greater than start_ms", nil, true
		}
		slice, err := s.retriever.TranscriptRange(ctx, discussionID, startMS, endMS)
		if err != nil || slice == nil {
			return "show_transcript failed: transcript range unavailable", nil, true
		}
		if len(slice.Lines) == 0 {
			return "No transcript lines in that range.", nil, true
		}
		return "Transcript excerpt shown to the user:\n" + transcriptText(slice),
			&Card{Kind: CardTranscript, Transcript: slice}, false
	case "show_sources":
		discussionID, err := s.scopedDiscussionID(tc.Arguments, true)
		if err != nil {
			return err.Error(), nil, true
		}
		sources, err := s.retriever.GetSources(ctx, discussionID)
		if err != nil {
			return "show_sources failed: " + err.Error(), nil, true
		}
		if urls := stringsArgOptional(tc.Arguments, "urls"); len(urls) > 0 {
			wanted := map[string]bool{}
			for _, u := range urls {
				wanted[strings.TrimSpace(u)] = true
			}
			filtered := sources[:0]
			for _, src := range sources {
				if wanted[src.URL] {
					filtered = append(filtered, src)
				}
			}
			if len(filtered) > 0 {
				sources = filtered
			}
		}
		if len(sources) == 0 {
			return "This podcast has no research sources to show.", nil, false
		}
		return fmt.Sprintf("Source cards shown to the user (%d sources). Continue your answer without relisting them.", len(sources)),
			&Card{Kind: CardSources, Sources: sources}, false
	case "display_mindmap", "display_ppt":
		discussionID, err := s.scopedDiscussionID(tc.Arguments, true)
		if err != nil {
			return err.Error(), nil, true
		}
		documentType, cardKind, label := "mindmap", CardMindmap, "Mindmap"
		if tc.Name == "display_ppt" {
			documentType, cardKind, label = "ppt", CardPPT, "PPT"
		}
		document, err := s.retriever.GetDocument(ctx, discussionID, documentType)
		if err != nil {
			return tc.Name + " failed: " + err.Error(), nil, true
		}
		if document == nil {
			return label + " is not available for this podcast.", nil, true
		}
		return label + " shown to the user. Do not add assistant prose in this turn.",
			&Card{Kind: cardKind, Document: document}, false
	default:
		return "unknown tool: " + tc.Name, nil, true
	}
}

func summaryDigest(summaries []SummaryInfo, includeTitle bool) string {
	var sb strings.Builder
	sb.WriteString("[generated summaries]\n")
	for i, summary := range summaries {
		markdown := strings.TrimSpace(summary.Markdown)
		if len(markdown) > 6000 {
			markdown = markdown[:6000] + "…"
		}
		fmt.Fprintf(&sb, "\nResult %d", i+1)
		if includeTitle {
			fmt.Fprintf(&sb, " — %s (discussion_id=%s)", summary.PodcastTitle, summary.DiscussionID)
		}
		sb.WriteString("\n" + markdown + "\n")
	}
	return sb.String()
}

func (s *session) podcastHighlightGroups(ctx context.Context, selections []podcastHighlightSelection) ([]PodcastHighlightGroup, error) {
	ids := make([]string, 0, len(selections))
	byID := make(map[string][]highlightSelection, len(selections))
	for _, selection := range selections {
		id := strings.TrimSpace(selection.DiscussionID)
		if id == "" || len(ids) >= maxDisplayedPodcasts {
			continue
		}
		if _, exists := byID[id]; !exists {
			ids = append(ids, id)
		}
		byID[id] = append(byID[id], selection.Highlights...)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one discussion_id is required")
	}
	podcasts, err := s.retriever.GetPodcasts(ctx, ids)
	if err != nil {
		return nil, err
	}
	podcastByID := make(map[string]PodcastInfo, len(podcasts))
	for _, podcast := range podcasts {
		podcastByID[podcast.ID] = podcast
	}
	groups := make([]PodcastHighlightGroup, 0, len(podcasts))
	totalLines := 0
	for _, id := range ids {
		podcast, ok := podcastByID[id]
		if !ok {
			continue
		}
		group := PodcastHighlightGroup{Podcast: podcast}
		for _, selection := range byID[id] {
			if totalLines >= maxHighlightLines {
				break
			}
			selection.DiscussionID = id
			line, err := s.validatedHighlightLine(ctx, selection)
			if err != nil {
				return nil, err
			}
			group.Lines = append(group.Lines, line)
			totalLines++
		}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("podcasts not found")
	}
	return groups, nil
}

func (s *session) highlightLineGroups(ctx context.Context, selections []highlightSelection) ([]PodcastHighlightGroup, error) {
	if len(selections) > maxHighlightLines {
		selections = selections[:maxHighlightLines]
	}
	ids := make([]string, 0, len(selections))
	byID := make(map[string][]highlightSelection, len(selections))
	for _, selection := range selections {
		id := strings.TrimSpace(selection.DiscussionID)
		if s.opts.Scope != ScopeGlobal {
			id = s.opts.DiscussionID
		}
		if id == "" {
			return nil, fmt.Errorf("discussion_id is required")
		}
		if _, exists := byID[id]; !exists {
			ids = append(ids, id)
		}
		selection.DiscussionID = id
		byID[id] = append(byID[id], selection)
	}
	podcasts, err := s.retriever.GetPodcasts(ctx, ids)
	if err != nil {
		return nil, err
	}
	podcastByID := make(map[string]PodcastInfo, len(podcasts))
	for _, podcast := range podcasts {
		podcastByID[podcast.ID] = podcast
	}
	groups := make([]PodcastHighlightGroup, 0, len(podcasts))
	for _, id := range ids {
		podcast, ok := podcastByID[id]
		if !ok {
			continue
		}
		group := PodcastHighlightGroup{Podcast: podcast}
		for _, selection := range byID[id] {
			line, err := s.validatedHighlightLine(ctx, selection)
			if err != nil {
				return nil, err
			}
			group.Lines = append(group.Lines, line)
		}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("podcasts not found")
	}
	return groups, nil
}

func (s *session) validatedHighlightLine(ctx context.Context, selection highlightSelection) (TranscriptLine, error) {
	selection.Quote = strings.TrimSpace(selection.Quote)
	if selection.Quote == "" {
		return TranscriptLine{}, fmt.Errorf("quote is required")
	}
	if selection.EndMS <= selection.StartMS {
		return TranscriptLine{}, fmt.Errorf("end_ms must be greater than start_ms")
	}
	slice, err := s.retriever.TranscriptRange(ctx, selection.DiscussionID, selection.StartMS, selection.EndMS)
	if err != nil || slice == nil {
		return TranscriptLine{}, fmt.Errorf("transcript range unavailable")
	}
	quote := normalizedQuote(selection.Quote)
	for _, line := range slice.Lines {
		text := normalizedQuote(line.Text)
		if text == quote || strings.Contains(text, quote) || strings.Contains(quote, text) {
			return line, nil
		}
	}
	return TranscriptLine{}, fmt.Errorf("quote was not found in the stored transcript range")
}

func normalizedQuote(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func uniqueNonEmpty(values []string, limit int) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func contentDigest(hits []ContentHit, includeTitle bool) string {
	var sb strings.Builder
	sb.WriteString("[search results]\n")
	for i, h := range hits {
		sb.WriteString(fmt.Sprintf("--- result %d (similarity %.2f, kind %s", i+1, h.Similarity, h.Kind))
		if includeTitle && h.PodcastTitle != "" {
			sb.WriteString(fmt.Sprintf(", podcast %q, discussion_id %s", h.PodcastTitle, h.DiscussionID))
		}
		if h.Kind == "transcript" {
			sb.WriteString(fmt.Sprintf(", %s–%s", formatMS(h.StartMS), formatMS(h.EndMS)))
		} else if h.SourceTitle != "" {
			sb.WriteString(fmt.Sprintf(", source %q", h.SourceTitle))
		}
		sb.WriteString(") ---\n")
		sb.WriteString(strings.TrimSpace(h.Text))
		sb.WriteString("\n")
	}
	return sb.String()
}

func podcastDigest(podcasts []PodcastInfo) string {
	var sb strings.Builder
	sb.WriteString("[podcasts]\n")
	for _, p := range podcasts {
		sb.WriteString(fmt.Sprintf("- discussion_id: %s | title: %s", p.ID, p.Title))
		if p.Topic != "" {
			sb.WriteString(" | topic: " + truncate(p.Topic, 120))
		}
		if !p.CreatedAt.IsZero() {
			sb.WriteString(" | created: " + p.CreatedAt.Format("2006-01-02"))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func sourcesDigest(sources []SourceInfo) string {
	var sb strings.Builder
	sb.WriteString("[sources]\n")
	for _, src := range sources {
		sb.WriteString("- " + src.Title)
		if src.URL != "" {
			sb.WriteString(" (" + src.URL + ")")
		}
		if src.Snippet != "" {
			sb.WriteString(": " + truncate(src.Snippet, 200))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func transcriptText(slice *TranscriptSlice) string {
	var sb strings.Builder
	for _, l := range slice.Lines {
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", formatMS(l.StartMS), l.Speaker, l.Text))
	}
	return sb.String()
}

func formatMS(ms int64) string {
	total := ms / 1000
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- tool argument helpers ---

func optionalStringArg(raw, key string) string {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &args) != nil {
		return ""
	}
	var v string
	if json.Unmarshal(args[key], &v) != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func int64Arg(raw, key string) int64 {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &args) != nil {
		return 0
	}
	var v int64
	if json.Unmarshal(args[key], &v) != nil {
		var f float64
		if json.Unmarshal(args[key], &f) == nil {
			return int64(f)
		}
		return 0
	}
	return v
}

func stringsArgOptional(raw, key string) []string {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &args) != nil {
		return nil
	}
	var list []string
	if json.Unmarshal(args[key], &list) != nil {
		return nil
	}
	return list
}
