package tts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OutputFormatMP3_24k48 is the chunked-streamable mp3 format used throughout
// the project. Same codec for every turn means ffmpeg concat -c copy works.
const OutputFormatMP3_24k48 = "audio-24khz-48kbitrate-mono-mp3"

// AzureClient is an Azure TTS REST client.
type AzureClient struct {
	region string
	key    string
	http   *http.Client
}

// NewAzure constructs an AzureClient.
func NewAzure(region, key string) *AzureClient {
	return &AzureClient{region: region, key: key, http: &http.Client{}}
}

// SynthesizeStream POSTs SSML for `text` and returns the chunked MP3 body.
// The caller MUST Close the returned reader.
func (c *AzureClient) SynthesizeStream(ctx context.Context, voice, text, lang string) (io.ReadCloser, error) {
	return c.postSSML(ctx, BuildSSML(voice, text, lang))
}

// SynthesizeSSML POSTs a caller-supplied SSML envelope verbatim. Lets the
// pipeline emit Azure's multi-voice SSML (one <speak> with several
// <voice> elements) so a series episode's narrator + character lines
// render in a single TTS call.
func (c *AzureClient) SynthesizeSSML(ctx context.Context, ssml string) (io.ReadCloser, error) {
	if strings.TrimSpace(ssml) == "" {
		return nil, fmt.Errorf("azure: empty ssml")
	}
	return c.postSSML(ctx, ssml)
}

func (c *AzureClient) postSSML(ctx context.Context, ssml string) (io.ReadCloser, error) {
	url := fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", c.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(ssml))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", c.key)
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", OutputFormatMP3_24k48)
	req.Header.Set("User-Agent", "debate-bot/0.1")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts post: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("tts status %d: %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}
