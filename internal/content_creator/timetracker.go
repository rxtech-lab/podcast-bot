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

	// Non-LLM API usage, accumulated so the run's total cost reflects every
	// paid call. ttsChars is the running character count handed to the TTS
	// provider; musicGens is the count of billed Lyria generations. The
	// per-unit prices are set once via SetMediaPricing from env config.
	ttsChars               int64
	musicGens              int64
	ttsCostPerMillionChars float64
	lyriaCostPerGen        float64

	usageSnapshot func(llm.UsageSummary)
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

// SetMediaPricing records the per-unit prices used to value TTS and music
// usage in the run summary. ttsPerMillionChars is dollars per 1M synthesised
// characters; lyriaPerGen is dollars per billed music generation. Safe to call
// once at construction before any usage is recorded.
func (t *Tracker) SetMediaPricing(ttsPerMillionChars, lyriaPerGen float64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ttsCostPerMillionChars = ttsPerMillionChars
	t.lyriaCostPerGen = lyriaPerGen
}

// SetUsageSnapshotCallback registers a callback invoked after chargeable LLM,
// TTS, or music usage is recorded. The callback receives the current aggregate
// snapshot and is called outside the tracker lock so it can safely persist to DB.
func (t *Tracker) SetUsageSnapshotCallback(fn func(llm.UsageSummary)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.usageSnapshot = fn
	t.mu.Unlock()
}

// AddTTSCharacters adds n characters to the TTS usage counter (one synthesis
// call's worth of text). No-op for n <= 0 so a failed/empty call costs nothing.
func (t *Tracker) AddTTSCharacters(n int64) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	t.ttsChars += n
	t.mu.Unlock()
	t.emitUsageSnapshot()
}

// AddMusicGeneration records one billed Lyria music-generation API call. Cache
// hits do not call this, so the count tracks real spend.
func (t *Tracker) AddMusicGeneration() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.musicGens++
	t.mu.Unlock()
	t.emitUsageSnapshot()
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
	t.mu.Unlock()
	t.emitUsageSnapshot()
}

func (t *Tracker) emitUsageSnapshot() {
	if t == nil {
		return
	}
	t.mu.RLock()
	fn := t.usageSnapshot
	t.mu.RUnlock()
	if fn == nil {
		return
	}
	fn(t.LLMSummary())
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
	out.TTSCharacters = t.ttsChars
	out.TTSCostUSD = float64(t.ttsChars) * t.ttsCostPerMillionChars / 1_000_000
	out.MusicGenerations = t.musicGens
	out.MusicCostUSD = float64(t.musicGens) * t.lyriaCostPerGen
	return out
}
