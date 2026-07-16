package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// azureFastAPIVersion pins the fast-transcription REST API version.
const azureFastAPIVersion = "2025-10-15"

// azureFastTimeout bounds one synchronous fast-transcription call. Azure
// processes faster than real time, but a multi-hour file still takes minutes.
const azureFastTimeout = 30 * time.Minute

// AzureFast calls the Azure Speech fast-transcription endpoint
// (POST {endpoint}/speechtotext/transcriptions:transcribe). It is synchronous
// on Azure's side and returns diarized phrases with word-level timings.
type AzureFast struct {
	endpoint string
	key      string
	client   *http.Client
}

// NewAzureFast builds the provider. endpoint is the full resource endpoint
// (https://{resource}.cognitiveservices.azure.com, from AZURE_SPEECH_ENDPOINT);
// when empty it is derived from the region the TTS integration already uses.
func NewAzureFast(endpoint, region, key string) *AzureFast {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" && strings.TrimSpace(region) != "" {
		endpoint = fmt.Sprintf("https://%s.api.cognitive.microsoft.com", strings.TrimSpace(region))
	}
	return &AzureFast{
		endpoint: endpoint,
		key:      strings.TrimSpace(key),
		client:   &http.Client{Timeout: azureFastTimeout},
	}
}

func (a *AzureFast) Name() string { return ProviderAzure }

// azureFastResponse mirrors the fast-transcription response document.
type azureFastResponse struct {
	DurationMilliseconds int64 `json:"durationMilliseconds"`
	Phrases              []struct {
		Speaker              int     `json:"speaker"`
		OffsetMilliseconds   int64   `json:"offsetMilliseconds"`
		DurationMilliseconds int64   `json:"durationMilliseconds"`
		Text                 string  `json:"text"`
		Locale               string  `json:"locale"`
		Confidence           float64 `json:"confidence"`
		Words                []struct {
			Text                 string `json:"text"`
			OffsetMilliseconds   int64  `json:"offsetMilliseconds"`
			DurationMilliseconds int64  `json:"durationMilliseconds"`
		} `json:"words"`
	} `json:"phrases"`
}

// Transcribe streams the audio behind req.AudioURL into a multipart upload to
// the fast-transcription endpoint. The audio is piped (never buffered whole)
// so multi-hundred-MB files don't spike memory.
func (a *AzureFast) Transcribe(ctx context.Context, req Request) (*Transcript, error) {
	if a.endpoint == "" || a.key == "" {
		return nil, fmt.Errorf("azure stt not configured (need AZURE_SPEECH_ENDPOINT or AZURE_SPEECH_REGION and AZURE_SPEECH_KEY)")
	}

	definition := map[string]any{
		"diarization": map[string]any{
			"enabled":     true,
			"maxSpeakers": ClampMaxSpeakers(req.MaxSpeakers),
		},
		"profanityFilterMode": "None",
	}
	if lang := strings.TrimSpace(req.Language); lang != "" {
		definition["locales"] = []string{lang}
	}
	defJSON, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}

	audioResp, err := a.fetchAudio(ctx, req.AudioURL)
	if err != nil {
		return nil, err
	}
	defer audioResp.Body.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := writeAzureMultipart(mw, defJSON, audioResp.Body)
		if cerr := mw.Close(); err == nil {
			err = cerr
		}
		pw.CloseWithError(err)
	}()

	url := fmt.Sprintf("%s/speechtotext/transcriptions:transcribe?api-version=%s", a.endpoint, azureFastAPIVersion)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Ocp-Apim-Subscription-Key", a.key)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azure transcribe request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read azure response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("azure transcribe %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}
	return decodeAzureFastResponse(raw)
}

// fetchAudio opens the presigned audio object for streaming.
func (a *AzureFast) fetchAudio(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Use a transport without the request timeout wrapper: the body is
	// consumed while the transcription call is in flight.
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("fetch audio: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch audio: status %d", resp.StatusCode)
	}
	return resp, nil
}

// writeAzureMultipart emits the two form parts the endpoint expects.
func writeAzureMultipart(mw *multipart.Writer, definition []byte, audio io.Reader) error {
	if err := mw.WriteField("definition", string(definition)); err != nil {
		return err
	}
	part, err := mw.CreateFormFile("audio", "audio")
	if err != nil {
		return err
	}
	_, err = io.Copy(part, audio)
	return err
}

// decodeAzureFastResponse maps the Azure document onto the neutral Transcript.
func decodeAzureFastResponse(raw []byte) (*Transcript, error) {
	var doc azureFastResponse
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse azure response: %w", err)
	}
	t := &Transcript{DurationMS: doc.DurationMilliseconds}
	for _, p := range doc.Phrases {
		phrase := Phrase{
			Speaker:    p.Speaker,
			OffsetMS:   p.OffsetMilliseconds,
			DurationMS: p.DurationMilliseconds,
			Text:       p.Text,
			Locale:     p.Locale,
		}
		for _, w := range p.Words {
			phrase.Words = append(phrase.Words, Word{
				Text:       w.Text,
				OffsetMS:   w.OffsetMilliseconds,
				DurationMS: w.DurationMilliseconds,
			})
		}
		t.Phrases = append(t.Phrases, phrase)
	}
	return t, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
