package llm

// Delta is one streamed event from the LLM: a text chunk, a (partial) tool call,
// or the terminal Done marker.
type Delta struct {
	TextChunk string
	ToolCall  *DeltaToolCall
	Done      bool
}

// DeltaToolCall holds one chunk of a streamed tool call. Streaming providers
// emit ID + name in the first chunk and append arguments incrementally; the
// receiver must accumulate by Index.
type DeltaToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// Stream wraps the underlying SSE stream and exposes a channel of Deltas plus
// a terminal error.
type Stream struct {
	deltas chan Delta
	errCh  chan error
	stop   func()
}

// Deltas returns the read end of the delta channel. Closed when the stream finishes.
func (s *Stream) Deltas() <-chan Delta { return s.deltas }

// Err returns the terminal error after Deltas() is fully drained, or nil.
func (s *Stream) Err() error {
	select {
	case e := <-s.errCh:
		return e
	default:
		return nil
	}
}

// Close stops the underlying stream early.
func (s *Stream) Close() {
	if s.stop != nil {
		s.stop()
	}
}
