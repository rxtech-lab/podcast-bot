package videojob

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/subtitleutil"
	"github.com/sirily11/debate-bot/internal/video"
)

type subtitleLanguage struct {
	Code  string
	Name  string
	Alias []string
}

var subtitleLanguages = []subtitleLanguage{
	{Code: "zh-Hans", Name: "Simplified Chinese", Alias: []string{"zh-cn", "zh-sg", "zh-hans"}},
	{Code: "zh-Hant", Name: "Traditional Chinese", Alias: []string{"zh-tw", "zh-hk", "zh-mo", "zh-hant"}},
	{Code: "en", Name: "English", Alias: []string{"eng"}},
	{Code: "ja", Name: "Japanese", Alias: []string{"jpn"}},
	{Code: "ko", Name: "Korean", Alias: []string{"kor"}},
	{Code: "es", Name: "Spanish", Alias: []string{"spa"}},
	{Code: "fr", Name: "French", Alias: []string{"fra", "fre"}},
	{Code: "de", Name: "German", Alias: []string{"deu", "ger"}},
}

type subtitleJSONClient interface {
	JSON(ctx context.Context, system, user string) ([]byte, error)
}

func subtitleTracksForJob(ctx context.Context, client subtitleJSONClient,
	outDir, sourceLanguage string, source []contentcreator.SubtitleCue, targets []string,
) ([]video.SubtitleTrack, error) {
	if len(source) == 0 {
		return nil, nil
	}
	langs, err := normalizeRequestedSubtitleLanguages(sourceLanguage, targets)
	if err != nil {
		return nil, err
	}
	tracks := make([]video.SubtitleTrack, 0, len(langs))
	for _, lang := range langs {
		translated, err := translateSubtitleCues(ctx, client, source, lang)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(outDir, "subtitles."+subtitleFileCode(lang.Code)+".vtt")
		if err := contentcreator.WriteSubtitleCues(path, translated); err != nil {
			return nil, fmt.Errorf("write translated subtitles %s: %w", lang.Code, err)
		}
		tracks = append(tracks, video.SubtitleTrack{
			Path:     path,
			Language: lang.Code,
		})
	}
	return tracks, nil
}

func newSubtitleTranslator(envBaseURL, envKey, envModel string) subtitleJSONClient {
	return llm.New(envBaseURL, envKey, envModel)
}

func normalizeRequestedSubtitleLanguages(sourceLanguage string, raw []string) ([]subtitleLanguage, error) {
	sourceKey := languageKey(sourceLanguage)
	seen := map[string]bool{}
	out := make([]subtitleLanguage, 0, len(raw))
	for _, v := range raw {
		lang, ok := lookupSubtitleLanguage(v)
		if !ok {
			return nil, fmt.Errorf("unsupported subtitle language %q", v)
		}
		key := languageKey(lang.Code)
		if key == "" || key == sourceKey || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, lang)
	}
	return out, nil
}

func lookupSubtitleLanguage(raw string) (subtitleLanguage, bool) {
	key := languageKey(raw)
	for _, lang := range subtitleLanguages {
		if languageKey(lang.Code) == key {
			return lang, true
		}
		for _, alias := range lang.Alias {
			if languageKey(alias) == key {
				return lang, true
			}
		}
	}
	return subtitleLanguage{}, false
}

func languageKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "zh-hans", "zh-cn", "zh-sg":
		return "zh-hans"
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return "zh-hant"
	}
	if i := strings.Index(key, "-"); i >= 0 {
		key = key[:i]
	}
	switch key {
	case "zho", "chi", "cmn", "yue":
		return "zh"
	case "eng":
		return "en"
	case "jpn":
		return "ja"
	case "kor":
		return "ko"
	case "spa":
		return "es"
	case "fra", "fre":
		return "fr"
	case "deu", "ger":
		return "de"
	default:
		return key
	}
}

func subtitleFileCode(code string) string {
	return strings.NewReplacer("-", "_", "/", "_", "\\", "_").Replace(strings.ToLower(code))
}

type subtitleTranslationResponse struct {
	Translations []string `json:"translations"`
}

func translateSubtitleCues(ctx context.Context, client subtitleJSONClient,
	source []contentcreator.SubtitleCue, target subtitleLanguage,
) ([]contentcreator.SubtitleCue, error) {
	if client == nil {
		return nil, fmt.Errorf("subtitle translation client is not configured")
	}
	texts := make([]string, len(source))
	for i, cue := range source {
		texts[i] = cue.Text
	}
	payload, err := json.Marshal(struct {
		TargetLanguage string   `json:"target_language"`
		Cues           []string `json:"cues"`
	}{
		TargetLanguage: target.Name,
		Cues:           texts,
	})
	if err != nil {
		return nil, err
	}

	system := "You translate subtitle cue text. Return only valid JSON with a translations array. Preserve cue count and order exactly. Do not include timestamps, numbering, markdown, commentary, or extra fields."
	user := "Translate every cue into " + target.Name + ". Keep each entry concise enough for subtitles. Input JSON:\n" + string(payload)
	raw, err := client.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("translate subtitles to %s: %w", target.Name, err)
	}

	var resp subtitleTranslationResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("translate subtitles to %s: invalid JSON: %w", target.Name, err)
	}
	if len(resp.Translations) != len(source) {
		return nil, fmt.Errorf("translate subtitles to %s: got %d cues, want %d",
			target.Name, len(resp.Translations), len(source))
	}

	out := make([]contentcreator.SubtitleCue, len(source))
	for i, cue := range source {
		text := strings.TrimSpace(resp.Translations[i])
		if text == "" {
			return nil, fmt.Errorf("translate subtitles to %s: cue %d is empty", target.Name, i+1)
		}
		text = subtitleutil.StripPunct(text)
		if text == "" {
			return nil, fmt.Errorf("translate subtitles to %s: cue %d is empty after punctuation stripping", target.Name, i+1)
		}
		out[i] = contentcreator.SubtitleCue{
			Start: cue.Start,
			End:   cue.End,
			Text:  text,
		}
	}
	return out, nil
}
