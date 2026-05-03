package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Voice describes a single TTS voice exposed by a Provider. The shape is
// modelled on Azure's neural voice metadata; ElevenLabs voices are mapped
// onto the same struct (ShortName = voice_id, Locale set to the topic
// language since the multilingual model handles every locale).
type Voice struct {
	ShortName  string   `json:"ShortName"`
	Locale     string   `json:"Locale"`
	Gender     string   `json:"Gender"`
	VoiceType  string   `json:"VoiceType"`
	StyleList  []string `json:"StyleList,omitempty"`
	LocaleName string   `json:"LocaleName,omitempty"`
}

// FetchVoices retrieves the full list of available Azure neural voices.
// The `language` argument is ignored — Azure exposes one global list and the
// agent voice picker filters by locale. It is part of the Provider interface
// for parity with ElevenLabs which uses the hint to tag returned voices.
func (c *AzureClient) FetchVoices(ctx context.Context, language string) ([]Voice, error) {
	url := fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/voices/list", c.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", c.key)
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
