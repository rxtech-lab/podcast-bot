package contentcreator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SeriesArchiveSubdir is the directory underneath the persistent root that
// holds every series episode's archive. Kept as a constant so callers and
// tests don't drift on the spelling.
const SeriesArchiveSubdir = "tv-series"

// ShowDir returns `<persistentRoot>/tv-series/<slug(show)>` — the per-show
// folder that holds every season/episode subdirectory for one series.
// persistentRoot is the non-session OUT_DIR (i.e. the user-supplied value
// before bootstrap appends `session-<stamp>`); see Env.PersistentRoot.
func ShowDir(persistentRoot, show string) string {
	return filepath.Join(persistentRoot, SeriesArchiveSubdir, SlugifyShow(show))
}

// EpisodeDir returns the canonical archive directory for one episode.
// The directory layout is:
//
//	<persistentRoot>/tv-series/<show>/s<NN>/e<NN>/
//	  ├── scenes/             generated narration PNGs (one per beat)
//	  ├── music/              looping music bed mp3
//	  ├── sounds/              per-cue sound clips
//	  ├── scene-plan.json     full ScenePlan / SeriesScenePlan
//	  ├── script.txt          verbatim host narration (markers + all)
//	  ├── subtitles.vtt       sidecar WebVTT for player CC
//	  └── episode.mp3         concatenated audio archive
//
// Season/episode are zero-padded to two digits so a directory listing sorts
// lexicographically through s10/e10 without surprise reordering.
func EpisodeDir(persistentRoot, show string, season, episode int) string {
	return filepath.Join(ShowDir(persistentRoot, show),
		fmt.Sprintf("s%02d", season),
		fmt.Sprintf("e%02d", episode))
}

// EnsureEpisodeDir creates EpisodeDir(...) and its standard subdirectories.
// Returns the episode directory path. Errors are wrapped with the path that
// failed so callers can surface a useful message.
func EnsureEpisodeDir(persistentRoot, show string, season, episode int) (string, error) {
	dir := EpisodeDir(persistentRoot, show, season, episode)
	for _, p := range []string{dir,
		filepath.Join(dir, "scenes"),
		filepath.Join(dir, "music"),
		filepath.Join(dir, "sounds"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return "", fmt.Errorf("ensure %s: %w", p, err)
		}
	}
	return dir, nil
}

// PriorEpisode is one entry from SiblingEpisodeDirs — the (season, episode)
// pair plus the absolute archive directory. Sorted in lexicographic order on
// (season, episode) so callers can read them as the show's chronology.
type PriorEpisode struct {
	Season  int
	Episode int
	Dir     string
}

var seasonDirRe = regexp.MustCompile(`^s(\d{1,3})$`)
var episodeDirRe = regexp.MustCompile(`^e(\d{1,3})$`)

// SiblingEpisodeDirs lists every prior episode of `show` whose (season,
// episode) is strictly before (curSeason, curEpisode) under lexicographic
// order. Used for both:
//
//   - "previously on …" recap input — read each prior `script.txt` /
//     `scene-plan.json` and feed them to the compression LLM.
//   - cross-episode image resolver — the host emits markers like
//     `<season-1-episode-3-image-7/>` and the renderer needs to know
//     which on-disk PNG to load.
//
// Missing or malformed directory names (anything that isn't `sNN` / `eNN`)
// are skipped silently rather than surfacing as an error: the show's archive
// is allowed to grow and shrink over time, and a half-deleted episode
// shouldn't block the next one from rendering.
func SiblingEpisodeDirs(persistentRoot, show string, curSeason, curEpisode int) ([]PriorEpisode, error) {
	root := ShowDir(persistentRoot, show)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("show stat: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("show path %s exists but is not a directory", root)
	}
	seasons, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("show readdir: %w", err)
	}
	var out []PriorEpisode
	for _, s := range seasons {
		if !s.IsDir() {
			continue
		}
		m := seasonDirRe.FindStringSubmatch(s.Name())
		if m == nil {
			continue
		}
		var season int
		if _, err := fmt.Sscanf(m[1], "%d", &season); err != nil || season <= 0 {
			continue
		}
		seasonPath := filepath.Join(root, s.Name())
		eps, err := os.ReadDir(seasonPath)
		if err != nil {
			continue
		}
		for _, e := range eps {
			if !e.IsDir() {
				continue
			}
			em := episodeDirRe.FindStringSubmatch(e.Name())
			if em == nil {
				continue
			}
			var episode int
			if _, err := fmt.Sscanf(em[1], "%d", &episode); err != nil || episode <= 0 {
				continue
			}
			if season > curSeason || (season == curSeason && episode >= curEpisode) {
				continue
			}
			out = append(out, PriorEpisode{
				Season:  season,
				Episode: episode,
				Dir:     filepath.Join(seasonPath, e.Name()),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out, nil
}

// ImageRefKey formats a stable cross-episode image-reference key. Used both
// as the `<season-S-episode-E-image-N/>` marker payload AND as the in-memory
// map key the resolver hands back to the renderer. Keeping this in one place
// avoids drift between the host's prompt format and the resolver's lookup.
func ImageRefKey(season, episode, beat int) string {
	return fmt.Sprintf("s%de%di%d", season, episode, beat)
}

// FormatImageRefMarker renders the human-readable marker form the host
// emits. Mirrors the marker regex used downstream.
func FormatImageRefMarker(season, episode, beat int) string {
	return fmt.Sprintf("<season-%d-episode-%d-image-%d/>", season, episode, beat)
}

var showSlugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// SlugifyShow normalises a show name to a filesystem-safe slug. Mirrors the
// debate slug logic in cmd/debate-bot/main.go but kept package-local so the
// content_creator code does not depend on cmd/.
func SlugifyShow(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = showSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "show"
	}
	return s
}
