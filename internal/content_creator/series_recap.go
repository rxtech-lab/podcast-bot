package contentcreator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/llm"
)

// PriorEpisodeContent is one prior episode's recap-relevant artefacts —
// the verbatim host narration and the parsed scene plan. Used internally
// by BuildRecap and exposed to callers (cmd/debate-bot/series.go) so they
// can build the cross-episode image-reuse catalog from the same parsed
// plan data.
type PriorEpisodeContent struct {
	Season  int
	Episode int
	Dir     string
	// Script is the concatenated script.txt content from the prior
	// episode's archive directory. Empty when the file wasn't written
	// (legacy archives, or a prior run that crashed before
	// finishCloseoutEpisode).
	Script string
	// Plan is the parsed scene-plan.json from the prior archive. nil
	// when the file is missing or malformed — caller should treat that
	// as "no reusable imagery from this episode".
	Plan *PriorScenePlan
}

// PriorScenePlan mirrors the shape of internal/video/scenes.ScenePlan but
// is duplicated here as a small struct so the content_creator package
// doesn't depend on internal/video/scenes (which would create an import
// cycle through anything that imports both). Only the fields BuildRecap
// and the cross-episode catalog need are decoded.
type PriorScenePlan struct {
	Surface    []string `json:"surface"`
	Conclusion []string `json:"conclusion"`
	Narration  []string `json:"narration"`
}

// LoadPriorEpisodes reads every entry returned by SiblingEpisodeDirs and
// fills in the script + scene-plan content. Errors per-episode are
// silently swallowed (the recap and catalog should both gracefully
// degrade when one prior archive is corrupt or partial); only a fatal
// SiblingEpisodeDirs error bubbles out.
func LoadPriorEpisodes(persistentRoot, show string, season, episode int) ([]PriorEpisodeContent, error) {
	siblings, err := SiblingEpisodeDirs(persistentRoot, show, season, episode)
	if err != nil {
		return nil, err
	}
	out := make([]PriorEpisodeContent, 0, len(siblings))
	for _, s := range siblings {
		entry := PriorEpisodeContent{Season: s.Season, Episode: s.Episode, Dir: s.Dir}
		if data, err := os.ReadFile(filepath.Join(s.Dir, "script.txt")); err == nil {
			entry.Script = string(data)
		}
		if data, err := os.ReadFile(filepath.Join(s.Dir, "scene-plan.json")); err == nil {
			var plan PriorScenePlan
			if err := json.Unmarshal(data, &plan); err == nil {
				entry.Plan = &plan
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// BuildImageRefCatalog walks priors and builds the cross-episode reuse
// catalog the series host receives in its prompt + the resolver map the
// stage uses. catalog entries describe what is in each archived frame;
// paths map canonical keys to absolute on-disk PNGs. A frame is only
// catalogued when both its description (from scene-plan.json) AND the
// PNG file (`<dir>/scenes/narration-vN-*.png`) exist.
//
// Today the caller hands a frameLookup callback that resolves a given
// narration-vN frame's on-disk path under <dir>/scenes/. We keep the
// lookup external to avoid hardcoding the cache-filename format here —
// scenes.go already mints content-addressed names with sha1(prompt) and
// only the scenes layer knows the prompt that produced each frame. The
// caller (cmd/debate-bot/series.go) probes the directory and matches
// on the "narration-vN" prefix.
func BuildImageRefCatalog(priors []PriorEpisodeContent,
	frameLookup func(dir string, beat int) string,
) (catalog []agent.ImageRefCatalogEntry, paths map[string]string) {
	paths = map[string]string{}
	for _, p := range priors {
		if p.Plan == nil {
			continue
		}
		// Series episodes use Plan.Narration; older puzzle-format archives
		// would expose Surface/Conclusion. Series catalog only includes
		// narration beats — cross-format reuse isn't expected here.
		for i, dir := range p.Plan.Narration {
			fpath := frameLookup(p.Dir, i)
			if fpath == "" {
				continue
			}
			key := ImageRefKey(p.Season, p.Episode, i)
			catalog = append(catalog, agent.ImageRefCatalogEntry{
				Season:      p.Season,
				Episode:     p.Episode,
				Beat:        i,
				Description: strings.TrimSpace(dir),
			})
			paths[key] = fpath
		}
	}
	return
}

// recapResult is the JSON shape we ask the compression LLM to produce.
type recapResult struct {
	Recap        string   `json:"recap"`
	HighlightIDs []string `json:"highlight_ids"`
}

// BuildRecap synthesises a "previously on …" preamble for episode > 1.
// Returns ("", nil) for episode 1 / when no prior content can be loaded.
// Errors are LLM/transport-level only — a thin recap or an empty
// highlight list is treated as success (the caller uses whatever came
// back).
//
// `comp` is the compression LLM client (Env.CompressionBaseURL/Key/Model).
// We pick this rather than the host LLM because it's already tuned for
// short-form summarisation and avoids burning the host model on
// non-creative text.
func BuildRecap(ctx context.Context, comp *llm.Client,
	priors []PriorEpisodeContent, showName string,
) (string, []string, error) {
	if comp == nil {
		return "", nil, nil
	}
	if len(priors) == 0 {
		return "", nil, nil
	}
	system := `You are writing a "previously on …" preamble for a serialized
narrated podcast. Produce strict JSON of the form:

  {
    "recap": "...",
    "highlight_ids": ["s1e2i7", "s1e3i2"]
  }

"recap" is a 3 to 6 sentence summary covering what the audience needs to
remember from prior episodes to make sense of the next one. Same narrator
voice as the show: calm, contemplative, plain prose, no markdown.
"highlight_ids" lists at most 6 archived-image keys the recap should
visually accompany — each id MUST be drawn from the highlight catalog
section in the user message. Skip ids that don't appear in the catalog.`

	var sb strings.Builder
	fmt.Fprintf(&sb, "Show: %s\n\n", showName)
	sb.WriteString("Highlight catalog (these are the only valid highlight_ids — do not invent):\n")
	for _, p := range priors {
		if p.Plan == nil {
			continue
		}
		for i, dir := range p.Plan.Narration {
			fmt.Fprintf(&sb, "  %s: %s\n",
				ImageRefKey(p.Season, p.Episode, i), strings.TrimSpace(dir))
		}
	}
	sb.WriteString("\nPrior-episode scripts (chronological):\n")
	for _, p := range priors {
		fmt.Fprintf(&sb, "\n=== Season %d, Episode %d ===\n", p.Season, p.Episode)
		if s := strings.TrimSpace(p.Script); s != "" {
			sb.WriteString(s)
			sb.WriteString("\n")
		} else {
			sb.WriteString("(no script archive available)\n")
		}
	}

	raw, err := comp.JSON(ctx, system, sb.String())
	if err != nil {
		return "", nil, fmt.Errorf("recap llm call: %w", err)
	}
	var parsed recapResult
	cleaned := unwrapFences(raw)
	if err := json.Unmarshal(cleaned, &parsed); err != nil {
		return "", nil, fmt.Errorf("recap unmarshal: %w (raw=%q)", err, truncatePreview(string(cleaned), 200))
	}
	// Filter highlight_ids against the catalog so a hallucinated key never
	// reaches the host's prompt or the renderer's resolver.
	valid := map[string]bool{}
	for _, p := range priors {
		if p.Plan == nil {
			continue
		}
		for i := range p.Plan.Narration {
			valid[ImageRefKey(p.Season, p.Episode, i)] = true
		}
	}
	highlights := make([]string, 0, len(parsed.HighlightIDs))
	for _, id := range parsed.HighlightIDs {
		id = strings.TrimSpace(id)
		if valid[id] {
			highlights = append(highlights, id)
			if len(highlights) >= 6 {
				break
			}
		}
	}
	return strings.TrimSpace(parsed.Recap), highlights, nil
}

// unwrapFences strips a leading "```json" / "```" code fence and a trailing
// "```" fence — same helper as scenes.unwrapJSONFences. Duplicated here so
// content_creator doesn't need to import scenes for one tiny utility.
func unwrapFences(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, "```") {
		return raw
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return []byte(strings.TrimSpace(s))
}
