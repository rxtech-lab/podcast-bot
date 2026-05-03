package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Voice is a single Azure neural TTS voice.
type Voice struct {
	ShortName  string   `json:"ShortName"`
	Locale     string   `json:"Locale"`
	Gender     string   `json:"Gender"`
	VoiceType  string   `json:"VoiceType"`
	StyleList  []string `json:"StyleList,omitempty"`
	LocaleName string   `json:"LocaleName,omitempty"`
}

// FetchVoices retrieves the full list of available voices from Azure.
// One call per process at startup; the list is then cached in memory.
func FetchVoices(ctx context.Context, region, key string) ([]Voice, error) {
	url := fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/voices/list", region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch voices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("voices list status %d: %s", resp.StatusCode, string(body))
	}
	var out []Voice
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode voices: %w", err)
	}
	return out, nil
}
