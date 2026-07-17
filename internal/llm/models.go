package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ModelEntry is one model advertised by the gateway's GET /models endpoint.
// Type is the gateway's model kind ("language", "embedding", "image", ...);
// it is empty when the endpoint doesn't include the field (plain
// OpenAI-compatible servers), in which case callers should treat the model as
// a chat model.
type ModelEntry struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
}

// ListModelEntries returns the models advertised by the OpenAI-compatible
// gateway at baseURL (its GET /models endpoint) with each entry's type. It is
// used to populate the model pickers with whatever the gateway actually
// serves, rather than a hard-coded roster, and the type lets callers split
// chat models from embedding models. Fetched with a plain HTTP GET because
// the typed openai-go Model drops the gateway's extra `type` field. Entries
// are returned in the order the gateway lists them.
func ListModelEntries(ctx context.Context, baseURL, apiKey string) ([]ModelEntry, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list models: %s returned %s", url, resp.Status)
	}
	var body struct {
		Data []ModelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("list models: decode: %w", err)
	}
	entries := make([]ModelEntry, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			entries = append(entries, m)
		}
	}
	return entries, nil
}
