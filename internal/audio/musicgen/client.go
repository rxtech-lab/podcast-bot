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
	"errors"
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

// Request is one music-generation request. Only the prompt is required.
// DurationSeconds, when > 0, is folded into the prompt as an explicit
// length hint Lyria honours when picking output length. We always ask
// for mp3 because the rest of the pipeline already speaks mp3 — saving
// us a transcode pass before the amix stage.
type Request struct {
	Prompt          string
	DurationSeconds int
}

// Generate POSTs the request and returns mp3 bytes. The response shape
// follows the standard generateContent contract: `inline_data.data` of
// the first audio part holds the base64 payload.
//
// Transport failures (mid-flight EOF / connection reset / DNS) and
// 429/5xx responses retry with the same linear-exponential backoff as
// imagegen — Lyria's endpoint resets the TCP connection often enough
// that a single attempt was losing entire sessions to a transient blip
// (run.log 2026-05-05_01-57-22 lost both surface + reveal music to one
// such EOF). Auth, parse, and other 4xx failures fail fast.
func (c *Client) Generate(ctx context.Context, req Request) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryBackoff(attempt)):
			}
		}
		raw, err := c.generateOnce(ctx, req)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxRetryAttempts, lastErr)
}

// maxRetryAttempts caps how many Lyria calls we make per phase. Matches
// imagegen's setting; sustained Lyria outages would still surface as a
// "music gen partial" log, which the orchestrator already handles by
// degrading to dry TTS.
const maxRetryAttempts = 4

// retryBackoff: 750 ms, 1.5 s, 3 s before attempts 2, 3, 4. Same schedule
// as imagegen so logs read consistently across both gen subsystems.
func retryBackoff(attempt int) time.Duration {
	base := 750 * time.Millisecond
	mult := time.Duration(1) << (attempt - 1)
	return base * mult
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func retryable(err error) error { return &retryableError{err: err} }

func isRetryable(err error) bool {
	var r *retryableError
	return errors.As(err, &r)
}

// isTransientStatus mirrors imagegen — 429 (rate-limit) and 5xx (server
// outage / bad gateway) are worth retrying; all other 4xx are permanent.
func isTransientStatus(code int) bool {
	if code == http.StatusTooManyRequests {
		return true
	}
	return code/100 == 5
}

func (c *Client) generateOnce(ctx context.Context, req Request) ([]byte, error) {
	// Lyria 3 Pro returns mp3 by default. Specifying responseMimeType at all
	// (even "audio/mp3") trips INVALID_ARGUMENT — that field on
	// generateContent only accepts text MIME types (text/plain,
	// application/json, etc.). The only valid override for this model is
	// "audio/wav", which we don't want. So omit it entirely and rely on
	// the AUDIO modality to signal binary output.
	prompt := req.Prompt
	if req.DurationSeconds > 0 {
		prompt = strings.TrimRight(prompt, "\n ") +
			fmt.Sprintf("\n\nTarget length: approximately %d seconds.", req.DurationSeconds)
	}
	body := map[string]any{
		"contents": []any{
			map[string]any{
				"parts": []any{
					map[string]any{"text": prompt},
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
		return nil, retryable(err)
	}
	defer resp.Body.Close()

	rawResp, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// Body cut mid-stream after headers — same upstream-flake class
		// as a Do() error; retry rather than failing the phase outright.
		return nil, retryable(fmt.Errorf("read lyria body: %w", readErr))
	}
	if resp.StatusCode/100 != 2 {
		statusErr := fmt.Errorf("lyria %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
		if isTransientStatus(resp.StatusCode) {
			return nil, retryable(statusErr)
		}
		return nil, statusErr
	}

	var parsed lyriaResponse
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

	// No audio in any candidate. Lyria's copyright/safety filter returns
	// HTTP 200 with finishReason="OTHER" and a message that the content
	// "may contain material that resembles existing copyrighted works",
	// which is recoverable on retry — Lyria samples the latent space
	// stochastically, so the same prompt frequently produces a clean
	// generation on the next attempt. Mark these retryable so the outer
	// loop re-rolls instead of silently degrading the puzzle to dry TTS.
	if reason, msg, ok := parsed.retryableFinish(); ok {
		return nil, retryable(fmt.Errorf("lyria filter (finishReason=%s): %s", reason, msg))
	}
	return nil, fmt.Errorf("no audio data in response (body: %s)", truncate(string(rawResp), 400))
}

// lyriaResponse mirrors the candidates[].content.parts[].inlineData
// shape Gemini Generative Language returns for music generation, plus
// finishReason / finishMessage for filter-driven empty responses.
type lyriaResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
	} `json:"candidates"`
}

// retryableFinish reports whether any candidate carries a finishReason
// that's worth re-rolling. Today: "OTHER" (the bucket Lyria's copyright/
// safety filter falls into), "SAFETY", "RECITATION", "PROHIBITED_CONTENT"
// — all generation-time decisions that the same prompt re-rolled
// stochastically can clear. STOP / MAX_TOKENS would have produced audio,
// so we don't see those here; everything else is treated as permanent.
// Returns the offending reason + message for log readability.
func (p lyriaResponse) retryableFinish() (reason, message string, ok bool) {
	for _, c := range p.Candidates {
		switch strings.ToUpper(c.FinishReason) {
		case "OTHER", "SAFETY", "RECITATION", "PROHIBITED_CONTENT":
			return c.FinishReason, truncate(c.FinishMessage, 200), true
		}
	}
	return "", "", false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
