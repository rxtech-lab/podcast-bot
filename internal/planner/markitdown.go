package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// PDF/docx conversion can be slow, especially for large documents, so allow
// the service up to ten minutes to return extracted text.
const markitdownTimeout = 600 * time.Second

// Guard against a malformed or hostile pagination response keeping the
// backend in an unbounded fetch loop. At the service's default 5,000
// characters per page this still permits documents with roughly 50 million
// converted characters, well beyond expected converted document sizes.
const maxMarkitdownPages = 10_000

// markitdownRequest is the POST /convert body. The service fetches the file
// itself from the supplied URL — it is NOT a multipart upload — so the URL must
// be reachable by the markitdown server (e.g. a presigned S3 download URL).
type markitdownRequest struct {
	File string `json:"file"`
}

type markitdownResponse struct {
	ID         string               `json:"id"`
	Content    string               `json:"content"`
	Pagination markitdownPagination `json:"pagination"`
}

type markitdownPagination struct {
	ID         string `json:"id"`
	Page       int    `json:"page"`
	TotalPages int    `json:"total_pages"`
	HasNext    bool   `json:"has_next"`
	NextPage   *int   `json:"next_page"`
}

// ConvertFile asks the markitdown service to parse the file at fileURL into
// markdown. Paginated responses are resolved here before the markdown reaches
// an Attachment, so every downstream path receives one complete converted
// document before applying its own context bounds and never needs to understand
// the markitdown transport contract. fileURL must be publicly fetchable by the
// service. Returns an error when no markitdown server is configured so callers
// can surface "uploads not available" cleanly.
func ConvertFile(ctx context.Context, env *config.Env, fileURL string) (string, error) {
	if env == nil || strings.TrimSpace(env.MarkitdownServerURL) == "" {
		return "", fmt.Errorf("markitdown service not configured")
	}
	if strings.TrimSpace(fileURL) == "" {
		return "", fmt.Errorf("file url is required")
	}

	body, err := json.Marshal(markitdownRequest{File: fileURL})
	if err != nil {
		return "", fmt.Errorf("encode markitdown request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, markitdownTimeout)
	defer cancel()
	baseURL := strings.TrimRight(env.MarkitdownServerURL, "/")
	apiKey := strings.TrimSpace(env.MarkitdownAPIKey)
	parsed, err := fetchMarkitdownPage(reqCtx, http.MethodPost, baseURL+"/convert", apiKey, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	pages := []string{parsed.Content}
	pagination := parsed.Pagination
	if pagination.Page == 0 {
		pagination.Page = 1
	}
	if pagination.TotalPages > maxMarkitdownPages {
		return "", fmt.Errorf("markitdown returned too many pages: %d", pagination.TotalPages)
	}
	if !pagination.HasNext {
		if pagination.TotalPages > pagination.Page {
			return "", fmt.Errorf("markitdown pagination ended at page %d of %d", pagination.Page, pagination.TotalPages)
		}
		return parsed.Content, nil
	}

	docID := strings.TrimSpace(parsed.ID)
	if docID == "" {
		docID = strings.TrimSpace(pagination.ID)
	}
	if docID == "" {
		return "", fmt.Errorf("markitdown pagination is missing a document id")
	}
	seen := map[int]struct{}{pagination.Page: {}}
	totalPages := pagination.TotalPages

	for pagination.HasNext {
		if len(seen) >= maxMarkitdownPages {
			return "", fmt.Errorf("markitdown pagination exceeded %d pages", maxMarkitdownPages)
		}
		if pagination.NextPage == nil || *pagination.NextPage < 1 {
			return "", fmt.Errorf("markitdown page %d says more pages are available but has no next_page", pagination.Page)
		}
		nextPage := *pagination.NextPage
		if _, ok := seen[nextPage]; ok {
			return "", fmt.Errorf("markitdown pagination repeated page %d", nextPage)
		}
		if totalPages > 0 && nextPage > totalPages {
			return "", fmt.Errorf("markitdown next page %d exceeds total_pages %d", nextPage, totalPages)
		}

		pageURL := baseURL + "/convert/" + url.PathEscape(docID) + "/pages/" + strconv.Itoa(nextPage)
		next, err := fetchMarkitdownPage(reqCtx, http.MethodGet, pageURL, apiKey, nil)
		if err != nil {
			return "", fmt.Errorf("fetch markitdown page %d: %w", nextPage, err)
		}
		if id := strings.TrimSpace(next.ID); id != "" && id != docID {
			return "", fmt.Errorf("markitdown page %d returned document id %q, want %q", nextPage, id, docID)
		}
		if id := strings.TrimSpace(next.Pagination.ID); id != "" && id != docID {
			return "", fmt.Errorf("markitdown page %d pagination returned document id %q, want %q", nextPage, id, docID)
		}
		if next.Pagination.Page != 0 && next.Pagination.Page != nextPage {
			return "", fmt.Errorf("markitdown requested page %d but response was page %d", nextPage, next.Pagination.Page)
		}
		if next.Pagination.Page == 0 {
			next.Pagination.Page = nextPage
		}
		if next.Pagination.TotalPages > maxMarkitdownPages {
			return "", fmt.Errorf("markitdown returned too many pages: %d", next.Pagination.TotalPages)
		}
		if totalPages > 0 && next.Pagination.TotalPages > 0 && next.Pagination.TotalPages != totalPages {
			return "", fmt.Errorf("markitdown total_pages changed from %d to %d", totalPages, next.Pagination.TotalPages)
		}
		if totalPages == 0 {
			totalPages = next.Pagination.TotalPages
		}

		pages = append(pages, next.Content)
		seen[nextPage] = struct{}{}
		pagination = next.Pagination
	}

	if totalPages > 0 && len(seen) != totalPages {
		return "", fmt.Errorf("markitdown pagination returned %d of %d pages", len(seen), totalPages)
	}
	return strings.Join(pages, "\n\n"), nil
}

func fetchMarkitdownPage(ctx context.Context, method, endpoint, apiKey string, body io.Reader) (markitdownResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return markitdownResponse{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return markitdownResponse{}, fmt.Errorf("markitdown request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return markitdownResponse{}, fmt.Errorf("markitdown status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed markitdownResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return markitdownResponse{}, fmt.Errorf("decode markitdown response: %w", err)
	}
	return parsed, nil
}
