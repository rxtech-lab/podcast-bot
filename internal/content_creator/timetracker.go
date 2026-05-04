package contentcreator

import (
	"sync"
	"time"
)

// Tracker tracks elapsed time and per-speaker speaking budget.
type Tracker struct {
	mu          sync.RWMutex
	start       time.Time
	total       time.Duration
	perSpeaker  map[string]time.Duration
	overallUsed time.Duration
}

// NewTracker starts the clock.
func NewTracker(total time.Duration) *Tracker {
	return &Tracker{
		start:      time.Now(),
		total:      total,
		perSpeaker: map[string]time.Duration{},
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
