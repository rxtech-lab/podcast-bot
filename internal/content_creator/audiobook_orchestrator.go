package contentcreator

import (
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// AudioBookImage is one generated illustration for an audiobook. Beat is the
// 0-based scene index it's anchored to (aligned with seriesNarrationPlan);
// Path is the on-disk PNG; URL is its stable/presigned object-storage URL;
// Caption is the planner's short description of the beat. Animation is the
// planner's camera-move token for the video post-pass (stall / pan* / zoom*;
// empty means stall).
type AudioBookImage struct {
	Beat      int
	Path      string
	URL       string
	Caption   string
	Animation string
}

// AudioBookAvatar is one generated speaker portrait for the conversational
// audiobook video layout. Path points at a local alpha PNG after chroma-key
// removal.
type AudioBookAvatar struct {
	Name string
	Path string
}

// SetAudioBookImages records the generated illustration set (ordered by Beat).
// Called by the audiobook prepare step before Run.
func (o *Orchestrator) SetAudioBookImages(imgs []AudioBookImage) {
	o.audioBookImages = append([]AudioBookImage(nil), imgs...)
}

// AudioBookImages returns the generated illustration set. The text-content and
// video stages read it after Run to embed/paint the images.
func (o *Orchestrator) AudioBookImages() []AudioBookImage {
	return append([]AudioBookImage(nil), o.audioBookImages...)
}

// SetAudioBookAvatars records generated transparent speaker portraits.
func (o *Orchestrator) SetAudioBookAvatars(avatars []AudioBookAvatar) {
	o.audioBookAvatars = append([]AudioBookAvatar(nil), avatars...)
}

// AudioBookAvatars returns generated speaker portraits for the video post-pass.
func (o *Orchestrator) AudioBookAvatars() []AudioBookAvatar {
	return append([]AudioBookAvatar(nil), o.audioBookAvatars...)
}

// recordAudioBookImageOffset stores the audio-timeline position at which the
// pipeline emitted beat's illustration. Later emissions of the same beat win
// (a re-fired marker means the image re-appeared at the newer moment — but in
// practice each beat fires once).
func (o *Orchestrator) recordAudioBookImageOffset(beat int, seconds float64) {
	if beat < 0 || seconds < 0 {
		return
	}
	o.audioBookImageOffsetsMu.Lock()
	defer o.audioBookImageOffsetsMu.Unlock()
	if o.audioBookImageOffsets == nil {
		o.audioBookImageOffsets = make(map[int]float64)
	}
	o.audioBookImageOffsets[beat] = seconds
}

// AudioBookImageOffsets returns a copy of the per-beat audio offsets (seconds)
// captured during the live run. Empty for non-audiobook runs or when no scene
// markers fired.
func (o *Orchestrator) AudioBookImageOffsets() map[int]float64 {
	o.audioBookImageOffsetsMu.Lock()
	defer o.audioBookImageOffsetsMu.Unlock()
	out := make(map[int]float64, len(o.audioBookImageOffsets))
	for k, v := range o.audioBookImageOffsets {
		out[k] = v
	}
	return out
}

// audioBookImageURLs returns the per-beat URL slice (index = beat) the
// pipeline emits into the chat transcript on each `<scene N/>` marker.
func (o *Orchestrator) audioBookImageURLs() []string {
	if len(o.audioBookImages) == 0 {
		return nil
	}
	maxBeat := 0
	for _, img := range o.audioBookImages {
		if img.Beat > maxBeat {
			maxBeat = img.Beat
		}
	}
	urls := make([]string, maxBeat+1)
	for _, img := range o.audioBookImages {
		if img.Beat >= 0 && img.Beat < len(urls) {
			urls[img.Beat] = img.URL
		}
	}
	return urls
}

func (o *Orchestrator) buildAudioBookAgents() error {
	name := o.Topic.AudioBookHost.Name
	if strings.TrimSpace(name) == "" {
		name = "Narrator"
	}
	base := o.makeAgent(config.AgentSpec{
		Name:    name,
		Model:   o.Topic.AudioBookHost.Model,
		BaseURL: o.Topic.AudioBookHost.BaseURL,
		APIKey:  o.Topic.AudioBookHost.APIKey,
	}, agent.RoleSeriesHost, o.Env.HostModel)
	o.Registry.SeriesHost = base
	// Seed the series-character roster from the audiobook speakers so Setup's
	// assignSeriesCharacterVoices picks a distinct Azure neural voice per
	// speaker and pushes it onto the host. Without this the roster stays empty,
	// every <char-N> span collapses to the narrator voice, and the audiobook
	// sounds single-voiced even when speakers are configured. A caller that
	// already installed a richer roster (e.g. the audiobook prepare step) wins.
	if len(o.seriesCharacters) == 0 && len(o.Topic.AudioBookSpeakers) > 0 {
		cast := make([]SeriesCharacter, 0, len(o.Topic.AudioBookSpeakers))
		for _, s := range o.Topic.AudioBookSpeakers {
			cast = append(cast, SeriesCharacter{
				Name:        s.Name,
				Gender:      s.Gender,
				Description: s.Description,
			})
		}
		o.seriesCharacters = cast
	}
	return nil
}

func (o *Orchestrator) newAudioBookPlanner() Planner {
	return NewAudioBookPlanner(o.Topic, o.Registry, o.audioBookEnd)
}

func audioBookOutline(t *config.DebateTopic) string {
	if t == nil {
		return ""
	}
	outline := strings.TrimSpace(t.Surface)
	if outline != "" {
		return outline
	}
	var b strings.Builder
	if summary := strings.TrimSpace(t.Background); summary != "" {
		b.WriteString("# Overall Summary\n\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	for i, ch := range t.AudioBookChapters {
		fmt.Fprintf(&b, "## Chapter %d: %s", i+1, strings.TrimSpace(ch.Title))
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(ch.Summary))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func audioBookCharacters(t *config.DebateTopic) []agent.SeriesCharacter {
	if t == nil || len(t.AudioBookSpeakers) == 0 {
		return nil
	}
	out := make([]agent.SeriesCharacter, 0, len(t.AudioBookSpeakers))
	for _, s := range t.AudioBookSpeakers {
		out = append(out, agent.SeriesCharacter{
			Name:        s.Name,
			Gender:      s.Gender,
			Description: s.Description,
		})
	}
	return out
}
