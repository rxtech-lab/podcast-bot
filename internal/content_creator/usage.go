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

// FormatUsageSummary renders the short user-visible usage line stored in
// server logs and shown by the iOS player.
func FormatUsageSummary(u llm.UsageSummary) string {
	if u.TotalTokens == 0 {
		return ""
	}
	text := fmt.Sprintf("Token usage: %s total (%s input, %s output)",
		formatInt(u.TotalTokens), formatInt(u.PromptTokens), formatInt(u.CompletionTokens))
	if u.CostKnown {
		text += fmt.Sprintf(" · total cost $%.6f", u.CostUSD)
	} else {
		text += " · total cost unavailable"
	}
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
