package planner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestConvertFileSinglePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/convert" {
			t.Errorf("request = %s %s, want POST /convert", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "secret" {
			t.Errorf("X-API-Key = %q, want secret", got)
		}
		var request markitdownRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.File != "https://example.com/book.pdf" {
			t.Errorf("file = %q", request.File)
		}
		writeMarkitdownTestJSON(t, w, map[string]any{
			"id":      "single",
			"content": "only page",
			"pagination": map[string]any{
				"id":          "single",
				"page":        1,
				"total_pages": 1,
				"has_next":    false,
				"next_page":   nil,
			},
		})
	}))
	defer server.Close()

	got, err := ConvertFile(t.Context(), markitdownTestEnv(server.URL), "https://example.com/book.pdf")
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	if got != "only page" {
		t.Fatalf("content = %q, want only page", got)
	}
}

func TestConvertFileFetchesEveryPageInOrder(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("X-API-Key"); got != "secret" {
			t.Errorf("X-API-Key = %q, want secret", got)
		}

		switch r.URL.Path {
		case "/convert":
			writeMarkitdownPage(t, w, "book-123", 1, 3, "first page", true, 2)
		case "/convert/book-123/pages/2":
			writeMarkitdownPage(t, w, "book-123", 2, 3, "second page", true, 3)
		case "/convert/book-123/pages/3":
			writeMarkitdownPage(t, w, "book-123", 3, 3, "third page", false, 0)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := ConvertFile(t.Context(), markitdownTestEnv(server.URL), "https://example.com/book.pdf")
	if err != nil {
		t.Fatalf("ConvertFile: %v", err)
	}
	if want := "first page\n\nsecond page\n\nthird page"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}

	mu.Lock()
	defer mu.Unlock()
	wantRequests := []string{
		"POST /convert",
		"GET /convert/book-123/pages/2",
		"GET /convert/book-123/pages/3",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", requests, wantRequests)
	}
}

func TestConvertFileRejectsPartialPaginatedContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/convert":
			writeMarkitdownPage(t, w, "expired", 1, 2, "partial page", true, 2)
		case "/convert/expired/pages/2":
			http.Error(w, "Document or page not found (it may have expired).", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	got, err := ConvertFile(t.Context(), markitdownTestEnv(server.URL), "https://example.com/book.pdf")
	if err == nil {
		t.Fatal("ConvertFile succeeded with partial paginated content")
	}
	if got != "" {
		t.Fatalf("content = %q, want empty on pagination failure", got)
	}
	if message := err.Error(); !strings.Contains(message, "fetch markitdown page 2") || !strings.Contains(message, "status 404") {
		t.Fatalf("error = %q, want page and status context", message)
	}
}

func TestConvertFileRejectsIncompletePaginationMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeMarkitdownTestJSON(t, w, map[string]any{
			"id":      "incomplete",
			"content": "partial page",
			"pagination": map[string]any{
				"id":          "incomplete",
				"page":        1,
				"total_pages": 2,
				"has_next":    false,
				"next_page":   nil,
			},
		})
	}))
	defer server.Close()

	_, err := ConvertFile(t.Context(), markitdownTestEnv(server.URL), "https://example.com/book.pdf")
	if err == nil || !strings.Contains(err.Error(), "ended at page 1 of 2") {
		t.Fatalf("error = %v, want incomplete pagination error", err)
	}
}

func markitdownTestEnv(serverURL string) *config.Env {
	return &config.Env{MarkitdownServerURL: serverURL, MarkitdownAPIKey: "secret"}
}

func writeMarkitdownPage(t *testing.T, w http.ResponseWriter, id string, page, totalPages int, content string, hasNext bool, nextPage int) {
	t.Helper()
	var next any
	if hasNext {
		next = nextPage
	}
	writeMarkitdownTestJSON(t, w, map[string]any{
		"id":      id,
		"content": content,
		"pagination": map[string]any{
			"id":          id,
			"page":        page,
			"total_pages": totalPages,
			"has_next":    hasNext,
			"next_page":   next,
		},
	})
}

func writeMarkitdownTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
