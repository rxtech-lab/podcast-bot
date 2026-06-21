package contentcreator

import (
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Tracker tracks elapsed time and per-speaker speaking budget.
type Tracker struct {
	mu           sync.RWMutex
	start        time.Time
	total        time.Duration
	perSpeaker   map[string]time.Duration
	overallUsed  time.Duration
	usageByModel map[string]llm.Usage
}

// NewTracker starts the clock.
func NewTracker(total time.Duration) *Tracker {
	return &Tracker{
		start:        time.Now(),
		total:        total,
		perSpeaker:   map[string]time.Duration{},
		usageByModel: map[string]llm.Usage{},
	}
}

// Elapsed returns wall-clock time since the tracker started.
func (t *Tracker) Elapsed() time.Duration {
	return time.Since(t.start)
}

// Remaining returns total minus elapsed (clamped at zero).
func (t *Tracker) Remaining() time.Duration {
	r := t.total - t.Elapsed()
	if r < 0 {
		return 0
	}
	return r
}

// Total returns the configured total budget.
func (t *Tracker) Total() time.Duration { return t.total }

// AddSpeaking adds d to a speaker's running total.
func (t *Tracker) AddSpeaking(speaker string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.perSpeaker[speaker] += d
	t.overallUsed += d
}

// Used returns a speaker's accumulated speaking time.
func (t *Tracker) Used(speaker string) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.perSpeaker[speaker]
}

// FairShare returns the per-speaker budget given a count of equal-share speakers.
func (t *Tracker) FairShare(speakers int) time.Duration {
	if speakers <= 0 {
		return t.total
	}
	return t.total / time.Duration(speakers)
}

// AddLLMUsage records one completed LLM call.
func (t *Tracker) AddLLMUsage(u llm.Usage) {
	if t == nil || u.TotalTokens == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	model := u.Model
	if model == "" {
		model = "unknown"
	}
	current := t.usageByModel[model]
	current.Model = model
	current.PromptTokens += u.PromptTokens
	current.CompletionTokens += u.CompletionTokens
	current.TotalTokens += u.TotalTokens
	if u.CostKnown {
		current.CostUSD += u.CostUSD
		current.CostKnown = true
	}
	t.usageByModel[model] = current
}

// LLMSummary returns aggregate LLM token and cost usage for the run.
func (t *Tracker) LLMSummary() llm.UsageSummary {
	if t == nil {
		return llm.UsageSummary{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := llm.UsageSummary{ByModel: map[string]llm.Usage{}}
	for model, usage := range t.usageByModel {
		out.PromptTokens += usage.PromptTokens
		out.CompletionTokens += usage.CompletionTokens
		out.TotalTokens += usage.TotalTokens
		if usage.CostKnown {
			out.CostUSD += usage.CostUSD
			out.CostKnown = true
		}
		out.ByModel[model] = usage
	}
	return out
}
