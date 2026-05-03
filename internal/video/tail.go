package video

import "sync"

// tailWriter is an io.Writer that retains only the trailing N bytes ever
// written. Used to keep a small in-memory window of ffmpeg stderr so we can
// log the most recent diagnostics when the encoder dies.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newTailWriter(capacity int) *tailWriter {
	return &tailWriter{cap: capacity}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= t.cap {
		t.buf = append(t.buf[:0], p[len(p)-t.cap:]...)
		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.cap {
		t.buf = t.buf[len(t.buf)-t.cap:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
