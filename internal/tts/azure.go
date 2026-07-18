package tts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OutputFormatRawPCM48k is the raw-PCM format requested from Azure. Azure has
// no stereo MP3 output formats, so we take mono 48 kHz PCM and locally encode
// to the pipeline's uniform 48 kHz/192 kbps stereo MP3 (encodePCMToStereoMP3)
// — a single lossy encode, same codec for every turn, so ffmpeg concat
// -c copy works.
const OutputFormatRawPCM48k = "raw-48khz-16bit-mono-pcm"

// AzureClient is an Azure TTS REST client.
type AzureClient struct {
	region   string
	key      string
	http     *http.Client
	endpoint string
}

// NewAzure constructs an AzureClient.
func NewAzure(region, key string) *AzureClient {
	return &AzureClient{
		region:   region,
		key:      key,
		http:     &http.Client{},
		endpoint: azureEndpoint(region),
	}
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
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	url := c.endpoint
	if url == "" {
		url = azureEndpoint(c.region)
	}

	var lastErr error
	for attempt := 0; attempt < azureRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(azureRetryBackoff(attempt)):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(ssml))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Ocp-Apim-Subscription-Key", c.key)
		req.Header.Set("Content-Type", "application/ssml+xml")
		req.Header.Set("X-Microsoft-OutputFormat", OutputFormatRawPCM48k)
		req.Header.Set("User-Agent", "debate-bot/0.1")

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("tts post: %w", err)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			// Azure streams raw PCM; encode to the pipeline's uniform stereo
			// MP3 on the fly (chunk-by-chunk, preserving stream-through).
			return encodePCMToStereoMP3(ctx, resp.Body, 48000)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("tts status %d: %s", resp.StatusCode, string(body))
		if !azureRetryableStatus(resp.StatusCode) {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", azureRetryAttempts, lastErr)
}

func azureEndpoint(region string) string {
	return fmt.Sprintf("https://%s.tts.speech.microsoft.com/cognitiveservices/v1", region)
}

const azureRetryAttempts = 4

var azureRetryBackoff = func(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 750 * time.Millisecond
	case 2:
		return 1500 * time.Millisecond
	default:
		return 3 * time.Second
	}
}

func azureRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}
