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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
