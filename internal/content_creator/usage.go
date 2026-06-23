package contentcreator

import "github.com/sirily11/debate-bot/internal/llm"

// UsageSummary returns the aggregate provider usage recorded for the run.
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
