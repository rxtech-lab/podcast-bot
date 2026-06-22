package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// Firecrawl REST endpoints. Search grounds a plan in live web results; scrape
// reads a single URL the user pasted or added. Both return clean markdown.
const (
	firecrawlSearchURL = "https://api.firecrawl.dev/v2/search"
	firecrawlScrapeURL = "https://api.firecrawl.dev/v2/scrape"
)

// maxResearchSources caps how many sources a plan carries — enough to ground
// the draft and populate the UI ("N external links searched") without
// overwhelming either. The product surfaces up to ten searched links.
const maxResearchSources = 10

// firecrawlSearchRequest mirrors Firecrawl's POST /v2/search body. Asking for
// the markdown scrape format gives the planner real page substance, not just a
// title + snippet.
type firecrawlSearchRequest struct {
	Query         string                 `json:"query"`
	Limit         int                    `json:"limit"`
	ScrapeOptions firecrawlScrapeOptions `json:"scrapeOptions"`
}

type firecrawlScrapeOptions struct {
	Formats []string `json:"formats"`
}

// firecrawlSearchResponse captures the web results we care about. Firecrawl
// nests results under data.web[]; markdown is present when scrapeOptions asked
// for it.
type firecrawlSearchResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Web []firecrawlDoc `json:"web"`
	} `json:"data"`
}

type firecrawlDoc struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Markdown    string `json:"markdown"`
}

// firecrawlScrapeRequest mirrors POST /v2/scrape.
type firecrawlScrapeRequest struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
}

type firecrawlScrapeResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Markdown string `json:"markdown"`
		Metadata struct {
			Title     string `json:"title"`
			SourceURL string `json:"sourceURL"`
		} `json:"metadata"`
	} `json:"data"`
}

// urlPattern finds bare http(s) URLs the user pasted into a topic so the
// planner can read them as first-class sources.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"')]+`)

// extractURLs returns the de-duplicated http(s) URLs found in text, trimming
// common trailing punctuation that regularly clings to a pasted link.
func extractURLs(text string) []string {
	matches := urlPattern.FindAllString(text, -1)
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;:!?")
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// research gathers real web sources for a topic via Firecrawl search. Returns
// (nil, false) when no key is configured or the lookup fails — planning then
// proceeds ungrounded and reports researched=false.
func (p *Planner) research(ctx context.Context, topic string) ([]config.Source, bool) {
	key := strings.TrimSpace(p.env.FirecrawlAPIKey)
	if key == "" {
		return nil, false
	}

	body, _ := json.Marshal(firecrawlSearchRequest{
		Query:         topic,
		Limit:         maxResearchSources,
		ScrapeOptions: firecrawlScrapeOptions{Formats: []string{"markdown"}},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, firecrawlSearchURL, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var parsed firecrawlSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false
	}

	sources := make([]config.Source, 0, len(parsed.Data.Web))
	for _, r := range parsed.Data.Web {
		if s, ok := docToSource(r); ok {
			sources = append(sources, s)
		}
	}
	if len(sources) == 0 {
		return nil, false
	}
	return sources, true
}

// scrapeURLs reads each URL via Firecrawl scrape and returns one source per
// URL that succeeds. Best-effort: unreachable links are skipped. Empty when no
// key is configured.
func (p *Planner) scrapeURLs(ctx context.Context, urls []string) []config.Source {
	key := strings.TrimSpace(p.env.FirecrawlAPIKey)
	if key == "" || len(urls) == 0 {
		return nil
	}
	out := make([]config.Source, 0, len(urls))
	for _, u := range urls {
		if s, ok := p.scrapeURL(ctx, key, u); ok {
			out = append(out, s)
		}
	}
	return out
}

func (p *Planner) scrapeURL(ctx context.Context, key, url string) (config.Source, bool) {
	body, _ := json.Marshal(firecrawlScrapeRequest{URL: url, Formats: []string{"markdown"}})
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, firecrawlScrapeURL, bytes.NewReader(body))
	if err != nil {
		return config.Source{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return config.Source{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return config.Source{}, false
	}
	var parsed firecrawlScrapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return config.Source{}, false
	}
	title := strings.TrimSpace(parsed.Data.Metadata.Title)
	if title == "" {
		title = url
	}
	src := strings.TrimSpace(parsed.Data.Metadata.SourceURL)
	if src == "" {
		src = url
	}
	markdown := strings.TrimSpace(parsed.Data.Markdown)
	return config.Source{
		Title:    title,
		URL:      src,
		Snippet:  truncate(markdown, 400),
		Markdown: markdown,
	}, true
}

// docToSource maps a Firecrawl web result into a config.Source, preferring the
// scraped markdown for the snippet and falling back to the description.
func docToSource(d firecrawlDoc) (config.Source, bool) {
	url := strings.TrimSpace(d.URL)
	if url == "" {
		return config.Source{}, false
	}
	markdown := strings.TrimSpace(d.Markdown)
	snippet := markdown
	if snippet == "" {
		snippet = strings.TrimSpace(d.Description)
		markdown = snippet
	}
	return config.Source{
		Title:    strings.TrimSpace(d.Title),
		URL:      url,
		Snippet:  truncate(snippet, 400),
		Markdown: markdown,
	}, true
}

// mergeSources appends add to base, skipping URLs already present, and caps the
// result at maxResearchSources so the plan and UI stay bounded.
func mergeSources(base, add []config.Source) []config.Source {
	seen := make(map[string]struct{}, len(base))
	for _, s := range base {
		seen[s.URL] = struct{}{}
	}
	out := append([]config.Source(nil), base...)
	for _, s := range add {
		if strings.TrimSpace(s.URL) == "" {
			continue
		}
		if _, ok := seen[s.URL]; ok {
			continue
		}
		seen[s.URL] = struct{}{}
		out = append(out, s)
	}
	if len(out) > maxResearchSources {
		out = out[:maxResearchSources]
	}
	return out
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
