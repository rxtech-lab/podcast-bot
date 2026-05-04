// Package musicgen wraps Google's Lyria 3 Pro REST endpoint so the
// situation-puzzle channel can pre-generate atmospheric background music
// for the surface (湯面) and reveal (湯底) phases, mirroring the
// pre-generated scene images served by internal/video/scenes.
//
// Generation is one-shot: a single POST returns a base64-encoded mp3
// blob inside a generateContent response. Output is cached on disk
// keyed by sha1(prompt) so a re-run hits the cache instead of paying
// the API cost a second time.
package musicgen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// LyriaURL is the Generative Language REST endpoint for Lyria 3 Pro
// music generation. The Pro variant returns ~1–2 minutes of stereo audio
// per call, which is plenty for the surface/reveal monologues
// (45–60 s budget each — looped via the amix stage if shorter).
const LyriaURL = "https://generativelanguage.googleapis.com/v1beta/models/lyria-3-pro-preview:generateContent"

// Client posts to the Lyria endpoint with the user's GEMINI_API_KEY.
// Construct via New; the zero value is not usable.
type Client struct {
	httpClient *http.Client
	apiKey     string
}

// New builds a Client. apiKey may be empty — in that case New reads from
// GEMINI_API_KEY (matches the env field validated by config.LoadEnv).
// Returns an error if no key is found, since music generation is the
// only thing this client does.
func New(apiKey string) (*Client, error) {
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	}
	if apiKey == "" {
		return nil, fmt.Errorf("musicgen: no API key (set GEMINI_API_KEY)")
	}
	return &Client{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		apiKey:     apiKey,
	}, nil
}

// Request is one music-generation request. Only the prompt is required;
// the model decides duration. We always ask for mp3 because the rest of
// the pipeline already speaks mp3 — saving us a transcode pass before
// the amix stage.
type Request struct {
	Prompt string
}

// Generate POSTs the request and returns mp3 bytes. The response shape
// follows the standard generateContent contract: `inline_data.data` of
// the first audio part holds the base64 payload.
func (c *Client) Generate(ctx context.Context, req Request) ([]byte, error) {
	// Lyria 3 Pro returns mp3 by default. Specifying responseMimeType at all
	// (even "audio/mp3") trips INVALID_ARGUMENT — that field on
	// generateContent only accepts text MIME types (text/plain,
	// application/json, etc.). The only valid override for this model is
	// "audio/wav", which we don't want. So omit it entirely and rely on
	// the AUDIO modality to signal binary output.
	body := map[string]any{
		"contents": []any{
			map[string]any{
				"parts": []any{
					map[string]any{"text": req.Prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO", "TEXT"},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, LyriaURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-goog-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("lyria %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return nil, fmt.Errorf("parse lyria response: %w (body: %s)", err, truncate(string(rawResp), 400))
	}
	for _, cand := range parsed.Candidates {
		for _, part := range cand.Content.Parts {
			if part.InlineData.Data == "" {
				continue
			}
			return base64.StdEncoding.DecodeString(part.InlineData.Data)
		}
	}
	return nil, fmt.Errorf("no audio data in response (body: %s)", truncate(string(rawResp), 400))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
