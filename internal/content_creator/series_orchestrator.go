package contentcreator

import (
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/tts"
)

// buildSeriesAgents constructs the single-host roster for a TV-series
// episode. Series content has no players, no judge, no debate sides — the
// host speaks alone for the entire episode. Viewers are populated by the
// base orchestrator's buildAgents, but the series planner ignores them
// (no audience interjection during a non-interactive narration).
func (o *Orchestrator) buildSeriesAgents() error {
	hostName := o.Topic.SeriesHost.Name
	if hostName == "" {
		hostName = "Narrator"
	}
	o.Registry.SeriesHost = o.makeAgent(
		config.AgentSpec{
			Name:    hostName,
			Model:   o.Topic.SeriesHost.Model,
			BaseURL: o.Topic.SeriesHost.BaseURL,
			APIKey:  o.Topic.SeriesHost.APIKey,
		},
		agent.RoleSeriesHost, o.Env.HostModel)
	return nil
}

// newSeriesPlanner constructs the series-format planner used by the base
// orchestrator's newPlanner dispatcher.
func (o *Orchestrator) newSeriesPlanner() Planner {
	return NewSeriesPlanner(o.Topic, o.Tracker, o.Registry, o.seriesPreviouslyOn != "")
}

// SetSeriesPreviouslyOn installs the compression-LLM-generated recap text
// for this episode. Empty string disables the recap turn entirely (the
// planner won't emit one and the host's prompt block stays empty so the
// LLM never invents one). Must be called before Run, since the host
// agent captures its prompt at construction time inside makeAgent.
func (o *Orchestrator) SetSeriesPreviouslyOn(recap string) {
	o.seriesPreviouslyOn = recap
}

// SetSeriesPlan records the visual director's per-frame direction list +
// anchors + animations for the series narration. Mirrors SetSurfacePlan /
// SetSurfaceAnchors on the puzzle side. nil / empty inputs are no-ops.
func (o *Orchestrator) SetSeriesPlan(plan, anchors, animations []string) {
	if len(plan) > 0 {
		o.seriesNarrationPlan = append([]string(nil), plan...)
	}
	if len(anchors) > 0 {
		o.seriesNarrationAnchors = append([]string(nil), anchors...)
	}
	if len(animations) > 0 {
		o.seriesNarrationAnims = append([]string(nil), animations...)
	}
}

// SeriesImageRefCatalogEntry is the contentcreator-facing mirror of
// agent.ImageRefCatalogEntry. Lets cmd/ wire the catalog without
// importing the agent package directly. The orchestrator's
// SetSeriesImageRefs translates these into the agent struct on the way
// to the host.
type SeriesImageRefCatalogEntry struct {
	Season      int
	Episode     int
	Beat        int
	Description string
}

// SetSeriesImageRefs installs the cross-episode reuse catalog (visible to
// the host's prompt) AND the resolver map (canonical key → on-disk PNG
// path) consumed by the stage. catalog and paths are independent inputs:
// the catalog drives what the LLM may emit, the paths determine what the
// renderer can actually paint. Empty catalog → host omits the image-reuse
// section from its prompt; empty paths → ImageRefMsg events become no-ops
// at the stage.
func (o *Orchestrator) SetSeriesImageRefs(catalog []SeriesImageRefCatalogEntry, paths map[string]string) {
	if len(catalog) > 0 {
		o.seriesImageRefCatalog = make([]agent.ImageRefCatalogEntry, len(catalog))
		for i, e := range catalog {
			o.seriesImageRefCatalog[i] = agent.ImageRefCatalogEntry{
				Season:      e.Season,
				Episode:     e.Episode,
				Beat:        e.Beat,
				Description: e.Description,
			}
		}
	}
	if len(paths) > 0 {
		o.seriesImageRefPaths = make(map[string]string, len(paths))
		for k, v := range paths {
			o.seriesImageRefPaths[k] = v
		}
	}
}

// SetSeriesMusic installs the optional looping music bed path for the
// upcoming episode run. Caller (cmd/debate-bot/series.go) populates this
// after musicgen finishes. Empty path is a no-op.
func (o *Orchestrator) SetSeriesMusic(path string) {
	if path != "" {
		o.seriesMusicPath = path
	}
}

// SetSeriesSoundPlan mirrors SetSoundPlan but applies to series episodes.
// Same trim-to-shorter-length semantics as the puzzle setter.
func (o *Orchestrator) SetSeriesSoundPlan(plan []SoundCueDirection, paths []string) {
	if len(plan) == 0 || len(paths) == 0 {
		return
	}
	n := len(plan)
	if len(paths) < n {
		n = len(paths)
	}
	o.seriesSoundPlan = append([]SoundCueDirection(nil), plan[:n]...)
	o.seriesSoundPaths = append([]string(nil), paths[:n]...)
}

// SeriesNarrationFrames reports the planner's narration-frame count for
// this episode. Used by the pipeline to size the marker-clamp budget so a
// stray `<scene 99/>` against a 14-frame plan doesn't pin the rotation.
func (o *Orchestrator) SeriesNarrationFrames() int {
	return len(o.seriesNarrationPlan)
}

// SeriesCharacter is the contentcreator-facing mirror of
// agent.SeriesCharacter. Lets the prepare layer (internal/series) wire
// the cast list without importing the agent package directly. AzureVoice
// is left empty by the caller — the orchestrator fills it in after
// FetchVoices runs in Setup, picking from the locale's voice pool.
type SeriesCharacter struct {
	Name        string
	Gender      string
	VoiceHint   string
	Description string
}

// SetSeriesCharacters installs the planner's per-episode cast roster. The
// orchestrator stores these as-is and assigns Azure neural voices to each
// during Setup (after FetchVoices succeeds) so the host's prompt and the
// pipeline's multi-voice SSML synth path see fully-populated voice IDs.
// Empty / nil disables the feature for this episode (the host's prompt
// omits the character section entirely).
func (o *Orchestrator) SetSeriesCharacters(cast []SeriesCharacter) {
	if len(cast) == 0 {
		o.seriesCharacters = nil
		return
	}
	o.seriesCharacters = make([]SeriesCharacter, len(cast))
	copy(o.seriesCharacters, cast)
}

// SeriesCharacters returns the cast roster with Azure voice IDs already
// assigned by the orchestrator. The pipeline reads this in synthSentence
// to map `<char-N>` markers to voice ShortNames at synth time.
func (o *Orchestrator) SeriesCharacters() []agent.SeriesCharacter {
	return o.seriesCharactersForHost()
}

// assignSeriesCharacterVoices picks one Azure neural voice per cast
// member out of the locale-filtered pool, excluding voices already
// claimed by agents (so the narrator doesn't share a voice with a
// character). The result is stored on the orchestrator and pushed onto
// the host agent's roster via SetCharacterVoices so the SSML envelope
// emitted at synth time uses the assigned voice. No-op when there are
// no characters to assign.
func (o *Orchestrator) assignSeriesCharacterVoices(voices []tts.Voice) {
	if len(o.seriesCharacters) == 0 || o.Registry == nil || o.Registry.SeriesHost == nil {
		return
	}
	excluded := map[string]bool{}
	for _, a := range o.Registry.All() {
		if v := a.Voice().ShortName; v != "" {
			excluded[v] = true
		}
	}
	names := make([]string, 0, len(o.seriesCharacters))
	genders := map[string]string{}
	for _, c := range o.seriesCharacters {
		names = append(names, c.Name)
		genders[c.Name] = c.Gender
	}
	o.seriesCharacterVoices = agent.AssignCharacterVoices(
		voices, names, genders, o.Topic.Language,
		time.Now().UnixNano(), excluded, o.Log)
	if h, ok := o.Registry.SeriesHost.(*agent.SeriesHost); ok {
		h.SetCharacterVoices(o.seriesCharacterVoices)
	}
	for _, c := range o.seriesCharacters {
		o.Log.Info("series character voice",
			"name", c.Name,
			"gender", c.Gender,
			"voice", o.seriesCharacterVoices[c.Name])
	}
}

// seriesCharactersForHost translates the orchestrator-side cast slice
// into the agent-package struct so makeAgent can hand it to NewSeriesHost
// without contentcreator → agent leakage in either direction.
func (o *Orchestrator) seriesCharactersForHost() []agent.SeriesCharacter {
	if len(o.seriesCharacters) == 0 {
		return nil
	}
	out := make([]agent.SeriesCharacter, len(o.seriesCharacters))
	for i, c := range o.seriesCharacters {
		out[i] = agent.SeriesCharacter{
			Name:        c.Name,
			Gender:      c.Gender,
			Description: c.Description,
			AzureVoice:  o.seriesCharacterVoices[c.Name],
		}
	}
	return out
}
