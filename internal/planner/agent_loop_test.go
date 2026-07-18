package planner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

const validCreatePlanArgs = `{
  "title": "A Test Panel",
  "background": "A grounded background for the discussion.",
  "host": { "name": "Morgan" },
  "discussants": [
    { "name": "Ari", "aspect": "technical" },
    { "name": "Blair", "aspect": "social" }
  ]
}`

func TestPlanningToolSessionRequiresSearchBeforeCreatePlan(t *testing.T) {
	session := &planningToolSession{
		planner:          &Planner{env: &config.Env{}},
		researchRequired: true,
		readURLs:         map[string]bool{},
	}

	got, terminal, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs)
	if err != nil {
		t.Fatalf("create_plan before search returned error: %v", err)
	}
	if terminal {
		t.Fatal("create_plan before search should not be terminal")
	}
	if !strings.Contains(got, "call web_search") {
		t.Fatalf("create_plan before search = %q, want blocked search message", got)
	}
	if session.finalized {
		t.Fatal("create_plan should not accept a final plan before web_search")
	}

	if got, terminal, err := session.dispatch(context.Background(), "web_search", `{"query":"test topic"}`); err != nil {
		t.Fatalf("web_search without key returned error: %v", err)
	} else if terminal {
		t.Fatal("web_search should not be terminal")
	} else if !strings.Contains(got, "unavailable") {
		t.Fatalf("web_search without key = %q, want unavailable message", got)
	}

	if got, terminal, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs); err != nil {
		t.Fatalf("create_plan after search returned error: %v", err)
	} else if !terminal {
		t.Fatal("accepted create_plan should be terminal")
	} else if got != "plan accepted" {
		t.Fatalf("create_plan after search = %q, want accepted", got)
	}
	if !session.finalized {
		t.Fatal("final plan not captured")
	}
	if d, err := decodeDraft(session.finalArgs); err != nil || d.Title != "A Test Panel" {
		t.Fatalf("final plan args not captured: %v %+v", err, d)
	}
	if _, _, err := session.dispatch(context.Background(), "web_search", `{"query":"after final"}`); err == nil {
		t.Fatal("tool call after accepted create_plan should be rejected")
	}
}

func TestPlanningToolSessionRequiresReadURLBeforeCreatePlan(t *testing.T) {
	url := "https://example.com/report"
	session := &planningToolSession{
		planner:      &Planner{env: &config.Env{}},
		requiredURLs: []string{url},
		readURLs:     map[string]bool{},
	}

	got, terminal, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs)
	if err != nil {
		t.Fatalf("create_plan before read_url returned error: %v", err)
	}
	if terminal {
		t.Fatal("create_plan before read_url should not be terminal")
	}
	if !strings.Contains(got, "call read_url") {
		t.Fatalf("create_plan before read_url = %q, want blocked URL message", got)
	}

	if got, terminal, err := session.dispatch(context.Background(), "read_url", `{"url":"https://example.com/report"}`); err != nil {
		t.Fatalf("read_url without key returned error: %v", err)
	} else if terminal {
		t.Fatal("read_url should not be terminal")
	} else if !strings.Contains(got, "unavailable") {
		t.Fatalf("read_url without key = %q, want unavailable message", got)
	}

	if got, terminal, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs); err != nil {
		t.Fatalf("create_plan after read_url returned error: %v", err)
	} else if !terminal {
		t.Fatal("accepted create_plan should be terminal")
	} else if got != "plan accepted" {
		t.Fatalf("create_plan after read_url = %q, want accepted", got)
	}
}

func TestPlanningToolSessionRequiresSuccessfulURLReadWhenConfigured(t *testing.T) {
	url := "https://example.com/report"
	session := &planningToolSession{
		planner:                  &Planner{env: &config.Env{}},
		requiredURLs:             []string{url},
		requireSuccessfulURLRead: true,
		readURLs:                 map[string]bool{},
		successfulURLReads:       map[string]bool{},
	}

	if got, terminal, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs); err != nil {
		t.Fatalf("create_plan before read_url returned error: %v", err)
	} else if terminal {
		t.Fatal("create_plan before read_url should not be terminal")
	} else if !strings.Contains(got, "call read_url") {
		t.Fatalf("create_plan before read_url = %q, want blocked URL message", got)
	}

	if _, _, err := session.dispatch(context.Background(), "read_url", `{"url":"https://example.com/report"}`); err != nil {
		t.Fatalf("read_url without key returned error: %v", err)
	}
	if _, _, err := session.dispatch(context.Background(), "create_plan", validCreatePlanArgs); err == nil {
		t.Fatal("create_plan should fail when every added link was unreadable")
	} else if !strings.Contains(err.Error(), "none of the added links could be read") {
		t.Fatalf("create_plan error = %v, want unreadable links error", err)
	}
}

func TestValidateCreatePlanTerminal(t *testing.T) {
	if err := validateCreatePlanTerminal([]llm.ToolCall{
		{Name: "create_plan"},
	}); err != nil {
		t.Fatalf("valid terminal create_plan rejected: %v", err)
	}
	if err := validateCreatePlanTerminal([]llm.ToolCall{
		{Name: "web_search"},
		{Name: "create_plan"},
	}); err == nil {
		t.Fatal("create_plan batched with research should be rejected")
	}
	if err := validateCreatePlanTerminal([]llm.ToolCall{
		{Name: "create_plan"},
		{Name: "web_search"},
	}); err == nil {
		t.Fatal("create_plan followed by another tool should be rejected")
	}
	if err := validateCreatePlanTerminal([]llm.ToolCall{
		{Name: "create_plan"},
		{Name: "create_plan"},
	}); err == nil {
		t.Fatal("duplicate create_plan should be rejected")
	}
}

func TestMergeSourcesDoesNotCapPersistedSources(t *testing.T) {
	base := make([]config.Source, 10)
	for i := range base {
		base[i] = config.Source{
			Title: fmt.Sprintf("Existing %d", i),
			URL:   fmt.Sprintf("https://example.com/existing-%d", i),
		}
	}
	added := []config.Source{
		{Title: "New 1", URL: "https://example.com/new-1"},
		{Title: "Duplicate", URL: "https://example.com/existing-3"},
		{Title: "Blank", URL: " "},
		{Title: "New 2", URL: "https://example.com/new-2"},
	}

	got := mergeSources(base, added)
	if len(got) != 12 {
		t.Fatalf("mergeSources length = %d, want 12", len(got))
	}
	for _, url := range []string{"https://example.com/new-1", "https://example.com/new-2"} {
		if countSourceURL(got, url) != 1 {
			t.Fatalf("mergeSources should keep added URL %s exactly once: %+v", url, got)
		}
	}
	if countSourceURL(got, "https://example.com/existing-3") != 1 {
		t.Fatalf("mergeSources should dedupe existing URLs: %+v", got)
	}
}

func TestDedupeURLsDoesNotCapAddedLinks(t *testing.T) {
	urls := make([]string, 0, 13)
	for i := 0; i < 12; i++ {
		urls = append(urls, fmt.Sprintf("https://example.com/source-%d", i))
	}
	urls = append(urls, "https://example.com/source-11.")

	got := dedupeURLs(urls)
	if len(got) != 12 {
		t.Fatalf("dedupeURLs length = %d, want 12", len(got))
	}
	if got[11] != "https://example.com/source-11" {
		t.Fatalf("dedupeURLs last URL = %q, want source-11", got[11])
	}
}

func countSourceURL(sources []config.Source, url string) int {
	count := 0
	for _, source := range sources {
		if source.URL == url {
			count++
		}
	}
	return count
}
