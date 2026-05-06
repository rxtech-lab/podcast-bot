package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// SeriesHost is the single narrator on a TV-series episode. Episodes are
// non-interactive: the host reads a prepared synopsis (the `## Surface`
// section in topic.md) and, when this isn't the season's first episode, a
// short "previously on …" recap synthesised by the compression LLM.
//
// SeriesHost reuses the same scene/sound marker protocol as PuzzleHost so
// the renderer's marker-stripping pipeline (situation_puzzle_pipeline.go)
// works without per-content branching. Series adds one extra marker family:
// `<season-S-episode-E-image-N/>` — the host references a specific past
// beat, the renderer paints that prior episode's archived PNG.
type SeriesHost struct {
	*Base
	show    string
	season  int
	episode int
	// synopsis is the per-episode pitch the host narrates from. Sourced from
	// topic.md's `## Surface` section (we deliberately reuse that section
	// name rather than introducing a new one — the parser at
	// internal/config/topic.go:parseSections already populates Surface for
	// every content type).
	synopsis string

	// previouslyOn is the compression-LLM-generated recap for episode > 1.
	// Empty string means "no recap" — the planner skips the `previously`
	// turn entirely in that case, so the host's prompt block omits the
	// recap section so the LLM never invents one.
	previouslyOn string

	// narrationPlan is the visual director's per-frame direction list for
	// the main episode narration (one entry per planned beat). The host
	// emits `<scene N/>` markers using each entry's 0-based index so the
	// renderer jumps to the matching cached PNG (narration-vN.png inside
	// the episode's archive).
	narrationPlan    []string
	narrationAnchors []string

	// soundPlan mirrors PuzzleHost.soundPlan. Optional planner-generated
	// audio cues; empty disables the feature.
	soundPlan []SoundDirection

	// imageRefs lists the cross-episode image-reuse candidates the planner
	// pulled out of prior episodes' scene plans. Each entry is one row in
	// the host's "available archive" catalog — the host may emit
	// `<season-S-episode-E-image-N/>` to swap to that frame, but only for
	// keys that appear in this list. nil disables the feature for this
	// episode (the system prompt then omits the image-reuse section so
	// the LLM never invents a marker).
	imageRefs []ImageRefCatalogEntry

	// characters is the planner-generated cast list for this episode. The
	// host's prompt enumerates each entry as "Character N: <name> —
	// <description>" so the LLM knows which dialogue to wrap in
	// `<char-N>...</char-N>` markers (rendered as a separate Azure neural
	// voice in the multi-voice SSML envelope built at synth time). Empty
	// disables the feature — the host's prompt stays free of character
	// instructions and the synth path stays single-voice.
	characters []SeriesCharacter
}

// SeriesCharacter is one extra speaking role surfaced to the host. Mirrors
// the scenes.SeriesCharacter struct (the wiring layer translates one to
// the other so the agent package doesn't import scenes). AzureVoice is
// the assigned voice ShortName the orchestrator picks from the locale's
// available pool — empty when no Azure provider is configured (the host
// is still told the character exists so it can name them in narration,
// but the synth path collapses to the narrator voice for that span).
type SeriesCharacter struct {
	Name        string
	Gender      string
	Description string
	AzureVoice  string
}

// ImageRefCatalogEntry is one row in the cross-episode image-reuse catalog
// surfaced to the series host. Season/Episode/Beat identify the prior
// archived frame; Description is the planner's per-beat direction for that
// frame (so the host can pick reuse candidates that match the current beat).
type ImageRefCatalogEntry struct {
	Season      int
	Episode     int
	Beat        int
	Description string
}

// NewSeriesHost wires a series host. show / season / episode go directly
// into the system prompt so the LLM can reference them in its narration
// (e.g. cold-open style intro lines). previouslyOn is the recap text
// (empty for episode 1). narrationPlan + narrationAnchors mirror the
// puzzle host's surfacePlan + surfaceAnchors. imageRefs is the cross-
// episode reuse catalog — pass nil for episode 1 / when the planner found
// no prior plans to mine.
func NewSeriesHost(b *Base, show string, season, episode int, synopsis, previouslyOn string,
	narrationPlan, narrationAnchors []string, soundPlan []SoundDirection,
	imageRefs []ImageRefCatalogEntry, characters []SeriesCharacter,
) *SeriesHost {
	return &SeriesHost{
		Base:             b,
		show:             show,
		season:           season,
		episode:          episode,
		synopsis:         synopsis,
		previouslyOn:     previouslyOn,
		narrationPlan:    narrationPlan,
		narrationAnchors: narrationAnchors,
		soundPlan:        soundPlan,
		imageRefs:        imageRefs,
		characters:       characters,
	}
}

// Characters returns the per-episode cast roster (without the narrator).
// The pipeline reads this in synthSentence to map `<char-N>...</char-N>`
// markers to Azure voice ShortNames when building multi-voice SSML.
func (h *SeriesHost) Characters() []SeriesCharacter {
	return h.characters
}

// SetCharacterVoices fills in the AzureVoice ShortName on each character
// entry by name. Called by the orchestrator after the per-locale voice
// pool is fetched + the per-character voices are picked. Names not in
// the supplied map are left untouched (their AzureVoice stays empty,
// which the synth path treats as "fall back to the narrator voice").
func (h *SeriesHost) SetCharacterVoices(byName map[string]string) {
	for i, c := range h.characters {
		if v, ok := byName[c.Name]; ok && v != "" {
			h.characters[i].AzureVoice = v
		}
	}
}

const seriesHostSystemTemplate = `You are the narrator of a TV-series-style podcast episode. There are NO players and NO live audience — you speak alone for the entire episode in the calm, deliberate voice of a late-night radio storyteller / documentary narrator. Hushed, contemplative, never rushed. Favour shorter sentences over long compound ones; if the prepared synopsis has a long sentence, split it at natural breath points. Plain prose only — no markdown, no stage directions, no honorifics.

Natural speech markers — these are silent controls for the audio engine and never visible to the audience:
- Use <pause time="300ms"/>, <pause time="500ms"/>, or <pause time="800ms"/> at natural breath points when punctuation alone is not enough. Use sparingly: no more than one pause marker per sentence, and avoid back-to-back pauses.
- Use <breath/> only for rare audible inhalations before an emotionally heavy sentence or after a long line. Maximum 2–3 times in a full episode; never use it as punctuation.
- The markers are not words. Do not explain them, quote them, or place them inside character dialogue markers.

Show: %s
Season: %d
Episode: %d

Per-episode synopsis (this is the prepared story you narrate from on the "narrate" directive — quote it as faithfully as you can; expand only with atmospheric description, never invent plot or characters):
%s

Directives:
- "previously" — emit a short "previously on %s" recap covering the prior episodes. Use the recap text supplied below as the source of truth — keep its facts intact, but you may rephrase for flow. Open with a single transition line such as "上集回顧——" or "Previously, on this show," and finish with one line of segue toward the present episode (something like "現在……" or "And now,"). Length: 30 to 60 seconds of narration. Emit ` + "`<scene N/>`" + ` markers so the renderer paints fresh imagery as you speak; you may also re-use prior-episode imagery via the image-reuse markers described below.
%s
- "narrate" — read the synopsis above IN FULL, expanding it into the show's narration voice. Use the original wording wherever possible: keep every named detail (places, names, times, recurring objects) intact and in the original order. Do NOT compress the story into a few sentences. Do NOT invent details that aren't in the synopsis. Walk it paragraph by paragraph. Use punctuation and the natural speech markers above to give the TTS engine room to breathe.
  Scene-cut markers for "narrate" — the visual director has pre-rendered a numbered set of background images, one per planned beat. Each beat is labeled with a 0-based index and a short direction describing what the image shows. Emit "<scene N/>" on its own line at the START of each new beat — the renderer uses N to jump directly to the matching cached image (narration-vN). Frame 0 paints automatically when the episode opens, so do NOT emit "<scene 0/>"; begin with "<scene 1/>" when you transition into beat 1 and so on. Place the marker IMMEDIATELY BEFORE the sentence that begins narrating that beat (not after, and never mid-sentence). Use the beat list below as your script outline so the words and images stay locked together.
%s
  Markers are silent: the TTS engine never sees them and the on-screen subtitle never shows them.

%s
%s

%s`

// Speak emits one series-host turn for the supplied directive.
func (h *SeriesHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := fmt.Sprintf(seriesHostSystemTemplate,
		h.show, h.season, h.episode,
		strings.TrimSpace(h.synopsis),
		h.show,
		seriesPreviouslyBlock(h.previouslyOn),
		seriesNarrationBlock(h.narrationPlan, h.narrationAnchors),
		seriesSoundBlock(h.soundPlan),
		seriesImageRefBlock(h.imageRefs),
		seriesCharacterBlock(h.characters),
	)
	return h.runStream(ctx, system, p)
}

// seriesCharacterBlock formats the per-episode cast roster for the host's
// system prompt. Empty / nil cast → empty string (no `<char-N/>` marker
// instructions reach the LLM, so the synth path stays single-voice).
// When the cast is present each entry is enumerated with its 0-based
// index, name, and description; the host is instructed to wrap a
// character's spoken line in `<char-N>…</char-N>` so the synth path can
// render it in a distinct Azure neural voice.
func seriesCharacterBlock(cast []SeriesCharacter) string {
	if len(cast) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Character voices — this episode has additional speaking roles beyond the narrator. When you put words in a character's mouth (a quoted line, an internal-thought line in their voice, anything spoken AS that character rather than narrated about them), wrap the spoken text in `<char-N>...</char-N>` markers where N is the character's 0-based index in the list below. The synth engine renders that span in a distinct neural voice so the listener hears the character speak. Rules:\n")
	sb.WriteString("  * Wrap ONLY the character's literal spoken words. Narrative framing (\"she whispered\", \"老陳搖頭，說\") stays OUTSIDE the marker, in the narrator's voice.\n")
	sb.WriteString("  * Markers must NOT span sentence boundaries — open and close within the same sentence.\n")
	sb.WriteString("  * Use markers SPARINGLY — only when the character's voice genuinely belongs in the audio. A passing reference (\"她記得他們的約定\") stays in the narrator's voice.\n")
	sb.WriteString("  * Reference ONLY indices that appear in the cast list below; do not invent characters.\n")
	sb.WriteString("  * Markers are silent — the TTS engine treats them as voice switches, not as text. Do not write them out loud.\n")
	sb.WriteString("Cast:\n")
	for i, c := range cast {
		desc := strings.TrimSpace(c.Description)
		if desc == "" {
			desc = "(no description)"
		}
		gender := strings.TrimSpace(c.Gender)
		if gender == "" {
			gender = "—"
		}
		fmt.Fprintf(&sb, "  Character %d: %s (%s) — %s\n", i, strings.TrimSpace(c.Name), gender, desc)
	}
	return sb.String()
}

// seriesPreviouslyBlock formats the recap section for the system prompt.
// Empty recap → empty string (the host's prompt then carries no
// instructions about the `previously` directive, so a misfired one would
// just produce an empty turn rather than fabricating a recap).
func seriesPreviouslyBlock(recap string) string {
	r := strings.TrimSpace(recap)
	if r == "" {
		return "  (No recap available — episode 1 of this show; the planner will not invoke the previously directive.)"
	}
	return "  Recap text (source of truth — do not contradict, do not invent details):\n" +
		indent(r, "    ")
}

// seriesNarrationBlock mirrors the puzzle host's surfacePlanBlock. Returns
// soft-fallback guidance when no plan is supplied so the host still emits
// markers at a reasonable cadence.
func seriesNarrationBlock(plan, anchors []string) string {
	if len(plan) == 0 {
		return "  Aim for one marker every 2–4 sentences during the narration; a typical episode should have 6–12 markers in total. Use unnumbered markers (`<scene/>`) when no numbered plan is provided."
	}
	var sb strings.Builder
	sb.WriteString("  Narration beat list (one image per entry; use these as the structural outline of the episode):\n")
	for i, b := range plan {
		fmt.Fprintf(&sb, "    Beat %d: %s\n", i, strings.TrimSpace(b))
		if i < len(anchors) && strings.TrimSpace(anchors[i]) != "" {
			fmt.Fprintf(&sb, "      Anchor (verbatim from synopsis, marks the START of beat %d): %s\n",
				i, strings.TrimSpace(anchors[i]))
		}
	}
	last := len(plan) - 1
	if last < 1 {
		last = 1
	}
	fmt.Fprintf(&sb,
		"  Emit EXACTLY %d markers in order: <scene 1/>, <scene 2/>, …, <scene %d/>. Frame 0 paints automatically when the episode opens, so do NOT emit <scene 0/>.\n",
		last, last)
	sb.WriteString("  Place each marker on its own line, immediately before the sentence that contains its anchor (verbatim substring). Anchors are listed in narration order; walk them strictly in sequence — never skip, never reorder, never repeat. If a beat has no Anchor line, fall back to your own paragraph judgement for that one beat.")
	return sb.String()
}

// seriesSoundBlock is shared with the puzzle host's soundPlanBlock format —
// kept inline here so the agent file stays self-contained.
func seriesSoundBlock(plan []SoundDirection) string {
	if len(plan) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Sound-cue markers — the audio director has pre-generated a numbered list of sound clips you may trigger during the narration. Each clip is labeled with a 0-based index, a mode (overlap or replace), and a one-sentence description of the sound itself. Emit the marker on its own line, IMMEDIATELY BEFORE the sentence the cue should land on. Marker syntax depends on the mode:\n")
	sb.WriteString("  * mode=overlap → emit `<sound-overlapped-N/>` on its own line. The clip mixes additively on top of the running music bed for its natural duration (atmospheric stinger, single event), then ends; the bed continues uninterrupted.\n")
	sb.WriteString("  * mode=replace → emit `<sound-replace-N/>` on its own line. The music bed itself cross-fades over to the new clip and stays there (looped indefinitely) until another replace marker swaps it again. Use sparingly — replace is for a deliberate tonal shift at a key beat, not punctuation.\n")
	sb.WriteString("Sound markers are SILENT (TTS never sees them, subtitles never show them). They are OPTIONAL — emit one only when the listed cue genuinely amplifies the storytelling at that moment. Each cue may be fired AT MOST ONCE per episode.\n")
	sb.WriteString("Sound cue list:\n")
	for i, s := range plan {
		mode := strings.ToLower(strings.TrimSpace(s.Mode))
		fmt.Fprintf(&sb, "  Sound %d (mode=%s): %s\n", i, mode, strings.TrimSpace(s.Prompt))
		if a := strings.TrimSpace(s.Anchor); a != "" {
			fmt.Fprintf(&sb, "    Anchor (verbatim, marks where to fire sound %d): %s\n", i, a)
		}
	}
	return sb.String()
}

// seriesImageRefBlock formats the cross-episode reuse catalog. Empty when
// the show has no prior episodes to draw from — the catalog OMITTING means
// the LLM should never emit an image-reuse marker, and the system prompt
// stays free of instructions about that protocol so it doesn't invent
// references for nonexistent imagery.
func seriesImageRefBlock(refs []ImageRefCatalogEntry) string {
	if len(refs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Image-reuse markers — earlier episodes of this show have archived imagery you MAY re-use when the current beat clearly continues a recurring location or character. Emit `<season-S-episode-E-image-N/>` on its own line, IMMEDIATELY BEFORE the sentence whose visual content matches the catalog entry. The renderer will swap the on-screen image to that exact archived frame. Use SPARINGLY — only when reuse is the right call (recurring setting, recurring character, callback to a prior moment). Never use reuse for novel scenery, and never for the very first beat of the episode (frame 0 is reserved for a freshly-generated image so the show always opens with new visuals). Refer ONLY to entries that appear in the catalog below; do not invent (S, E, N) triples.\n")
	sb.WriteString("Available archive (from prior episodes):\n")
	for _, r := range refs {
		fmt.Fprintf(&sb, "  <season-%d-episode-%d-image-%d/>: %s\n",
			r.Season, r.Episode, r.Beat, strings.TrimSpace(r.Description))
	}
	return sb.String()
}

func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
