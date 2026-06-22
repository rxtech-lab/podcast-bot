package contentcreator

import (
	"fmt"
	"strconv"

	"github.com/sirily11/debate-bot/internal/llm"
)

// UsageSummary returns the aggregate LLM usage for the completed run.
func (o *Orchestrator) UsageSummary() llm.UsageSummary {
	if o == nil || o.Tracker == nil {
		return llm.UsageSummary{}
	}
	return o.Tracker.LLMSummary()
}

// RecordMusicGeneration records one billed Lyria music-generation call against
// the run tracker. Asset-prep code (discussion/series music) passes this as the
// musicgen client's usage recorder so generation cost lands in the run total;
// cache hits never call it. Safe on a nil orchestrator/tracker.
func (o *Orchestrator) RecordMusicGeneration() {
	if o == nil {
		return
	}
	o.Tracker.AddMusicGeneration()
}

// FormatUsageSummary renders the short user-visible usage line stored in
// server logs and shown by the iOS player.
func FormatUsageSummary(u llm.UsageSummary) string {
	if u.TotalTokens == 0 {
		return ""
	}
	text := fmt.Sprintf("Token usage: %s total (%s input, %s output)",
		formatInt(u.TotalTokens), formatInt(u.PromptTokens), formatInt(u.CompletionTokens))
	if !u.CostKnown {
		text += " · total cost unavailable"
		return text
	}
	// Break the spend down by source so the user can see what drives the bill,
	// then report the grand total (LLM tokens + TTS synthesis + music gen).
	if u.TTSCostUSD > 0 || u.MusicCostUSD > 0 {
		text += fmt.Sprintf(" · LLM $%.6f", u.CostUSD)
		if u.TTSCostUSD > 0 {
			text += fmt.Sprintf(" · TTS $%.6f (%s chars)", u.TTSCostUSD, formatInt(u.TTSCharacters))
		}
		if u.MusicCostUSD > 0 {
			text += fmt.Sprintf(" · music $%.6f (%s gens)", u.MusicCostUSD, formatInt(u.MusicGenerations))
		}
	}
	text += fmt.Sprintf(" · total cost $%.6f", u.TotalCostUSD())
	return text
}

func formatInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	first := len(s) % 3
	if first == 0 {
		first = 3
	}
	out = append(out, s[:first]...)
	for i := first; i < len(s); i += 3 {
		out = append(out, ',')
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}
