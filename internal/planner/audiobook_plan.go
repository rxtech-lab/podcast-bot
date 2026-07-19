package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// assembleAudioBookPlan handles write_plan/update_plan for audiobooks. When
// the conversation carries an uploaded source, it slices the real text at the
// model's chapter markers, walks each slice to extract the speaking cast,
// uploads the slices to durable storage, and stamps the resulting keys onto
// the assembled plan. Split failures are model-facing tool errors so the
// model retries with corrected markers within the same turn.
func (s *conversationSession) assembleAudioBookPlan(ctx context.Context, args string) (*Result, string, error) {
	d, err := decodeAudioBookDraft(args)
	if err != nil {
		return nil, "", err
	}
	source := strings.TrimSpace(s.opts.AudioBookSource)
	var slices []chapterSlice
	if source != "" {
		if !draftChaptersCarryMarkers(d.Chapters) {
			return nil, "", &audioBookSplitError{msg: "a source document was uploaded, so each chapter needs a start_index from the Source markers catalog (or a verbatim start_marker); the server slices the real text at those boundaries"}
		}
		// The split is positional: slice i belongs to draft chapter i. Reject
		// anything assembly would silently drop so the alignment cannot skew.
		if len(d.Chapters) > audioBookMaxChapters {
			return nil, "", fmt.Errorf("too many chapters: %d (the maximum is %d); merge adjacent sections", len(d.Chapters), audioBookMaxChapters)
		}
		for i, ch := range d.Chapters {
			if strings.TrimSpace(ch.Title) == "" || strings.TrimSpace(ch.Summary) == "" {
				return nil, "", fmt.Errorf("chapter %d needs both a title and a summary", i+1)
			}
		}
		slices, err = splitAudioBookSource(source, audioBookSourceMarkers(source), d.Chapters)
		if err != nil {
			return nil, "", err
		}
	}

	var cast []extractedCharacter
	if len(slices) > 0 {
		cast, err = s.planner.extractAudioBookCast(ctx, slices)
		if err != nil {
			// Extraction enriches the cast; it never blocks the plan.
			s.planner.emit("writing", "Character extraction failed; keeping the drafted cast")
			cast = nil
		}
	}

	res, err := s.planner.assembleAudioBookWithModel(d, s.planLanguage(), s.opts.Channel, s.sources, s.planModel())
	if err != nil {
		return nil, "", err
	}
	if len(slices) == 0 {
		return res, "", nil
	}
	topic := res.Script
	if len(topic.AudioBookChapters) != len(slices) {
		return nil, "", fmt.Errorf("internal error: %d assembled chapters vs %d source slices", len(topic.AudioBookChapters), len(slices))
	}
	mergeExtractedCast(topic, cast, s.planModel())
	for i := range topic.AudioBookChapters {
		topic.AudioBookChapters[i].StartMarker = truncate(firstLine(slices[i].Content), audioBookMarkerTextLimit)
		topic.AudioBookChapters[i].ContentChars = len(slices[i].Content)
	}
	stored := 0
	if s.opts.StoreChapterContent != nil {
		for i := range topic.AudioBookChapters {
			key, err := s.opts.StoreChapterContent(ctx, i+1, []byte(slices[i].Content))
			if err != nil {
				// Storage is best-effort: without keys the plan still works,
				// generation just falls back to outline-only narration. Keys
				// are all-or-nothing so a batch never mixes grounded and
				// ungrounded chapters silently.
				s.planner.emit("writing", "Storing chapter text failed; the plan will narrate from the outline only")
				for j := range topic.AudioBookChapters {
					topic.AudioBookChapters[j].ContentKey = ""
				}
				stored = 0
				break
			}
			topic.AudioBookChapters[i].ContentKey = key
			stored++
		}
	}
	// Chapters changed after assembly rendered the plan markdown; re-render so
	// the persisted markdown round-trips the new fields.
	md, err := topic.RenderMarkdown()
	if err != nil {
		return nil, "", fmt.Errorf("render planned audiobook script: %w", err)
	}
	res.Markdown = md
	return res, audioBookPlanNote(slices, cast, stored), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// mergeExtractedCast appends extracted characters that the draft's speaker
// list is missing. Draft entries stay canonical; the narrator is never added.
func mergeExtractedCast(topic *config.DebateTopic, cast []extractedCharacter, model string) {
	if len(cast) == 0 {
		return
	}
	seen := map[string]bool{normalizedSpeakerName(topic.AudioBookHost.Name): true}
	for _, sp := range topic.AudioBookSpeakers {
		seen[normalizedSpeakerName(sp.Name)] = true
	}
	for _, c := range cast {
		name := strings.TrimSpace(c.Name)
		key := normalizedSpeakerName(name)
		if name == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		topic.AudioBookSpeakers = append(topic.AudioBookSpeakers, config.AudioBookSpeaker{
			Name:        name,
			Gender:      normalizeSpeakerGender(c.Gender),
			Description: strings.TrimSpace(c.Description),
			Model:       model,
		})
	}
}

// audioBookPlanNote summarizes the split/extraction for the tool result so the
// planner model can refine chapter speaker lists via update_plan.
func audioBookPlanNote(slices []chapterSlice, cast []extractedCharacter, stored int) string {
	minChars, maxChars := len(slices[0].Content), len(slices[0].Content)
	for _, sl := range slices[1:] {
		if len(sl.Content) < minChars {
			minChars = len(sl.Content)
		}
		if len(sl.Content) > maxChars {
			maxChars = len(sl.Content)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "The source was split into %d chapters (%d–%d chars each)", len(slices), minChars, maxChars)
	if stored > 0 {
		sb.WriteString(" and each chapter's real text was stored for the narrator to read from during generation")
	}
	sb.WriteByte('.')
	if len(cast) > 0 {
		names := make([]string, 0, len(cast))
		for _, c := range cast {
			if g := normalizeSpeakerGender(c.Gender); g != "" {
				names = append(names, fmt.Sprintf("%s (%s)", strings.TrimSpace(c.Name), g))
			} else {
				names = append(names, strings.TrimSpace(c.Name))
			}
		}
		fmt.Fprintf(&sb, " A walk through the actual chapter text found these speaking voices: %s. They were merged into the plan's speakers; assign them to the chapters where they talk via update_plan if any dialogue chapters are missing them.", strings.Join(names, ", "))
	}
	return sb.String()
}
