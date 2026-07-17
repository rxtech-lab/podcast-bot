package qa

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/sirily11/debate-bot/internal/llm"
)

type qaToolTestRetriever struct {
	podcasts  map[string]PodcastInfo
	lines     map[string][]TranscriptLine
	summaries []SummaryInfo
	documents map[string]DocumentInfo
}

func (r *qaToolTestRetriever) SearchSummaries(context.Context, string, string, int) ([]SummaryInfo, error) {
	return r.summaries, nil
}

func (r *qaToolTestRetriever) SearchContent(context.Context, string, string, int) ([]ContentHit, error) {
	return nil, nil
}

func (r *qaToolTestRetriever) SearchPodcasts(context.Context, string, int) ([]PodcastInfo, error) {
	return nil, nil
}

func (r *qaToolTestRetriever) GetPodcast(_ context.Context, id string) (*PodcastInfo, error) {
	podcast, ok := r.podcasts[id]
	if !ok {
		return nil, nil
	}
	return &podcast, nil
}

func (r *qaToolTestRetriever) GetPodcasts(_ context.Context, ids []string) ([]PodcastInfo, error) {
	out := make([]PodcastInfo, 0, len(ids))
	for _, id := range ids {
		if podcast, ok := r.podcasts[id]; ok {
			out = append(out, podcast)
		}
	}
	return out, nil
}

func (r *qaToolTestRetriever) GetSources(context.Context, string) ([]SourceInfo, error) {
	return nil, nil
}

func (r *qaToolTestRetriever) TranscriptRange(_ context.Context, id string, startMS, endMS int64) (*TranscriptSlice, error) {
	lines, ok := r.lines[id]
	if !ok {
		return nil, errors.New("missing transcript")
	}
	slice := &TranscriptSlice{DiscussionID: id, StartMS: startMS, EndMS: endMS}
	for _, line := range lines {
		if line.StartMS >= startMS && line.StartMS <= endMS {
			slice.Lines = append(slice.Lines, line)
		}
	}
	return slice, nil
}

func (r *qaToolTestRetriever) GetDocument(_ context.Context, id, documentType string) (*DocumentInfo, error) {
	document, ok := r.documents[id+":"+documentType]
	if !ok {
		return nil, nil
	}
	return &document, nil
}

func TestBothScopesExposeSummaryAndDocumentTools(t *testing.T) {
	for scope, tools := range map[string][]openai.ChatCompletionToolParam{
		ScopePodcast: podcastTools(),
		ScopeGlobal:  globalTools(),
	} {
		names := map[string]bool{}
		for _, tool := range tools {
			names[tool.Function.Name] = true
		}
		for _, name := range []string{"search_summary", "display_mindmap", "display_ppt"} {
			if !names[name] {
				t.Fatalf("%s tool set is missing %q", scope, name)
			}
		}
	}
}

func TestSearchSummaryReturnsGeneratedSummary(t *testing.T) {
	retriever := &qaToolTestRetriever{summaries: []SummaryInfo{{
		DiscussionID: "a", PodcastTitle: "Alpha", Markdown: "# Summary\n\nCore conclusion.",
	}}}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	output, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
		Name: "search_summary", Arguments: `{"query":"core conclusion"}`,
	})
	if isErr || card != nil || !strings.Contains(output, "Core conclusion") || !strings.Contains(output, "discussion_id=a") {
		t.Fatalf("search_summary output=%q card=%+v isErr=%v", output, card, isErr)
	}
}

func TestDisplayGeneratedDocumentsBuildTerminalCards(t *testing.T) {
	retriever := &qaToolTestRetriever{documents: map[string]DocumentInfo{
		"a:mindmap": {DiscussionID: "a", Title: "Alpha"},
		"a:ppt":     {DiscussionID: "a", Title: "Alpha"},
	}}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	for toolName, wantKind := range map[string]string{"display_mindmap": CardMindmap, "display_ppt": CardPPT} {
		_, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
			Name: toolName, Arguments: `{"discussion_id":"a"}`,
		})
		if isErr || card == nil || card.Kind != wantKind || card.Document == nil || card.Document.DiscussionID != "a" {
			t.Fatalf("%s card=%+v isErr=%v", toolName, card, isErr)
		}
		if !isBatchPresentationTool(toolName) {
			t.Fatalf("%s must terminate the presentation turn", toolName)
		}
	}
}

func TestGlobalToolsExposeBatchPresentationOnly(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range globalTools() {
		names[tool.Function.Name] = true
	}
	for _, name := range []string{"display_podcasts", "show_podcasts", "show_highlight_lines"} {
		if !names[name] {
			t.Fatalf("batch tool %q is missing", name)
		}
	}
	if names["show_podcast"] {
		t.Fatal("new turns must not expose the legacy singular show_podcast tool")
	}
}

func TestDisplayPodcastsPreservesOrderAndDeduplicates(t *testing.T) {
	retriever := &qaToolTestRetriever{podcasts: map[string]PodcastInfo{
		"a": {ID: "a", Title: "Alpha"},
		"b": {ID: "b", Title: "Beta"},
	}}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	output, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
		Name: "display_podcasts", Arguments: `{"discussion_ids":["b","a","missing","b"]}`,
	})
	if isErr || card == nil {
		t.Fatalf("display_podcasts failed: output=%q card=%+v", output, card)
	}
	if card.Kind != CardPodcasts || len(card.Podcasts) != 2 || card.Podcasts[0].ID != "b" || card.Podcasts[1].ID != "a" {
		t.Fatalf("podcast batch = %+v", card.Podcasts)
	}
}

func TestShowHighlightLinesGroupsPodcastsAndUsesCanonicalTranscript(t *testing.T) {
	retriever := &qaToolTestRetriever{
		podcasts: map[string]PodcastInfo{
			"a": {ID: "a", Title: "Alpha"},
			"b": {ID: "b", Title: "Beta"},
		},
		lines: map[string][]TranscriptLine{
			"a": {{Speaker: "Alice", Text: "The complete canonical Alpha quote.", StartMS: 1_000}},
			"b": {{Speaker: "Bob", Text: "The complete canonical Beta quote.", StartMS: 2_000}},
		},
	}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	_, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
		Name: "show_highlight_lines",
		Arguments: `{"highlights":[` +
			`{"discussion_id":"b","start_ms":0,"end_ms":3000,"quote":"canonical Beta"},` +
			`{"discussion_id":"a","start_ms":0,"end_ms":3000,"quote":"canonical Alpha"}` +
			`]}`,
	})
	if isErr || card == nil {
		t.Fatalf("show_highlight_lines failed: card=%+v", card)
	}
	if card.Kind != CardHighlightLines || len(card.Highlights) != 2 {
		t.Fatalf("highlight groups = %+v", card.Highlights)
	}
	if card.Highlights[0].Podcast.ID != "b" || card.Highlights[1].Podcast.ID != "a" {
		t.Fatalf("highlight order = %+v", card.Highlights)
	}
	if got := card.Highlights[0].Lines[0].Text; got != "The complete canonical Beta quote." {
		t.Fatalf("displayed model text instead of canonical transcript: %q", got)
	}
}

func TestShowPodcastsBuildsOneCardForMultiplePodcasts(t *testing.T) {
	retriever := &qaToolTestRetriever{
		podcasts: map[string]PodcastInfo{
			"a": {ID: "a", Title: "Alpha"},
			"b": {ID: "b", Title: "Beta"},
		},
		lines: map[string][]TranscriptLine{
			"a": {{Speaker: "Alice", Text: "Alpha highlight.", StartMS: 1_000}},
			"b": {{Speaker: "Bob", Text: "Beta highlight.", StartMS: 2_000}},
		},
	}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	_, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
		Name: "show_podcasts",
		Arguments: `{"podcasts":[` +
			`{"discussion_id":"a","highlights":[{"start_ms":0,"end_ms":3000,"quote":"Alpha highlight."}]},` +
			`{"discussion_id":"b","highlights":[{"start_ms":0,"end_ms":3000,"quote":"Beta highlight."}]}` +
			`]}`,
	})
	if isErr || card == nil || card.Kind != CardPodcastHighlights || len(card.Highlights) != 2 {
		t.Fatalf("multi-podcast card = %+v isErr=%v", card, isErr)
	}
}

func TestShowHighlightLinesRejectsUngroundedQuote(t *testing.T) {
	retriever := &qaToolTestRetriever{
		podcasts: map[string]PodcastInfo{"a": {ID: "a", Title: "Alpha"}},
		lines: map[string][]TranscriptLine{
			"a": {{Speaker: "Alice", Text: "Stored transcript text.", StartMS: 1_000}},
		},
	}
	s := &session{retriever: retriever, opts: Options{Scope: ScopeGlobal}}
	output, card, isErr := s.dispatch(context.Background(), llm.ToolCall{
		Name:      "show_highlight_lines",
		Arguments: `{"highlights":[{"discussion_id":"a","start_ms":0,"end_ms":3000,"quote":"invented quote"}]}`,
	})
	if !isErr || card != nil || output == "" {
		t.Fatalf("ungrounded quote accepted: output=%q card=%+v isErr=%v", output, card, isErr)
	}
}

func TestBatchPresentationToolsAreTerminal(t *testing.T) {
	for _, name := range []string{"display_podcasts", "show_podcasts", "show_highlight_lines"} {
		if !isBatchPresentationTool(name) {
			t.Fatalf("%s is not terminal", name)
		}
	}
	if isBatchPresentationTool("search_content") {
		t.Fatal("retrieval tools must not terminate the turn")
	}
}
