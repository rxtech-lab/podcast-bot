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

// Firecrawl REST endpoints. Search grounds a plan in live web results; crawl
// reads a single URL the user pasted or added. Both return clean markdown.
const (
	firecrawlSearchURL = "https://api.firecrawl.dev/v2/search"
	firecrawlCrawlURL  = "https://api.firecrawl.dev/v2/crawl"
)

// firecrawlSearchLimit bounds a single Firecrawl search request. Persisted plan
// sources are not capped; user-added links should remain attached to the plan.
const firecrawlSearchLimit = 10

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

type firecrawlCrawlRequest struct {
	URL               string                 `json:"url"`
	Limit             int                    `json:"limit"`
	MaxDiscoveryDepth int                    `json:"maxDiscoveryDepth"`
	Sitemap           string                 `json:"sitemap"`
	ScrapeOptions     firecrawlScrapeOptions `json:"scrapeOptions"`
}

type firecrawlCrawlStartResponse struct {
	Success bool   `json:"success"`
	ID      string `json:"id"`
	URL     string `json:"url"`
}

type firecrawlCrawlStatusResponse struct {
	Status string              `json:"status"`
	Data   []firecrawlCrawlDoc `json:"data"`
}

type firecrawlCrawlDoc struct {
	Markdown string `json:"markdown"`
	Metadata struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		SourceURL   string `json:"sourceURL"`
		URL         string `json:"url"`
	} `json:"metadata"`
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
		Limit:         firecrawlSearchLimit,
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

// SearchSources exposes Firecrawl search for the native source picker. It does
// not mutate a plan; callers can present the returned URLs for review first.
func (p *Planner) SearchSources(ctx context.Context, query string) ([]config.Source, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	sources, ok := p.research(ctx, query)
	if !ok || len(sources) == 0 {
		return nil, fmt.Errorf("no readable search results")
	}
	return sources, nil
}

// crawlURLs reads each URL via Firecrawl crawl and returns one source per URL
// that succeeds. Best-effort: unreachable links are skipped. Empty when no key
// is configured.
func (p *Planner) crawlURLs(ctx context.Context, urls []string) []config.Source {
	key := strings.TrimSpace(p.env.FirecrawlAPIKey)
	if key == "" || len(urls) == 0 {
		return nil
	}
	out := make([]config.Source, 0, len(urls))
	for _, u := range urls {
		if s, ok := p.crawlURL(ctx, key, u); ok {
			out = append(out, s)
		}
	}
	return out
}

func (p *Planner) crawlURL(ctx context.Context, key, url string) (config.Source, bool) {
	body, _ := json.Marshal(firecrawlCrawlRequest{
		URL:               url,
		Limit:             1,
		MaxDiscoveryDepth: 0,
		Sitemap:           "skip",
		ScrapeOptions:     firecrawlScrapeOptions{Formats: []string{"markdown"}},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, firecrawlCrawlURL, bytes.NewReader(body))
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
	var started firecrawlCrawlStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		return config.Source{}, false
	}
	if strings.TrimSpace(started.ID) == "" {
		return config.Source{}, false
	}
	return p.pollCrawl(reqCtx, key, started.ID, url)
}

func (p *Planner) pollCrawl(ctx context.Context, key, id, fallbackURL string) (config.Source, bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		source, status, ok := p.getCrawlStatus(ctx, key, id, fallbackURL)
		if ok {
			return source, true
		}
		if status == "failed" || status == "cancelled" {
			return config.Source{}, false
		}
		select {
		case <-ctx.Done():
			return config.Source{}, false
		case <-ticker.C:
		}
	}
}

func (p *Planner) getCrawlStatus(ctx context.Context, key, id, fallbackURL string) (config.Source, string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, firecrawlCrawlURL+"/"+id, nil)
	if err != nil {
		return config.Source{}, "", false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return config.Source{}, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return config.Source{}, "", false
	}
	var status firecrawlCrawlStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return config.Source{}, "", false
	}
	if status.Status != "completed" || len(status.Data) == 0 {
		return config.Source{}, status.Status, false
	}
	return crawlDocToSource(status.Data[0], fallbackURL)
}

func crawlDocToSource(d firecrawlCrawlDoc, fallbackURL string) (config.Source, string, bool) {
	url := strings.TrimSpace(d.Metadata.SourceURL)
	if url == "" {
		url = strings.TrimSpace(d.Metadata.URL)
	}
	if url == "" {
		url = fallbackURL
	}
	markdown := strings.TrimSpace(d.Markdown)
	snippet := markdown
	if snippet == "" {
		snippet = strings.TrimSpace(d.Metadata.Description)
		markdown = snippet
	}
	title := strings.TrimSpace(d.Metadata.Title)
	if title == "" {
		title = url
	}
	return config.Source{Title: title, URL: url, Snippet: truncate(snippet, 400), Markdown: markdown}, "completed", strings.TrimSpace(url) != ""
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

// mergeSources appends add to base, skipping URLs already present. Persisted
// plan sources are intentionally uncapped so user-added links remain visible.
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
