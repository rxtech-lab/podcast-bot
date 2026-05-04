package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // register decoder so models that return JPEG round-trip
	_ "image/png"  // register decoder for the common PNG case
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
)

// Client posts to the AI Gateway image endpoint and returns decoded image
// bytes. Construct via New; the zero value is not usable.
type Client struct {
	httpClient *http.Client
	apiKey     string
}

// New builds a Client. apiKey may be empty — in that case New reads from
// AI_GATEWAY_API_KEY then OPENAI_API_KEY (the same env vars the rest of the
// bot already uses for the gateway). Returns an error if no key is found.
func New(apiKey string) (*Client, error) {
	if apiKey == "" {
		apiKey = firstNonEmpty(
			os.Getenv("AI_GATEWAY_API_KEY"),
			os.Getenv("OPENAI_API_KEY"),
		)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("imagegen: no API key (set AI_GATEWAY_API_KEY or OPENAI_API_KEY)")
	}
	return &Client{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		apiKey:     apiKey,
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
// Routing: Google's gemini-*-image models are surfaced by the gateway
// through chat completions with modalities=["image"], not the
// /v1/images/generations endpoint. Generate auto-detects those models and
// dispatches the correct request shape.
func (c *Client) Generate(ctx context.Context, req Request) ([]byte, error) {
	if isGeminiImageModel(req.Model) {
		return c.generateChatImage(ctx, req)
	}
	return c.generateImage(ctx, req)
}

func (c *Client) generateImage(ctx context.Context, req Request) ([]byte, error) {
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
		return nil, err
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gateway %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
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

// generateChatImage hits /v1/chat/completions with modalities=["image"]
// — the route Gemini's flash-image models use through the AI Gateway.
// The image returns inside choices[0].message.images[0].image_url.url as
// a data: URL.
func (c *Client) generateChatImage(ctx context.Context, req Request) ([]byte, error) {
	body := map[string]any{
		"model": req.Model,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": req.Prompt,
			},
		},
		"modalities": []string{"image"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, GatewayChatURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gateway chat %d: %s", resp.StatusCode, truncate(string(rawResp), 400))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Images []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return nil, fmt.Errorf("parse chat response: %w (body: %s)", err, truncate(string(rawResp), 400))
	}
	if len(parsed.Choices) == 0 || len(parsed.Choices[0].Message.Images) == 0 {
		return nil, fmt.Errorf("no image data in chat response (body: %s)", truncate(string(rawResp), 400))
	}
	url := parsed.Choices[0].Message.Images[0].ImageURL.URL
	return decodeDataURL(url)
}

// decodeDataURL parses a data:image/...;base64,... URL and returns the
// decoded bytes. Anything else is rejected.
func decodeDataURL(s string) ([]byte, error) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return nil, fmt.Errorf("not a data URL")
	}
	comma := strings.Index(s, ",")
	if comma < 0 {
		return nil, fmt.Errorf("malformed data URL: missing comma")
	}
	meta := s[len(prefix):comma]
	if !strings.Contains(meta, "base64") {
		return nil, fmt.Errorf("data URL not base64-encoded")
	}
	return base64.StdEncoding.DecodeString(s[comma+1:])
}

// isGeminiImageModel reports whether the model slug routes through
// /v1/chat/completions with image modalities instead of the dedicated
// images endpoint.
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
