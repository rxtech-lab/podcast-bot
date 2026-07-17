package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Rolling-context-window parameters (ported from linda-assistant's compaction
// design). Token counts use a chars/4 heuristic that intentionally
// over-estimates so compaction fires early rather than late.
const (
	// ContextWindowTokens is the history budget for one QA conversation.
	ContextWindowTokens = 75_000
	// CompactThreshold triggers compaction at this fraction of the window.
	CompactThreshold = 0.75
	// KeepRecentMessages is how many trailing messages always stay verbatim.
	KeepRecentMessages = 6
	// summaryTargetTokens bounds the compaction summary's size.
	summaryTargetTokens = 1_500

	// perMessageOverheadChars pads each message for role/framing tokens.
	perMessageOverheadChars = 40
)

// EstimateTokens over-approximates the token count of a message history,
// including tool-call arguments and tool results.
func EstimateTokens(msgs []llm.Message) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content) + perMessageOverheadChars
		for _, tc := range m.ToolCalls {
			chars += len(tc.Name) + len(tc.Arguments) + perMessageOverheadChars
		}
	}
	return chars / 4
}

// NeedsCompaction reports whether the history (plus system prompt) exceeds
// the compaction threshold.
func NeedsCompaction(msgs []llm.Message, systemChars int) bool {
	return systemChars/4+EstimateTokens(msgs) > int(float64(ContextWindowTokens)*CompactThreshold)
}

// CompactionBoundary picks the split index: msgs[:boundary] is evicted (to be
// summarized), msgs[boundary:] is kept verbatim. It keeps the last
// KeepRecentMessages and then walks the boundary back so the kept slice never
// starts with a tool result whose assistant tool-call was evicted (an orphan
// tool message is an invalid OpenAI sequence). Returns 0 when there is
// nothing worth evicting.
func CompactionBoundary(msgs []llm.Message) int {
	boundary := len(msgs) - KeepRecentMessages
	if boundary <= 0 {
		return 0
	}
	for boundary > 0 && msgs[boundary].Role == llm.RoleTool {
		boundary--
	}
	return boundary
}

// SummarizeEvicted produces the compaction summary for the evicted prefix
// using the (cheap) compression model client.
func SummarizeEvicted(ctx context.Context, client *llm.Client, evicted []llm.Message) (string, error) {
	if client == nil {
		return "", fmt.Errorf("qa compaction: no summarizer client")
	}
	system := fmt.Sprintf(`You compact chat history. Summarize the conversation below, preserving ALL factual information, user questions and preferences, assistant answers and citations (podcast titles, speakers, timestamps), tool findings, and any unresolved items. Write plain prose, at most roughly %d tokens. Respond as strict JSON: {"summary": "..."}`, summaryTargetTokens)
	payload, err := client.JSON(ctx, system, renderForSummary(evicted))
	if err != nil {
		return "", err
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(payload, &out); err == nil && strings.TrimSpace(out.Summary) != "" {
		return strings.TrimSpace(out.Summary), nil
	}
	// Model ignored the JSON contract; use the raw text if it looks like prose.
	raw := strings.TrimSpace(string(payload))
	if raw != "" && !strings.HasPrefix(raw, "{") {
		return raw, nil
	}
	return "", fmt.Errorf("qa compaction: empty summary")
}

// SummaryText wraps a compaction summary in the sentinel markers the model
// history uses.
func SummaryText(summary string) string {
	return "[CONVERSATION SUMMARY]\n" + summary + "\n[END SUMMARY]\n\nThe conversation continues below:"
}

// SummaryMessage renders a stored summary as the user-role message the model
// sees in place of the evicted prefix.
func SummaryMessage(summary string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: SummaryText(summary)}
}

// renderForSummary flattens messages into readable text for the summarizer,
// bounding tool outputs so a huge retrieval result can't blow the summary call.
func renderForSummary(msgs []llm.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			sb.WriteString("User: " + m.Content + "\n")
		case llm.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				sb.WriteString("Assistant: " + m.Content + "\n")
			}
			for _, tc := range m.ToolCalls {
				sb.WriteString("Assistant called " + tc.Name + "(" + truncate(tc.Arguments, 300) + ")\n")
			}
		case llm.RoleTool:
			sb.WriteString("Tool result: " + truncate(m.Content, 800) + "\n")
		}
	}
	return sb.String()
}
