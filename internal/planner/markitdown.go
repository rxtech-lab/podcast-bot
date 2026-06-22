package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

// markitdownTimeout matches the budget linda-assistant uses: PDF/docx
// conversion can be slow, so allow a generous window.
const markitdownTimeout = 120 * time.Second

// markitdownRequest is the POST /convert body. The service fetches the file
// itself from the supplied URL — it is NOT a multipart upload — so the URL must
// be reachable by the markitdown server (e.g. a presigned S3 download URL).
type markitdownRequest struct {
	File string `json:"file"`
}

type markitdownResponse struct {
	Content string `json:"content"`
}

// ConvertFile asks the markitdown service to parse the file at fileURL into
// markdown. fileURL must be publicly fetchable by the service. Returns an error
// when no markitdown server is configured so callers can surface "uploads not
// available" cleanly.
func ConvertFile(ctx context.Context, env *config.Env, fileURL string) (string, error) {
	if env == nil || strings.TrimSpace(env.MarkitdownServerURL) == "" {
		return "", fmt.Errorf("markitdown service not configured")
	}
	if strings.TrimSpace(fileURL) == "" {
		return "", fmt.Errorf("file url is required")
	}

	body, _ := json.Marshal(markitdownRequest{File: fileURL})
	reqCtx, cancel := context.WithTimeout(ctx, markitdownTimeout)
	defer cancel()
	url := strings.TrimRight(env.MarkitdownServerURL, "/") + "/convert"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(env.MarkitdownAPIKey); key != "" {
		req.Header.Set("X-API-Key", key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("markitdown request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error bodies are plain text, not JSON.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("markitdown status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed markitdownResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode markitdown response: %w", err)
	}
	return parsed.Content, nil
}
