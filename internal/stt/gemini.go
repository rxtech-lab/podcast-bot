package stt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// geminiBase is the Generative Language REST base.
const geminiBase = "https://generativelanguage.googleapis.com"

// geminiInlineLimit is the largest audio we send inline as base64; bigger
// files go through the Files API (the request cap is 20 MB total).
const geminiInlineLimit = 18 << 20

// geminiTimeout bounds one transcription call; long audio takes minutes.
const geminiTimeout = 30 * time.Minute

// geminiFilePollInterval/geminiFilePollMax pace waiting for an uploaded file
// to become ACTIVE before it can be referenced from generateContent.
const (
	geminiFilePollInterval = 2 * time.Second
	geminiFilePollMax      = 5 * time.Minute
)

// Gemini transcribes by prompting a multimodal model for diarized,
// timestamped JSON. Word-level timings are not available; phrase offsets are
// model-estimated, so cue building falls back to proportional splitting.
type Gemini struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGemini builds the provider around the same key/model the voice-message
// transcription fallback already uses (GEMINI_API_KEY / GEMINI_TRANSCRIBE_MODEL).
func NewGemini(apiKey, model string) *Gemini {
	return &Gemini{
		apiKey: strings.TrimSpace(apiKey),
		model:  strings.TrimSpace(model),
		client: &http.Client{Timeout: geminiTimeout},
	}
}

func (g *Gemini) Name() string { return ProviderGemini }

// geminiSTTPrompt asks for the exact JSON shape geminiSTTSchema enforces.
const geminiSTTPrompt = `Transcribe this audio with speaker diarization. Rules:
- Identify distinct speakers and number them 1, 2, 3, ... in order of first appearance. Use at most %d speakers.
- Split the transcript into complete sentences or complete speaker turns. Never split one sentence at a comma, colon, or other clause punctuation.
- For each phrase, listen for the first and last spoken word and give its exact start offset and duration in milliseconds. Do not estimate timestamps from text length.
- Offsets must be non-decreasing and every phrase must end within the audio's real duration. Before responding, verify that no later phrase starts earlier than a preceding phrase.
- Keep the verbatim text with natural punctuation.
- Also report the total audio duration in milliseconds as durationMs.
Output only the JSON.`

// geminiSTTSchema is the responseSchema forcing structured output.
var geminiSTTSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"durationMs": map[string]any{"type": "integer"},
		"phrases": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"speaker":    map[string]any{"type": "integer"},
					"offsetMs":   map[string]any{"type": "integer"},
					"durationMs": map[string]any{"type": "integer"},
					"text":       map[string]any{"type": "string"},
				},
				"required": []string{"speaker", "offsetMs", "durationMs", "text"},
			},
		},
	},
	"required": []string{"durationMs", "phrases"},
}

// Transcribe downloads the audio, ships it to Gemini (inline when small,
// Files API otherwise) and decodes the structured transcription.
func (g *Gemini) Transcribe(ctx context.Context, req Request) (*Transcript, error) {
	if g.apiKey == "" {
		return nil, fmt.Errorf("gemini stt not configured (GEMINI_API_KEY)")
	}
	if g.model == "" {
		return nil, fmt.Errorf("gemini stt model not configured")
	}

	data, err := g.downloadAudio(ctx, req.AudioURL)
	if err != nil {
		return nil, err
	}
	mime := strings.TrimSpace(req.MIME)
	if mime == "" {
		mime = "audio/mp4"
	}

	var audioPart map[string]any
	if len(data) <= geminiInlineLimit {
		audioPart = map[string]any{"inline_data": map[string]any{
			"mime_type": mime,
			"data":      base64.StdEncoding.EncodeToString(data),
		}}
	} else {
		fileURI, err := g.uploadFile(ctx, data, mime)
		if err != nil {
			return nil, err
		}
		audioPart = map[string]any{"file_data": map[string]any{
			"mime_type": mime,
			"file_uri":  fileURI,
		}}
	}

	prompt := fmt.Sprintf(geminiSTTPrompt, ClampMaxSpeakers(req.MaxSpeakers))
	if lang := strings.TrimSpace(req.Language); lang != "" {
		prompt += "\nThe audio language is " + lang + "."
	}
	body := map[string]any{
		"contents": []any{map[string]any{
			"parts": []any{
				map[string]any{"text": prompt},
				audioPart,
			},
		}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"responseMimeType": "application/json",
			"responseSchema":   geminiSTTSchema,
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", geminiBase, g.model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-goog-api-key", g.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gemini response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gemini %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}
	return decodeGeminiResponse(raw)
}

// downloadAudio pulls the audio bytes from the presigned URL. Gemini needs
// the bytes locally either way (inline or Files upload).
func (g *Gemini) downloadAudio(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch audio: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch audio: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("audio is empty")
	}
	return data, nil
}

// uploadFile pushes the audio through the Files API (resumable, single shot)
// and waits until the file is ACTIVE. Returns the file URI to reference from
// generateContent.
func (g *Gemini) uploadFile(ctx context.Context, data []byte, mime string) (string, error) {
	startURL := geminiBase + "/upload/v1beta/files"
	meta, _ := json.Marshal(map[string]any{"file": map[string]any{"display_name": "podcast-audio"}})
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, bytes.NewReader(meta))
	if err != nil {
		return "", err
	}
	startReq.Header.Set("x-goog-api-key", g.apiKey)
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startReq.Header.Set("X-Goog-Upload-Command", "start")
	startReq.Header.Set("X-Goog-Upload-Header-Content-Length", strconv.Itoa(len(data)))
	startReq.Header.Set("X-Goog-Upload-Header-Content-Type", mime)
	startResp, err := g.client.Do(startReq)
	if err != nil {
		return "", fmt.Errorf("gemini file upload start: %w", err)
	}
	io.Copy(io.Discard, startResp.Body)
	startResp.Body.Close()
	if startResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gemini file upload start: status %d", startResp.StatusCode)
	}
	uploadURL := startResp.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		return "", fmt.Errorf("gemini file upload: missing upload url")
	}

	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	upReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	upReq.Header.Set("X-Goog-Upload-Offset", "0")
	upResp, err := g.client.Do(upReq)
	if err != nil {
		return "", fmt.Errorf("gemini file upload: %w", err)
	}
	defer upResp.Body.Close()
	upRaw, err := io.ReadAll(upResp.Body)
	if err != nil {
		return "", err
	}
	if upResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gemini file upload: status %d: %s", upResp.StatusCode, truncate(string(upRaw), 300))
	}
	var upDoc struct {
		File struct {
			Name  string `json:"name"`
			URI   string `json:"uri"`
			State string `json:"state"`
		} `json:"file"`
	}
	if err := json.Unmarshal(upRaw, &upDoc); err != nil {
		return "", fmt.Errorf("parse gemini upload response: %w", err)
	}
	if upDoc.File.URI == "" {
		return "", fmt.Errorf("gemini file upload: missing file uri")
	}
	return g.waitFileActive(ctx, upDoc.File.Name, upDoc.File.URI, upDoc.File.State)
}

// waitFileActive polls the file until Gemini finishes processing it.
func (g *Gemini) waitFileActive(ctx context.Context, name, uri, state string) (string, error) {
	deadline := time.Now().Add(geminiFilePollMax)
	for state == "PROCESSING" {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("gemini file processing timed out")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(geminiFilePollInterval):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiBase+"/v1beta/"+name, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("x-goog-api-key", g.apiKey)
		resp, err := g.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("gemini file status: %w", err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		var doc struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", fmt.Errorf("parse gemini file status: %w", err)
		}
		state = doc.State
	}
	if state != "" && state != "ACTIVE" {
		return "", fmt.Errorf("gemini file state %s", state)
	}
	return uri, nil
}

// decodeGeminiResponse extracts the structured transcription from the first
// candidate's text part.
func decodeGeminiResponse(raw []byte) (*Transcript, error) {
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse gemini response: %w", err)
	}
	var sb strings.Builder
	for _, cand := range parsed.Candidates {
		for _, part := range cand.Content.Parts {
			sb.WriteString(part.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil, fmt.Errorf("gemini returned no transcription")
	}
	var doc struct {
		DurationMS int64 `json:"durationMs"`
		Phrases    []struct {
			Speaker    int    `json:"speaker"`
			OffsetMS   int64  `json:"offsetMs"`
			DurationMS int64  `json:"durationMs"`
			Text       string `json:"text"`
		} `json:"phrases"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("parse gemini transcription json: %w", err)
	}
	t := &Transcript{DurationMS: doc.DurationMS}
	for _, p := range doc.Phrases {
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		t.Phrases = append(t.Phrases, Phrase{
			Speaker:    p.Speaker,
			OffsetMS:   p.OffsetMS,
			DurationMS: p.DurationMS,
			Text:       p.Text,
		})
	}
	return t, nil
}
