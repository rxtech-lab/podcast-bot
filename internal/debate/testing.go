package debate

// NewForTest builds a minimal Orchestrator suitable for tests that only
// exercise the user-message queue and transcript emission. The send callback
// receives every event the orchestrator emits — wrap it with the bus +
// StampChannelID stamp the same way main.go does to mimic real channel routing.
//
// store may be nil for tests that don't care about persistence; pass a real
// *Store to verify reload-from-disk behavior. Production code must use New();
// this constructor skips agent / TTS / MCP / memory wiring so any orchestration
// call (Setup, Run) will panic.
func NewForTest(send func(any), store *Store) *Orchestrator {
	var transcript *Transcript
	if store != nil {
		transcript = NewTranscriptWithStore(store)
	} else {
		transcript = NewTranscript()
	}
	return &Orchestrator{
		Queue:      &userQueue{},
		Transcript: transcript,
		Store:      store,
		Send:       send,
	}
}
