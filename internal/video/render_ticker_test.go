package video

import (
	"testing"
	"time"
)

// renderForTest builds a Renderer the package tests can poke into directly.
// 1280×720 matches the production frame size used in cmd/render-smoke.
func renderForTest(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRendererForTest(1280, 720)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	return r
}

// case 1 from the spec: a single ShowUserMessage stays in the pending buffer
// until userMsgDebounceWindow elapses with no follow-up, then flushes to the
// queue as a one-item batch with the original username/text intact.
func TestUserTicker_SingleMessage_FlushesAfterDebounce(t *testing.T) {
	r := renderForTest(t)

	r.ShowUserMessage("hello there", "alice", 5*time.Second)

	r.mu.Lock()
	if got := len(r.userPending); got != 1 {
		r.mu.Unlock()
		t.Fatalf("pending after single push = %d, want 1", got)
	}
	if got := len(r.userQueue); got != 0 {
		r.mu.Unlock()
		t.Fatalf("queue after single push = %d, want 0 (still in debounce window)", got)
	}
	// Backdate userPendingLast past the debounce window so advance flushes.
	r.userPendingLast = time.Now().Add(-userMsgDebounceWindow - 50*time.Millisecond)
	r.advanceUserTickerLocked()
	if got := len(r.userPending); got != 0 {
		r.mu.Unlock()
		t.Fatalf("pending after flush = %d, want 0", got)
	}
	if got := len(r.userQueue); got != 1 {
		r.mu.Unlock()
		t.Fatalf("queue after flush = %d, want 1", got)
	}
	head := r.userQueue[0]
	r.mu.Unlock()

	if head.username != "alice" {
		t.Errorf("head.username = %q, want %q", head.username, "alice")
	}
	if head.text != "hello there" {
		t.Errorf("head.text = %q, want %q", head.text, "hello there")
	}
}

// case 2 from the spec: rapid-fire ShowUserMessage calls accumulate in
// pending until the user goes quiet for userMsgDebounceWindow; then the
// whole batch flushes as ONE merged ticker entry (so the audience sees them
// "at once" rather than the last message clobbering the earlier ones).
func TestUserTicker_RapidMessages_BatchAfterDebounce(t *testing.T) {
	r := renderForTest(t)

	r.ShowUserMessage("first", "alice", 5*time.Second)
	r.ShowUserMessage("second", "alice", 5*time.Second)
	r.ShowUserMessage("third", "alice", 5*time.Second)

	r.mu.Lock()
	if got := len(r.userPending); got != 3 {
		r.mu.Unlock()
		t.Fatalf("pending after 3 rapid pushes = %d, want 3", got)
	}
	if got := len(r.userQueue); got != 0 {
		r.mu.Unlock()
		t.Fatalf("queue mid-burst = %d, want 0 (still debouncing)", got)
	}
	r.userPendingLast = time.Now().Add(-userMsgDebounceWindow - 50*time.Millisecond)
	r.advanceUserTickerLocked()
	if got := len(r.userQueue); got != 1 {
		r.mu.Unlock()
		t.Fatalf("queue after burst flush = %d, want 1 merged entry", got)
	}
	head := r.userQueue[0]
	r.mu.Unlock()

	if head.username != "alice" {
		t.Errorf("head.username = %q, want %q (single-user batch)", head.username, "alice")
	}
	want := "first — second — third"
	if head.text != want {
		t.Errorf("head.text = %q, want %q", head.text, want)
	}
}

// Bonus: while the active head is scrolling, a new burst forms a second
// queue entry so the ticker keeps rolling once the head expires — this is
// the "if queue keeps filling, keep showing" half of the spec.
func TestUserTicker_QueueDrainsAndContinues(t *testing.T) {
	r := renderForTest(t)

	r.ShowUserMessage("msg-a", "alice", 5*time.Second)
	r.mu.Lock()
	r.userPendingLast = time.Now().Add(-userMsgDebounceWindow - 50*time.Millisecond)
	r.advanceUserTickerLocked()
	if got := len(r.userQueue); got != 1 {
		r.mu.Unlock()
		t.Fatalf("queue after first flush = %d, want 1", got)
	}
	r.mu.Unlock()

	// Second burst arrives while the first item is still mid-scroll.
	r.ShowUserMessage("msg-b", "bob", 5*time.Second)
	r.mu.Lock()
	r.userPendingLast = time.Now().Add(-userMsgDebounceWindow - 50*time.Millisecond)
	r.advanceUserTickerLocked()
	if got := len(r.userQueue); got != 2 {
		r.mu.Unlock()
		t.Fatalf("queue after second flush = %d, want 2 (first head + buffered next)", got)
	}

	// Force-expire the active head and advance — the second entry should
	// take over as the new active head.
	r.userExpiry = time.Now().Add(-time.Millisecond)
	r.advanceUserTickerLocked()
	if got := len(r.userQueue); got != 1 {
		r.mu.Unlock()
		t.Fatalf("queue after head expiry = %d, want 1", got)
	}
	head := r.userQueue[0]
	r.mu.Unlock()
	if head.username != "bob" || head.text != "msg-b" {
		t.Errorf("post-advance head = (%q, %q), want (bob, msg-b)", head.username, head.text)
	}
}
