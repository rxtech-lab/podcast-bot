package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register decoder so models that return JPEG round-trip
	_ "image/png"  // register decoder for the common PNG case
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	xdraw "golang.org/x/image/draw"
)

// Client posts to image-generation endpoints and returns decoded image bytes.
// Construct via New; the zero value is not usable.
type Client struct {
	httpClient *http.Client
	apiKey     string
	geminiKey  string
}

// New builds a Client. apiKey may be empty — in that case New reads from
// AI_GATEWAY_API_KEY then OPENAI_API_KEY for gateway-routed image models, and
// GEMINI_API_KEY for Google's native Nano Banana models. Returns an error if
// no usable key is found.
func New(apiKey string) (*Client, error) {
	if apiKey == "" {
		apiKey = firstNonEmpty(
			envOrDotEnv("AI_GATEWAY_API_KEY"),
			envOrDotEnv("OPENAI_API_KEY"),
		)
	}
	geminiKey := envOrDotEnv("GEMINI_API_KEY")
	if apiKey == "" && geminiKey == "" {
		return nil, fmt.Errorf("imagegen: no API key (set AI_GATEWAY_API_KEY, OPENAI_API_KEY, or GEMINI_API_KEY)")
	}
	return &Client{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		apiKey:     apiKey,
		geminiKey:  geminiKey,
	}, nil
}

// Request is one image generation request.
type Request struct {
	Model       string
	Prompt      string
	Size        string // e.g. "1024x1024", "1536x1024"
	Quality     string // optional; "auto" / "low" / "medium" / "high" — gpt-image-* only
	Transparent bool   // gpt-image-* only — Gemini ignores
}

// Generate POSTs the request and returns raw decoded image bytes
// (PNG/JPEG depending on what the model returns; both are decodable by
// image.Decode below).
//
// Routing: Google's gemini-*-image models are sent through Google's native
// Interactions API when GEMINI_API_KEY is available. Older gateway-compatible
// models continue to use the OpenAI-compatible /v1/images/generations path.
//
// Transient failures (DNS/connection errors, 429, 5xx) are retried with
// exponential backoff up to maxRetryAttempts; permanent errors
// (auth/parse/decode/content-filter) fail immediately so we don't burn
// time on calls that can never succeed.
func (c *Client) Generate(ctx context.Context, req Request) ([]byte, error) {
	doOnce := func() ([]byte, error) {
		if isGeminiImageModel(req.Model) {
			if c.geminiKey == "" {
				return nil, fmt.Errorf("imagegen: Gemini image model requires GEMINI_API_KEY")
			}
			return c.generateGeminiInteractionImage(ctx, req)
		}
		return c.generateImage(ctx, req)
	}
	var lastErr error
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		if attempt > 0 {
			wait := retryBackoff(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		raw, err := doOnce()
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

// maxRetryAttempts caps how many times we'll retry a transient image-gen
// failure. Picked so a brief DNS / network hiccup (the failure mode that
// took out a whole puzzle's scene generation in 6 ms) recovers without
// blocking the orchestrator for too long on a sustained outage.
const maxRetryAttempts = 4

// retryBackoff is the wait before the (attempt+1)-th try: 750 ms, 1.5 s,
// 3 s. A linear-exponential schedule rather than full jitter — the upstream
// is a single Vercel gateway, so spreading retries across many goroutines
// has limited value, and predictable timing keeps logs interpretable.
func retryBackoff(attempt int) time.Duration {
	base := 750 * time.Millisecond
	mult := time.Duration(1) << (attempt - 1)
	return base * mult
}

// retryableError marks errors the Generate retry loop should re-attempt.
// Network failures (dial errors, DNS, mid-flight resets) and gateway
// 429/5xx wrap into this type; permanent errors (auth, parse, decode,
// content-filter) do not, so they fail fast.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func retryable(err error) error { return &retryableError{err: err} }

func isRetryable(err error) bool {
	var r *retryableError
	return errors.As(err, &r)
}

// isTransientStatus reports whether a gateway HTTP status code is worth
// retrying. 429 (rate-limit) and 5xx (server-side outage / bad gateway)
// are; 4xx (auth, bad request, content-filter) are not.
func isTransientStatus(code int) bool {
	if code == http.StatusTooManyRequests {
		return true
	}
	return code/100 == 5
}

func (c *Client) generateImage(ctx context.Context, req Request) ([]byte, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("imagegen: no gateway API key (set AI_GATEWAY_API_KEY or OPENAI_API_KEY)")
	}
	body := map[string]any{
		"model":           req.Model,
		"prompt":          req.Prompt,
		"n":               1,
		"size":            req.Size,
		"response_format": "b64_json",
	}
	if req.Transparent {
		body["background"] = "transparent"
	}
	if req.Quality != "" {
		body["quality"] = req.Quality
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, GatewayURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, retryable(err)
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		statusErr := fmt.Errorf("gateway %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
		if isTransientStatus(resp.StatusCode) {
			return nil, retryable(statusErr)
		}
		return nil, statusErr
	}

	var parsed struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w (body: %s)", err, truncate(string(rawResp), 400))
	}
	if len(parsed.Data) == 0 || parsed.Data[0].B64 == "" {
		return nil, fmt.Errorf("no image data in response (body: %s)", truncate(string(rawResp), 400))
	}
	return base64.StdEncoding.DecodeString(parsed.Data[0].B64)
}

// generateGeminiInteractionImage uses Google's native Interactions API for
// Nano Banana models. The current Gemini docs return the last generated image
// at output_image.data, while interleaved results can also appear under
// steps[].content[].
func (c *Client) generateGeminiInteractionImage(ctx context.Context, req Request) ([]byte, error) {
	body := map[string]any{
		"model": geminiModelName(req.Model),
		"input": []any{
			map[string]any{
				"type": "text",
				"text": req.Prompt,
			},
		},
		"response_format": geminiResponseFormat(req),
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, GeminiInteractionsURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-goog-api-key", c.geminiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, retryable(err)
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		statusErr := fmt.Errorf("gemini interactions %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
		if isTransientStatus(resp.StatusCode) {
			return nil, retryable(statusErr)
		}
		return nil, statusErr
	}
	b64 := geminiImageData(rawResp)
	if b64 == "" {
		return nil, fmt.Errorf("no image data in Gemini interactions response (body: %s)", truncate(string(rawResp), 400))
	}
	return base64.StdEncoding.DecodeString(b64)
}

func geminiModelName(model string) string {
	return strings.TrimPrefix(model, "google/")
}

func geminiResponseFormat(req Request) map[string]any {
	format := map[string]any{
		"type":      "image",
		"mime_type": "image/jpeg",
	}
	if ratio := aspectRatioFromSize(req.Size); ratio != "" {
		format["aspect_ratio"] = ratio
	}
	if tier := geminiImageSizeTier(req.Size); tier != "" {
		format["image_size"] = tier
	}
	return format
}

func geminiImageData(raw []byte) string {
	var parsed struct {
		OutputImage struct {
			Data string `json:"data"`
		} `json:"output_image"`
		Steps []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"content"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	if parsed.OutputImage.Data != "" {
		return parsed.OutputImage.Data
	}
	for _, step := range parsed.Steps {
		if step.Type != "" && step.Type != "model_output" {
			continue
		}
		for _, block := range step.Content {
			if block.Type == "image" && block.Data != "" {
				return block.Data
			}
		}
	}
	return ""
}

func aspectRatioFromSize(size string) string {
	w, h, ok := parsePixelSize(size)
	if !ok || w <= 0 || h <= 0 {
		return ""
	}
	g := gcd(w, h)
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

func geminiImageSizeTier(size string) string {
	w, h, ok := parsePixelSize(size)
	if !ok {
		return ""
	}
	longest := max(w, h)
	switch {
	case longest <= 512:
		return "512"
	case longest <= 1024:
		return "1K"
	case longest <= 2048:
		return "2K"
	case longest <= 4096:
		return "4K"
	default:
		return ""
	}
}

func parsePixelSize(size string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(size), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	var w, h int
	if _, err := fmt.Sscanf(parts[0], "%d", &w); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &h); err != nil {
		return 0, 0, false
	}
	return w, h, true
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// isGeminiImageModel reports whether the model slug routes through
// Google's native Interactions API when GEMINI_API_KEY is available.
func isGeminiImageModel(model string) bool {
	if !strings.HasPrefix(model, "google/gemini-") {
		return false
	}
	// Heuristic: "image" anywhere in the slug → image-capable.
	return strings.Contains(model, "image")
}

// DecodeAndResize decodes a PNG/JPEG byte slice and resamples it to (w, h)
// with Catmull-Rom — sharper than bilinear, cheap enough at our scales.
func DecodeAndResize(raw []byte, w, h int) (*image.RGBA, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if src.Bounds().Dx() == w && src.Bounds().Dy() == h {
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.Draw(out, out.Bounds(), src, src.Bounds().Min, xdraw.Src)
		return out, nil
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func envOrDotEnv(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	values, err := godotenv.Read()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values[key])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
