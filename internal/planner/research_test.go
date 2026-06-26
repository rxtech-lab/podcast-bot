package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestSearchSourcesLimitsResultsWithoutScraping(t *testing.T) {
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })

	var got map[string]any
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if req.URL.String() != firecrawlSearchURL {
			t.Fatalf("url = %s, want %s", req.URL.String(), firecrawlSearchURL)
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer token", auth)
		}
		if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		docs := make([]firecrawlDoc, 12)
		for i := range docs {
			docs[i] = firecrawlDoc{
				URL:         fmt.Sprintf("https://example.com/source-%02d", i+1),
				Title:       fmt.Sprintf("Source %02d", i+1),
				Description: fmt.Sprintf("Snippet %02d", i+1),
			}
		}
		body, err := json.Marshal(firecrawlSearchResponse{
			Success: true,
			Data: struct {
				Web []firecrawlDoc `json:"web"`
			}{Web: docs},
		})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    req,
		}, nil
	})}

	p := &Planner{env: &config.Env{FirecrawlAPIKey: "test-key"}}
	sources, err := p.SearchSources(context.Background(), "ai policy")
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(sources) != firecrawlSearchLimit {
		t.Fatalf("sources length = %d, want %d", len(sources), firecrawlSearchLimit)
	}
	if sources[0].URL != "https://example.com/source-01" {
		t.Fatalf("first source URL = %q", sources[0].URL)
	}
	if sources[len(sources)-1].URL != "https://example.com/source-10" {
		t.Fatalf("last source URL = %q, want source-10", sources[len(sources)-1].URL)
	}

	if got["query"] != "ai policy" {
		t.Fatalf("query = %v, want ai policy", got["query"])
	}
	if got["limit"] != float64(firecrawlSearchLimit) {
		t.Fatalf("limit = %v, want %d", got["limit"], firecrawlSearchLimit)
	}
	if _, ok := got["scrapeOptions"]; ok {
		t.Fatalf("request body included scrapeOptions: %+v", got)
	}
}

func TestResearchPapersUsesFirecrawlResearchIndex(t *testing.T) {
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if req.URL.Path != "/v2/search/research/papers" {
			t.Fatalf("path = %s, want research papers endpoint", req.URL.Path)
		}
		if got := req.URL.Query().Get("query"); got != "classroom ai tutoring" {
			t.Fatalf("query = %q, want classroom ai tutoring", got)
		}
		if got := req.URL.Query().Get("k"); got != "10" {
			t.Fatalf("k = %q, want 10", got)
		}
		if auth := req.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want bearer token", auth)
		}
		body := `{"success":true,"data":{"papers":[{"paperId":"p1","primaryId":"arxiv:2401.00001","title":"AI Tutoring in Classrooms","abstract":"Evidence on classroom AI tutoring.","authors":["Ada Lovelace"],"year":2024}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	p := &Planner{env: &config.Env{FirecrawlAPIKey: "test-key"}}
	sources, ok := p.researchPapers(context.Background(), "classroom ai tutoring")
	if !ok {
		t.Fatal("researchPapers returned !ok")
	}
	if len(sources) != 1 {
		t.Fatalf("sources length = %d, want 1", len(sources))
	}
	if sources[0].URL != "https://arxiv.org/abs/2401.00001" {
		t.Fatalf("source URL = %q, want arxiv URL", sources[0].URL)
	}
	if !strings.Contains(sources[0].Markdown, "AI Tutoring in Classrooms") {
		t.Fatalf("source markdown missing title: %q", sources[0].Markdown)
	}
}

func TestReadResearchPaperUsesPaperPassageEndpoint(t *testing.T) {
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })

	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if req.URL.Path != "/v2/search/research/papers/arxiv:2401.00001" {
			t.Fatalf("path = %s, want paper endpoint", req.URL.Path)
		}
		if got := req.URL.Query().Get("query"); got != "learning outcomes" {
			t.Fatalf("query = %q, want learning outcomes", got)
		}
		if got := req.URL.Query().Get("k"); got != "4" {
			t.Fatalf("k = %q, want 4", got)
		}
		body := `{"success":true,"data":{"paper":{"primaryId":"arxiv:2401.00001","title":"AI Tutoring in Classrooms","abstract":"Abstract text."},"passages":[{"section":"Results","text":"Students improved quiz scores."}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	p := &Planner{env: &config.Env{FirecrawlAPIKey: "test-key"}}
	source, ok := p.readResearchPaper(context.Background(), "arxiv:2401.00001", "learning outcomes")
	if !ok {
		t.Fatal("readResearchPaper returned !ok")
	}
	if !strings.Contains(source.Markdown, "Results: Students improved quiz scores.") {
		t.Fatalf("source markdown missing passage: %q", source.Markdown)
	}
	if !strings.Contains(source.Snippet, "Students improved quiz scores") {
		t.Fatalf("source snippet missing passage: %q", source.Snippet)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
