package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// defaultTranscribeModel is the Gemini model used to transcribe a voice message
// when the sender's device can't do it on-device. Override with GEMINI_TRANSCRIBE_MODEL.
//
// Gemini (not the Vercel AI Gateway) does the work: the gateway lists whisper /
// gpt-4o-transcribe in its catalog but does NOT proxy the OpenAI-compatible
// /audio/transcriptions endpoint (it 404s — see vercel/ai#13504), so we
// transcribe through Google's multimodal generateContent instead, reusing the
// GEMINI_API_KEY already required at startup.
const defaultTranscribeModel = "gemini-2.5-flash"

// geminiModelsBase is the Generative Language REST base for generateContent.
const geminiModelsBase = "https://generativelanguage.googleapis.com/v1beta/models"

// transcribePrompt instructs Gemini to return only the spoken words.
const transcribePrompt = "Transcribe this audio verbatim. Output ONLY the transcript text — no commentary, labels, quotation marks, or notes. If there is no intelligible speech, output nothing."

// transcribeFetchTTL bounds the presigned GET used to pull the audio object the
// model transcribes — long enough for a slow upstream, short-lived otherwise.
const transcribeFetchTTL = 5 * time.Minute

// transcribeRequest is the body of POST /api/transcribe: the durable storage key
// of an already-uploaded voice message owned by the caller.
type transcribeRequest struct {
	AudioKey string `json:"audio_key"`
}

type transcribeResponse struct {
	Text string `json:"text"`
}

// handleTranscribeAudio transcribes an already-uploaded voice message to text via
// Gemini. iOS calls this only as a fallback when the device can't transcribe
// on-device, so a voice note still reaches the agent as text; the audio itself is
// uploaded and sent independently (replayable either way).
func (s *Server) handleTranscribeAudio(w http.ResponseWriter, r *http.Request) {
	var req transcribeRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	user := s.requestUser(r)
	key := s.validatedAudioKey(user.ID, req.AudioKey)
	if key == "" {
		http.Error(w, "invalid audio key", http.StatusBadRequest)
		return
	}
	if s.d.Env == nil || strings.TrimSpace(s.d.Env.GeminiAPIKey) == "" {
		http.Error(w, "transcription not configured", http.StatusServiceUnavailable)
		return
	}
	text, err := s.cloudTranscribe(r.Context(), key)
	if err != nil {
		http.Error(w, "transcribe: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, transcribeResponse{Text: text})
}

// cloudTranscribe pulls the audio object behind key from storage and transcribes
// it with Gemini. Returns "" (no error) when transcription is not configured, so
// callers can treat it as best-effort.
func (s *Server) cloudTranscribe(ctx context.Context, key string) (string, error) {
	if s.d.Env == nil || strings.TrimSpace(s.d.Env.GeminiAPIKey) == "" {
		return "", nil
	}
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return "", nil
	}
	fetchURL, err := s.d.Uploader.PresignGet(ctx, key, transcribeFetchTTL)
	if err != nil || fetchURL == "" {
		return "", fmt.Errorf("presign audio: %w", err)
	}
	data, err := fetchTranscribeAudio(ctx, fetchURL)
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(s.d.Env.TranscribeModel)
	if model == "" {
		model = defaultTranscribeModel
	}
	return geminiTranscribe(ctx, s.d.Env.GeminiAPIKey, model, data, geminiAudioMIME(key))
}

// geminiTranscribe sends the audio inline to Gemini's generateContent and returns
// the concatenated text of the first candidate.
func geminiTranscribe(ctx context.Context, apiKey, model string, audio []byte, mime string) (string, error) {
	body := map[string]any{
		"contents": []any{
			map[string]any{
				"parts": []any{
					map[string]any{"text": transcribePrompt},
					map[string]any{"inline_data": map[string]any{
						"mime_type": mime,
						"data":      base64.StdEncoding.EncodeToString(audio),
					}},
				},
			},
		},
		"generationConfig": map[string]any{"temperature": 0},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/%s:generateContent", geminiModelsBase, model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-goog-api-key", apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read gemini body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gemini %d: %s", resp.StatusCode, truncateText(string(raw), 300))
	}
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
		return "", fmt.Errorf("parse gemini response: %w", err)
	}
	var sb strings.Builder
	for _, cand := range parsed.Candidates {
		for _, part := range cand.Content.Parts {
			sb.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// geminiAudioMIME maps a stored object's extension to a MIME type Gemini accepts.
// iOS records .m4a (AAC in an MP4 container); Gemini wants "audio/mp4" for those.
func geminiAudioMIME(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".m4a", ".mp4", ".aac":
		return "audio/mp4"
	case ".mp3":
		return "audio/mp3"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".aiff", ".aif":
		return "audio/aiff"
	default:
		return "audio/mp4"
	}
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// fetchTranscribeAudio downloads the audio bytes from a presigned URL, capped at
// the same ceiling as uploads so a forged key can't exhaust memory.
func fetchTranscribeAudio(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("audio request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch audio: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch audio: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("audio is empty")
	}
	if int64(len(data)) > maxUploadBytes {
		return nil, fmt.Errorf("audio too large")
	}
	return data, nil
}
