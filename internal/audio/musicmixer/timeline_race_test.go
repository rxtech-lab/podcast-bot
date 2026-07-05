package musicmixer

import (
	"testing"
	"time"
)

// pacedZeroReader serves an endless stream of zero PCM, sleeping per chunk so
// mixLoop iterates at a bounded, test-controlled rate (stands in for the -re
// paced music decoder).
type pacedZeroReader struct {
	chunk time.Duration
}

func (r *pacedZeroReader) Read(p []byte) (int, error) {
	time.Sleep(r.chunk)
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
func (r *pacedZeroReader) Close() error { return nil }

// slowWriter models downstream backpressure (encoder pipe → LiveStream -re):
// each Write blocks for a while, which is exactly the window where the old
// ttsActive computation raced with TTS PCM arriving mid-iteration.
type slowWriter struct {
	delay time.Duration
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return len(p), nil
}
func (w *slowWriter) Close() error { return nil }

// TestBurstStartSyncNotRacedByArrivalTiming reproduces the "captions shift by
// one bed-only gap" bug: TTS PCM that lands in ttsCh between the mix loop's
// drain and its end-of-iteration bookkeeping used to mark the stream active
// without having been mixed, so the burst's first mixed chunk skipped
// recordTTSBurstStart and the TTS→output map lost the gap. Every burst after
// an idle gap must produce a sync point regardless of arrival phase, so this
// sweeps arrivals across the iteration period.
func TestBurstStartSyncNotRacedByArrivalTiming(t *testing.T) {
	m := &Mixer{
		musicOut: &pacedZeroReader{chunk: time.Millisecond},
		encIn:    &slowWriter{delay: 3 * time.Millisecond},
		ttsCh:    make(chan []byte, ttsBufferChunks),
		swapCh:   make(chan musicSwap, 1),
		drainCh:  make(chan struct{}),
		mixDone:  make(chan struct{}),
		ttsScale: 1,
	}
	go m.mixLoop()

	ttsMixed := func() int64 {
		m.syncMu.Lock()
		defer m.syncMu.Unlock()
		return m.ttsMixed
	}
	syncCount := func() int {
		m.syncMu.Lock()
		defer m.syncMu.Unlock()
		return len(m.syncPoints)
	}

	const bursts = 24
	const chunksPerBurst = 4
	var fed int64
	// Iteration period ≈ 1ms music pacing + 3ms blocked write ≈ 4ms. Sweep
	// burst arrival phase in sub-period steps so some arrivals land inside
	// the write-block window (the old race) and some outside.
	for b := 0; b < bursts; b++ {
		// idle gap: wait for full drain, then several bed-only iterations
		deadline := time.Now().Add(5 * time.Second)
		for ttsMixed() < fed {
			if time.Now().After(deadline) {
				t.Fatalf("burst %d: mixer never drained (mixed=%d fed=%d)", b, ttsMixed(), fed)
			}
			time.Sleep(200 * time.Microsecond)
		}
		time.Sleep(40 * time.Millisecond) // ~10 idle iterations
		time.Sleep(time.Duration(b) * 350 * time.Microsecond) // phase sweep

		for c := 0; c < chunksPerBurst; c++ {
			chunk := make([]byte, chunkBytes)
			m.ttsCh <- chunk
			fed += int64(len(chunk))
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for ttsMixed() < fed {
		if time.Now().After(deadline) {
			t.Fatalf("final drain timeout (mixed=%d fed=%d)", ttsMixed(), fed)
		}
		time.Sleep(200 * time.Microsecond)
	}
	close(m.ttsCh)
	close(m.drainCh)
	select {
	case <-m.mixDone:
	case <-time.After(5 * time.Second):
		t.Fatal("mixLoop did not exit")
	}

	if got := syncCount(); got != bursts {
		t.Errorf("sync points = %d, want %d (one per idle→active burst); "+
			"missing points mean cues after those gaps export shifted", got, bursts)
	}
}
