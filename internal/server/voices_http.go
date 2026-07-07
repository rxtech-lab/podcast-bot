package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sirily11/debate-bot/internal/tts"
)

// voiceMeta is the client-facing description of one Azure TTS voice. It
// re-tags tts.Voice's Azure-cased JSON fields with conventional lowercase keys.
type voiceMeta struct {
	Name       string   `json:"name"`
	Locale     string   `json:"locale"`
	LocaleName string   `json:"locale_name,omitempty"`
	Gender     string   `json:"gender,omitempty"`
	VoiceType  string   `json:"voice_type,omitempty"`
	Styles     []string `json:"styles,omitempty"`
}

// voicesResponse is the body of GET /api/voices.
type voicesResponse struct {
	Voices []voiceMeta `json:"voices"`
}

// handleVoices enumerates the Azure neural voices the engine can synthesize
// with, so the app can populate its per-speaker voice pickers. The list is
// fetched live from Azure's voices/list endpoint and cached in Redis for 24h.
// Returns 503 when Azure speech credentials are not configured.
func (s *Server) handleVoices(w http.ResponseWriter, r *http.Request) {
	if s.d.Env != nil && s.d.Env.E2EMode {
		writeJSON(w, voicesResponse{Voices: e2eFakeVoices()})
		return
	}
	if cached, ok := s.d.VoiceCatalog.Get(r.Context()); ok {
		writeJSON(w, voicesResponse{Voices: voiceMetas(cached)})
		return
	}
	voices, err := s.fetchAzureVoices(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.d.VoiceCatalog.Set(r.Context(), voices)
	writeJSON(w, voicesResponse{Voices: voiceMetas(voices)})
}

// e2eFakeVoices is the static roster served in hermetic E2E mode, where Azure
// speech is never configured but the UI tests still exercise the voice picker.
func e2eFakeVoices() []voiceMeta {
	return []voiceMeta{
		{Name: "en-US-E2EAvaNeural", Locale: "en-US", LocaleName: "English (United States)", Gender: "Female", VoiceType: "Neural"},
		{Name: "en-US-E2ENovaNeural", Locale: "en-US", LocaleName: "English (United States)", Gender: "Male", VoiceType: "Neural"},
	}
}

func voiceMetas(voices []tts.Voice) []voiceMeta {
	out := make([]voiceMeta, 0, len(voices))
	for _, v := range voices {
		out = append(out, voiceMeta{
			Name:       v.ShortName,
			Locale:     v.Locale,
			LocaleName: v.LocaleName,
			Gender:     v.Gender,
			VoiceType:  v.VoiceType,
			Styles:     v.StyleList,
		})
	}
	return out
}

// catalogVoices returns the Azure voice roster for admin dropdowns and other
// non-request contexts, preferring the Redis cache and falling back to a live
// fetch. Returns nil (not an error) when Azure is unconfigured — callers treat
// an empty roster as "no voices to choose from".
func (s *Server) catalogVoices(ctx context.Context) []tts.Voice {
	if s.d.Env != nil && s.d.Env.E2EMode {
		return e2eFakeVoicesTTS()
	}
	if cached, ok := s.d.VoiceCatalog.Get(ctx); ok {
		return cached
	}
	client, err := s.azureTTS()
	if err != nil {
		return nil
	}
	voices, err := client.FetchVoices(ctx, "")
	if err != nil {
		return nil
	}
	s.d.VoiceCatalog.Set(ctx, voices)
	return voices
}

// e2eFakeVoicesTTS mirrors e2eFakeVoices as tts.Voice values for catalog use.
func e2eFakeVoicesTTS() []tts.Voice {
	return []tts.Voice{
		{ShortName: "en-US-E2EAvaNeural", Locale: "en-US", LocaleName: "English (United States)", Gender: "Female", VoiceType: "Neural"},
		{ShortName: "en-US-E2ENovaNeural", Locale: "en-US", LocaleName: "English (United States)", Gender: "Male", VoiceType: "Neural"},
	}
}

func (s *Server) azureTTS() (*tts.AzureClient, error) {
	if s.d.Env == nil || strings.TrimSpace(s.d.Env.AzureSpeechRegion) == "" || strings.TrimSpace(s.d.Env.AzureSpeechKey) == "" {
		return nil, errAzureUnconfigured
	}
	return tts.NewAzure(s.d.Env.AzureSpeechRegion, s.d.Env.AzureSpeechKey), nil
}

var errAzureUnconfigured = errors.New("azure speech is not configured")

func (s *Server) fetchAzureVoices(r *http.Request) ([]tts.Voice, error) {
	client, err := s.azureTTS()
	if err != nil {
		return nil, err
	}
	return client.FetchVoices(r.Context(), "")
}

// voicePreviewRequest is the body of POST /api/voices/preview. Voice is the
// Azure ShortName; language is the BCP-47 plan language used for SSML, backend
// sample text selection, and the cache key.
type voicePreviewRequest struct {
	Voice    string `json:"voice"`
	Language string `json:"language"`
}

type voicePreviewResponse struct {
	URL string `json:"url"`
}

// voicePreviewKeyPart accepts Azure voice ShortNames ("zh-CN-XiaochenNeural",
// "en-US-Ava:DragonHDLatestNeural") and BCP-47 tags, and rejects anything that
// could escape the S3 key prefix.
var voicePreviewKeyPart = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

const voicePreviewMaxTextChars = 300

// handleVoicePreview returns a playable URL for a short sample of one Azure
// voice. The sample is synthesized at most once per (voice, language, sample
// text): the rendered MP3 lives in S3 with a text hash in the key, and later
// requests just re-sign the stored object. 503 when Azure speech or S3 storage
// is not configured.
func (s *Server) handleVoicePreview(w http.ResponseWriter, r *http.Request) {
	var req voicePreviewRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	voice := strings.TrimSpace(req.Voice)
	language := strings.TrimSpace(req.Language)
	if voice == "" || language == "" {
		http.Error(w, "voice and language are required", http.StatusBadRequest)
		return
	}
	if !voicePreviewKeyPart.MatchString(voice) || !voicePreviewKeyPart.MatchString(language) {
		http.Error(w, "invalid voice or language", http.StatusBadRequest)
		return
	}
	text := voicePreviewSampleText(language)
	if utf8.RuneCountInString(text) > voicePreviewMaxTextChars {
		http.Error(w, "text is too long", http.StatusBadRequest)
		return
	}
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() || s.d.Discussions == nil {
		http.Error(w, "preview storage is not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	if key, err := s.d.Discussions.GetVoicePreview(ctx, voice, language); err == nil && voicePreviewKeyMatchesText(key, text) {
		if url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour); err == nil {
			writeJSON(w, voicePreviewResponse{URL: url})
			return
		}
	}

	client, err := s.azureTTS()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// Reject voices Azure doesn't know rather than paying for a synthesis
	// attempt; skip the check when the catalog itself is unavailable.
	if catalog := s.voiceCatalog(r); len(catalog) > 0 && !voiceInCatalog(catalog, voice) {
		http.Error(w, "unknown voice", http.StatusBadRequest)
		return
	}

	body, err := client.SynthesizeStream(ctx, voice, text, language)
	if err != nil {
		http.Error(w, "synthesize preview: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "read preview audio: "+err.Error(), http.StatusBadGateway)
		return
	}

	key := s.d.Uploader.Key(voicePreviewObjectName(voice, language, text))
	if err := s.d.Uploader.UploadBytes(ctx, key, "audio/mpeg", data); err != nil {
		http.Error(w, "store preview: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := s.d.Discussions.PutVoicePreview(ctx, voice, language, key); err != nil && s.d.Log != nil {
		s.d.Log.Warn("record voice preview", "voice", voice, "language", language, "err", err)
	}
	url, err := s.d.Uploader.DownloadURL(ctx, key, time.Hour)
	if err != nil {
		http.Error(w, "sign preview url: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, voicePreviewResponse{URL: url})
}

func voicePreviewSampleText(language string) string {
	switch normalizedVoicePreviewLanguage(language) {
	case "zh-Hant":
		return "你好！這是這段聲音的快速試聽。"
	case "zh-Hans":
		return "你好！这是这段声音的快速试听。"
	case "ja":
		return "こんにちは。この音声がどのように聞こえるかの簡単なプレビューです。"
	case "ko":
		return "안녕하세요! 이 목소리가 어떻게 들리는지 짧게 미리 들어보세요."
	case "es":
		return "¡Hola! Esta es una breve muestra de cómo suena esta voz."
	case "fr":
		return "Bonjour ! Voici un bref aperçu de cette voix."
	case "de":
		return "Hallo! Hier ist eine kurze Vorschau darauf, wie diese Stimme klingt."
	default:
		return "Hello! Here's a quick preview of how this voice sounds."
	}
}

func normalizedVoicePreviewLanguage(language string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	if normalized == "zh" || strings.HasPrefix(normalized, "zh-hans") ||
		strings.HasPrefix(normalized, "zh-cn") || strings.HasPrefix(normalized, "zh-sg") {
		return "zh-Hans"
	}
	if strings.HasPrefix(normalized, "zh-hant") || strings.HasPrefix(normalized, "zh-tw") ||
		strings.HasPrefix(normalized, "zh-hk") || strings.HasPrefix(normalized, "zh-mo") {
		return "zh-Hant"
	}
	switch {
	case strings.HasPrefix(normalized, "ja"):
		return "ja"
	case strings.HasPrefix(normalized, "ko"):
		return "ko"
	case strings.HasPrefix(normalized, "es"):
		return "es"
	case strings.HasPrefix(normalized, "fr"):
		return "fr"
	case strings.HasPrefix(normalized, "de"):
		return "de"
	default:
		return "en"
	}
}

func voicePreviewObjectName(voice, language, text string) string {
	return "voice-previews/" + voice + "-" + language + "-" + voicePreviewTextHash(text) + ".mp3"
}

func voicePreviewKeyMatchesText(key, text string) bool {
	return strings.HasSuffix(strings.TrimSpace(key), "-"+voicePreviewTextHash(text)+".mp3")
}

func voicePreviewTextHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])[:12]
}

// voiceCatalog returns the Azure voice roster, preferring the Redis cache and
// falling back to a live fetch (which also refills the cache). Empty when
// Azure is unreachable.
func (s *Server) voiceCatalog(r *http.Request) []tts.Voice {
	if cached, ok := s.d.VoiceCatalog.Get(r.Context()); ok {
		return cached
	}
	voices, err := s.fetchAzureVoices(r)
	if err != nil {
		return nil
	}
	s.d.VoiceCatalog.Set(r.Context(), voices)
	return voices
}

func voiceInCatalog(catalog []tts.Voice, shortName string) bool {
	for _, v := range catalog {
		if strings.EqualFold(v.ShortName, shortName) {
			return true
		}
	}
	return false
}
