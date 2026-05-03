package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

const (
	elevenLabsBaseURL = "https://api.elevenlabs.io"
	// elevenLabsModel is the multilingual model used for every synthesis.
	// `eleven_multilingual_v2` covers all locales the agents speak, so we
	// don't need per-language model selection.
	elevenLabsModel = "eleven_multilingual_v2"
)

// ElevenLabsClient is an ElevenLabs TTS REST client.
//
// Output is transcoded on the fly to Azure's audio-24khz-48kbitrate-mono-mp3
// format so the rest of the pipeline (LiveStream pacing, ConcatToMP3 with
// `-c copy`, AudioBytesPerSec subtitle alignment) keeps working without a
// branch per provider.
type ElevenLabsClient struct {
	apiKey string
	http   *http.Client
}

// NewElevenLabs constructs an ElevenLabsClient.
func NewElevenLabs(apiKey string) *ElevenLabsClient {
	return &ElevenLabsClient{apiKey: apiKey, http: &http.Client{}}
}

type elVoiceLabels struct {
	Gender string `json:"gender,omitempty"`
}

type elVoice struct {
	VoiceID  string        `json:"voice_id"`
	Name     string        `json:"name"`
	Category string        `json:"category"`
	Labels   elVoiceLabels `json:"labels"`
}

type elVoicesResp struct {
	Voices []elVoice `json:"voices"`
}

// FetchVoices lists ElevenLabs voices. Returned voices have `Locale` set to
// the topic `language` because eleven_multilingual_v2 voices are not
// locale-bound; tagging them this way lets the existing voice picker treat
// them as eligible without provider-specific code.
func (c *ElevenLabsClient) FetchVoices(ctx context.Context, language string) ([]Voice, error) {
	url := elevenLabsBaseURL + "/v2/voices?page_size=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch elevenlabs voices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("elevenlabs voices status %d: %s", resp.StatusCode, string(body))
	}
	var data elVoicesResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode elevenlabs voices: %w", err)
	}
	if language == "" {
		language = "en-US"
	}
	out := make([]Voice, 0, len(data.Voices))
	for _, v := range data.Voices {
		out = append(out, Voice{
			ShortName:  v.VoiceID,
			Locale:     language,
			Gender:     normalizeGender(v.Labels.Gender),
			VoiceType:  "Neural",
			LocaleName: v.Name,
		})
	}
	return out, nil
}

// SynthesizeStream POSTs `text` to the ElevenLabs streaming endpoint and
// returns a reader yielding MP3 bytes in Azure's 24kHz/48kbps/mono format.
//
// Internally:
//  1. Request `output_format=pcm_24000` so we get raw 16-bit signed LE PCM
//     at 24 kHz mono — matching Azure's sample rate exactly.
//  2. Pipe that PCM through ffmpeg to MP3 at 48 kbps mono. Single lossy
//     encode (vs. mp3->mp3 transcode) and the final byte stream is
//     bit-rate compatible with the rest of the pipeline.
func (c *ElevenLabsClient) SynthesizeStream(ctx context.Context, voiceID, text, lang string) (io.ReadCloser, error) {
	if voiceID == "" {
		return nil, fmt.Errorf("elevenlabs: voice_id is required")
	}
	body := map[string]any{
		"text":     text,
		"model_id": elevenLabsModel,
	}
	if lang != "" {
		// language_code is honored by eleven_multilingual_v2 only as a hint;
		// passing the topic language helps stabilize pronunciation.
		body["language_code"] = strings.SplitN(lang, "-", 2)[0]
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1/text-to-speech/%s/stream?output_format=pcm_24000",
		elevenLabsBaseURL, voiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/pcm")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs tts post: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("elevenlabs tts status %d: %s", resp.StatusCode, string(errBody))
	}
	return encodePCMToAzureMP3(ctx, resp.Body)
}

// encodePCMToAzureMP3 wraps a PCM reader with an ffmpeg subprocess that
// re-encodes to audio-24khz-48kbitrate-mono-mp3. The returned ReadCloser
// closes both ffmpeg's pipes and waits for the subprocess.
func encodePCMToAzureMP3(ctx context.Context, src io.ReadCloser) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "quiet",
		"-f", "s16le",
		"-ar", "24000",
		"-ac", "1",
		"-i", "pipe:0",
		"-c:a", "libmp3lame",
		"-b:a", "48k",
		"-ar", "24000",
		"-ac", "1",
		"-f", "mp3",
		"pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("elevenlabs ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = src.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("elevenlabs ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = src.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("elevenlabs ffmpeg start: %w", err)
	}
	// Pump HTTP body → ffmpeg stdin in the background so the caller can
	// stream bytes off stdout as they're produced.
	go func() {
		defer src.Close()
		defer stdin.Close()
		_, _ = io.Copy(stdin, src)
	}()
	return &transcodeReader{stdout: stdout, cmd: cmd}, nil
}

type transcodeReader struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (r *transcodeReader) Read(p []byte) (int, error) { return r.stdout.Read(p) }

func (r *transcodeReader) Close() error {
	err := r.stdout.Close()
	_ = r.cmd.Wait()
	return err
}

func normalizeGender(g string) string {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case "male":
		return "Male"
	case "female":
		return "Female"
	}
	return ""
}
