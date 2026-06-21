package llm

import "sync"

// Usage is the token/cost accounting reported by an OpenAI-compatible chat
// endpoint for one model call.
type Usage struct {
	Model            string  `json:"model,omitempty"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostKnown        bool    `json:"cost_known,omitempty"`
}

// UsageSummary is the aggregate usage for a full generation.
type UsageSummary struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostUSD          float64
	CostKnown        bool
	ByModel          map[string]Usage
}

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
	mu     sync.RWMutex
	usage  Usage
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

// Usage returns the accumulated token/cost usage reported for the stream.
func (s *Stream) Usage() Usage {
	if s == nil {
		return Usage{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usage
}

func (s *Stream) addUsage(u Usage) {
	if s == nil || u.TotalTokens == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage.Model == "" {
		s.usage.Model = u.Model
	}
	s.usage.PromptTokens += u.PromptTokens
	s.usage.CompletionTokens += u.CompletionTokens
	s.usage.TotalTokens += u.TotalTokens
	if u.CostKnown {
		s.usage.CostUSD += u.CostUSD
		s.usage.CostKnown = true
	}
}
