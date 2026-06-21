package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// defaultSearchURL is Tavily's search endpoint. Tavily's request/response shape
// is the lingua franca we target; other providers that mirror it work by
// overriding SEARCH_API_URL.
const defaultSearchURL = "https://api.tavily.com/search"

// maxResearchSources caps how many sources a plan carries — enough to ground
// the draft and populate the UI without overwhelming either.
const maxResearchSources = 6

// tavilyRequest / tavilyResult mirror the Tavily search API.
type tavilyRequest struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// research gathers real web sources for a topic. Returns (nil, false) when no
// search backend is configured or the lookup fails — planning then proceeds
// ungrounded and reports researched=false, exactly as before this was wired.
func (p *Planner) research(ctx context.Context, topic string) ([]config.Source, bool) {
	key := p.env.SearchAPIKey
	if strings.TrimSpace(key) == "" {
		return nil, false
	}
	url := p.env.SearchAPIURL
	if url == "" {
		url = defaultSearchURL
	}

	body, _ := json.Marshal(tavilyRequest{
		APIKey:      key,
		Query:       topic,
		MaxResults:  maxResearchSources,
		SearchDepth: "basic",
	})
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var parsed tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false
	}

	sources := make([]config.Source, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		sources = append(sources, config.Source{
			Title:   strings.TrimSpace(r.Title),
			URL:     strings.TrimSpace(r.URL),
			Snippet: truncate(strings.TrimSpace(r.Content), 400),
		})
	}
	if len(sources) == 0 {
		return nil, false
	}
	return sources, true
}

// sourcesPrompt renders sources as a compact block to fold into the planning
// prompt so the LLM grounds the background in them.
func sourcesPrompt(sources []config.Source) string {
	if len(sources) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nGround the background in these researched sources; reference their substance where relevant:\n")
	for i, s := range sources {
		fmt.Fprintf(&sb, "%d. %s — %s\n   %s\n", i+1, s.Title, s.URL, s.Snippet)
	}
	return sb.String()
}
