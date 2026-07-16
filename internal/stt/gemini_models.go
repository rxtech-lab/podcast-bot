package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GeminiModel is one selectable transcription model from the Gemini catalog.
type GeminiModel struct {
	// ID is the bare model id used in generateContent paths, e.g.
	// "gemini-2.5-flash" (the API's "models/" prefix stripped).
	ID          string
	DisplayName string
}

// geminiListTimeout bounds the admin-facing catalog fetch.
const geminiListTimeout = 15 * time.Second

// ListGeminiAudioModels fetches the Gemini model catalog and returns the
// models usable for audio transcription: generateContent-capable core Gemini
// models. The API does not advertise input modalities directly, so specialty
// variants that cannot read audio files (embeddings, image/video generation,
// TTS, live-audio dialog) are filtered out by family name.
func ListGeminiAudioModels(ctx context.Context, apiKey string) ([]GeminiModel, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("gemini api key is not configured")
	}
	client := &http.Client{Timeout: geminiListTimeout}
	var out []GeminiModel
	pageToken := ""
	for {
		u := geminiBase + "/v1beta/models?pageSize=200"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("gemini list models: %w", err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read gemini models: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("gemini list models %d: %s", resp.StatusCode, truncate(string(raw), 300))
		}
		var doc struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse gemini models: %w", err)
		}
		for _, m := range doc.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if !geminiModelSupportsAudio(id, m.SupportedGenerationMethods) {
				continue
			}
			display := strings.TrimSpace(m.DisplayName)
			if display == "" {
				display = id
			}
			out = append(out, GeminiModel{ID: id, DisplayName: display})
		}
		if doc.NextPageToken == "" {
			return out, nil
		}
		pageToken = doc.NextPageToken
	}
}

// geminiModelSupportsAudio filters the catalog down to audio-transcription
// candidates: core Gemini generateContent models, excluding the specialty
// families that cannot take an audio file as input.
func geminiModelSupportsAudio(id string, methods []string) bool {
	lower := strings.ToLower(id)
	if !strings.HasPrefix(lower, "gemini-") {
		return false
	}
	supportsGenerate := false
	for _, m := range methods {
		if m == "generateContent" {
			supportsGenerate = true
			break
		}
	}
	if !supportsGenerate {
		return false
	}
	for _, excluded := range []string{
		"embedding", "-tts", "image", "vision", "live", "audio-dialog",
		"native-audio", "veo", "aqa", "gemma",
	} {
		if strings.Contains(lower, excluded) {
			return false
		}
	}
	return true
}
