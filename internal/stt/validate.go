package stt

import "fmt"

// ValidateTranscriptTiming rejects provider output that cannot safely own
// subtitle timing. Generative STT models can return fluent text alongside a
// timestamp sequence that jumps backwards; publishing that result makes every
// following caption point at unrelated audio.
func ValidateTranscriptTiming(t *Transcript) error {
	if t == nil {
		return fmt.Errorf("transcript is nil")
	}
	if t.DurationMS <= 0 {
		return fmt.Errorf("audio duration must be positive")
	}
	var previousOffset int64 = -1
	for i, phrase := range t.Phrases {
		if phrase.OffsetMS < 0 {
			return fmt.Errorf("phrase %d has negative offset %d", i, phrase.OffsetMS)
		}
		if phrase.DurationMS <= 0 {
			return fmt.Errorf("phrase %d has non-positive duration %d", i, phrase.DurationMS)
		}
		if phrase.OffsetMS < previousOffset {
			return fmt.Errorf("phrase %d offset %d precedes the previous phrase offset %d", i, phrase.OffsetMS, previousOffset)
		}
		if phrase.DurationMS > t.DurationMS-phrase.OffsetMS {
			return fmt.Errorf("phrase %d ends after the audio duration", i)
		}
		previousOffset = phrase.OffsetMS
	}
	return nil
}
