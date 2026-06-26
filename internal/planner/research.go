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

// firecrawlSearchURL grounds a plan in live web results. Reading a single URL
// the user pasted or added is handled separately by Cloudflare Browser
// Rendering (see cloudflareMarkdownURL), so search does not request Firecrawl
// page scraping.
const firecrawlSearchURL = "https://api.firecrawl.dev/v2/search"

// cloudflareMarkdownURL is Cloudflare Browser Rendering's /markdown endpoint
// (account id is the %s). It renders a URL in headless Chromium and returns the
// page as markdown in a single synchronous call — no async crawl job to poll.
const cloudflareMarkdownURL = "https://api.cloudflare.com/client/v4/accounts/%s/browser-rendering/markdown"

// firecrawlSearchLimit bounds a single Firecrawl search request. Persisted plan
// sources are not capped; user-added links should remain attached to the plan.
const firecrawlSearchLimit = 10

// firecrawlSearchRequest mirrors Firecrawl's POST /v2/search body. Keep this
// metadata-only; explicit URL reads do the heavier page rendering.
type firecrawlSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// firecrawlSearchResponse captures the web results we care about. Firecrawl
// nests results under data.web[].
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

// cloudflareMarkdownRequest mirrors Cloudflare Browser Rendering's POST
// /markdown body. Only the URL is required; the service handles the headless
// render and markdown conversion.
type cloudflareMarkdownRequest struct {
	URL string `json:"url"`
}

// cloudflareMarkdownResponse is the /markdown envelope: the rendered page lives
// in result as a markdown string when success is true.
type cloudflareMarkdownResponse struct {
	Success bool   `json:"success"`
	Result  string `json:"result"`
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
		Query: topic,
		Limit: firecrawlSearchLimit,
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
		if len(sources) >= firecrawlSearchLimit {
			break
		}
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

// crawlURLs reads each URL via Cloudflare Browser Rendering and returns one
// source per URL that succeeds. Best-effort: unreachable links are skipped.
// Empty when Cloudflare credentials are not configured.
func (p *Planner) crawlURLs(ctx context.Context, urls []string) []config.Source {
	account := strings.TrimSpace(p.env.CloudflareAccountID)
	token := strings.TrimSpace(p.env.CloudflareAPIToken)
	if account == "" || token == "" || len(urls) == 0 {
		return nil
	}
	out := make([]config.Source, 0, len(urls))
	for i, u := range urls {
		p.emit("read", fmt.Sprintf("Reading source %d of %d: %s", i+1, len(urls), shortURLForStatus(u)))
		if s, ok := p.crawlURL(ctx, account, token, u); ok {
			out = append(out, s)
			p.emit("sources", fmt.Sprintf("Read %d of %d source%s", len(out), len(urls), plural(len(out))))
		}
	}
	return out
}

// crawlURL renders a single URL to markdown via Cloudflare Browser Rendering.
// Unlike Firecrawl's async crawl, /markdown is synchronous — one POST returns
// the rendered markdown, so there is no job id to poll.
func (p *Planner) crawlURL(ctx context.Context, account, token, url string) (config.Source, bool) {
	body, _ := json.Marshal(cloudflareMarkdownRequest{URL: url})
	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf(cloudflareMarkdownURL, account)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return config.Source{}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return config.Source{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return config.Source{}, false
	}
	var parsed cloudflareMarkdownResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return config.Source{}, false
	}
	if !parsed.Success {
		return config.Source{}, false
	}
	return markdownToSource(parsed.Result, url)
}

func shortURLForStatus(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "source"
	}
	if len(raw) <= 64 {
		return raw
	}
	return raw[:61] + "..."
}

// markdownToSource builds a config.Source from Cloudflare-rendered markdown.
// Cloudflare returns only the markdown body (no separate title/description), so
// the title is taken from the first markdown heading, falling back to the URL.
func markdownToSource(markdown, url string) (config.Source, bool) {
	markdown = strings.TrimSpace(markdown)
	if strings.TrimSpace(url) == "" || markdown == "" {
		return config.Source{}, false
	}
	title := firstMarkdownHeading(markdown)
	if title == "" {
		title = url
	}
	return config.Source{
		Title:    title,
		URL:      url,
		Snippet:  truncate(markdown, 400),
		Markdown: markdown,
	}, true
}

// firstMarkdownHeading returns the text of the first ATX heading ("# ...") in
// the markdown, stripped of leading hashes, or "" when there is none.
func firstMarkdownHeading(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if h := strings.TrimSpace(strings.TrimLeft(line, "#")); h != "" {
				return h
			}
		}
	}
	return ""
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
