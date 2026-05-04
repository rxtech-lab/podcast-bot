package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// recorder collects every callback invocation in invocation order so tests
// can assert on what the watcher emitted. Locks around the slices so the
// watcher's goroutine (writes) and the test goroutine (reads) don't race.
type recorder struct {
	mu      sync.Mutex
	ready   []string
	removed []string
}

func (r *recorder) onReady(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = append(r.ready, path)
}

func (r *recorder) onRemove(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, path)
}

func (r *recorder) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rd := append([]string(nil), r.ready...)
	rm := append([]string(nil), r.removed...)
	return rd, rm
}

// waitFor polls until cond returns true or timeout elapses. Reports a clear
// failure with the recorder's current state so debugging missed events isn't
// guesswork. Returns the recorder's final state for follow-up assertions.
func waitFor(t *testing.T, rec *recorder, timeout time.Duration, cond func(ready, removed []string) bool, what string) ([]string, []string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ready, removed := rec.snapshot()
		if cond(ready, removed) {
			return ready, removed
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher: timed out waiting for %s\n  ready:   %v\n  removed: %v", what, ready, removed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startWatcher spins up a Watcher rooted at dir with the given debounce.
// Returns the recorder + a cleanup that cancels the watcher goroutine. Test
// failures terminate via t.Fatalf so callers don't have to check errors at
// every step.
//
// debounce is a per-test knob: tests that exercise debouncing/cancellation
// pass a longer window so the assertions are deterministic even under
// `-race` (where syscall latency can push event delivery past short
// debounce windows and turn the test flaky).
func startWatcher(t *testing.T, dir string, debounce time.Duration) (*recorder, context.CancelFunc) {
	t.Helper()
	rec := &recorder{}
	// Discard log output during tests — the watcher's debug logs would
	// otherwise spam `go test -v` runs without adding signal.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := New([]string{dir}, debounce, log, Callbacks{
		OnReady:  rec.onReady,
		OnRemove: rec.onRemove,
	})
	if err != nil {
		t.Fatalf("watcher.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()
	cleanup := func() {
		cancel()
		// Drain the goroutine so we don't leak it into the next test.
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Logf("watcher.Run did not exit within 1s after cancel")
		}
	}
	// fsnotify on macOS sometimes drops events fired before the kqueue is
	// fully primed. A tiny pause makes the test deterministic.
	time.Sleep(20 * time.Millisecond)
	return rec, cleanup
}

func TestWatcher_NewMarkdownFile_FiresOnReady(t *testing.T) {
	dir := t.TempDir()
	rec, cleanup := startWatcher(t, dir, 50*time.Millisecond)
	defer cleanup()

	path := filepath.Join(dir, "topic.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ready, removed := waitFor(t, rec, 2*time.Second,
		func(ready, _ []string) bool { return len(ready) >= 1 },
		"OnReady to fire for a new .md file")

	if got, want := ready[0], path; got != want {
		t.Errorf("OnReady path: got %q, want %q", got, want)
	}
	if len(removed) != 0 {
		t.Errorf("OnRemove fired unexpectedly: %v", removed)
	}
}

func TestWatcher_NonMarkdownFile_Ignored(t *testing.T) {
	dir := t.TempDir()
	rec, cleanup := startWatcher(t, dir, 50*time.Millisecond)
	defer cleanup()

	// Drop a .txt file — should be ignored entirely.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Also a .md file so we have a positive signal that the watcher is
	// alive — without this we couldn't tell "ignored .txt" apart from "the
	// watcher never started".
	mdPath := filepath.Join(dir, "topic.md")
	if err := os.WriteFile(mdPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ready, _ := waitFor(t, rec, 2*time.Second,
		func(ready, _ []string) bool { return len(ready) >= 1 },
		"OnReady to fire for the .md file")

	for _, p := range ready {
		if filepath.Ext(p) != ".md" {
			t.Errorf("non-.md path leaked into OnReady: %q", p)
		}
	}
}

func TestWatcher_RemoveFile_FiresOnRemove(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the file BEFORE starting the watcher so the only event the
	// watcher sees during this test is the Remove. (If we created it after
	// startup, OnReady would also fire and we'd be testing a different thing.)
	path := filepath.Join(dir, "topic.md")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("pre-write: %v", err)
	}

	rec, cleanup := startWatcher(t, dir, 50*time.Millisecond)
	defer cleanup()

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, removed := waitFor(t, rec, 2*time.Second,
		func(_, removed []string) bool { return len(removed) >= 1 },
		"OnRemove to fire after deleting a .md file")

	if got, want := removed[0], path; got != want {
		t.Errorf("OnRemove path: got %q, want %q", got, want)
	}
}

// On macOS, Finder "Move to Trash" and `mv file.md somewhere/else.md` emit
// fsnotify.Rename — NOT fsnotify.Remove — for the source path. The watcher
// must treat that as a removal, otherwise UIs that rely on OnRemove (e.g.
// the schedule view) keep showing files that are gone from disk. This was
// observed in production: `rm` via Finder didn't drop the entry from the
// queue.
func TestWatcher_RenameOut_FiresOnRemove(t *testing.T) {
	srcDir := t.TempDir()
	// Destination dir for the rename — outside the watched tree so the
	// watcher only sees the rename of the source path (no Create on the
	// destination muddying the waters).
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "topic.md")
	dstPath := filepath.Join(dstDir, "topic.md")
	if err := os.WriteFile(srcPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("pre-write: %v", err)
	}

	rec, cleanup := startWatcher(t, srcDir, 50*time.Millisecond)
	defer cleanup()

	if err := os.Rename(srcPath, dstPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	_, removed := waitFor(t, rec, 2*time.Second,
		func(_, removed []string) bool { return len(removed) >= 1 },
		"OnRemove to fire after renaming the .md out of the watched dir")

	if got, want := removed[0], srcPath; got != want {
		t.Errorf("OnRemove path: got %q, want %q", got, want)
	}
}

// Atomic save (write tempfile, rename over the original) generates a Rename
// event on the destination on some platforms. The file STILL EXISTS at the
// path after the rename — so OnRemove must NOT fire; OnReady should fire
// instead (the new contents need reloading). This is the inverse of the
// rename-out case above.
func TestWatcher_RenameIntoSamePath_FiresOnReadyNotRemove(t *testing.T) {
	dir := t.TempDir()
	rec, cleanup := startWatcher(t, dir, 50*time.Millisecond)
	defer cleanup()

	target := filepath.Join(dir, "topic.md")
	tmp := filepath.Join(dir, "topic.md.tmp")
	if err := os.WriteFile(tmp, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	// Rename overwrites topic.md (which doesn't exist yet), placing new
	// contents at the watched path. fsnotify reports this as a Rename on
	// `topic.md.tmp` (gone) and a Create on `topic.md` (or potentially a
	// Rename on `topic.md` depending on backend). The .tmp file is filtered
	// by the .md extension check, so we only act on topic.md events.
	if err := os.Rename(tmp, target); err != nil {
		t.Fatalf("rename: %v", err)
	}

	ready, removed := waitFor(t, rec, 2*time.Second,
		func(ready, _ []string) bool { return len(ready) >= 1 },
		"OnReady to fire for the file that landed at the watched path")

	if ready[0] != target {
		t.Errorf("OnReady path: got %q, want %q", ready[0], target)
	}
	for _, p := range removed {
		if p == target {
			t.Errorf("OnRemove fired for %q even though the file still exists", p)
		}
	}
}

func TestWatcher_CreateThenRemove_CancelsPendingReady(t *testing.T) {
	dir := t.TempDir()
	// Use a long debounce so the Remove is guaranteed to land before the
	// pending OnReady timer fires, even when the test runs under `-race`
	// (where syscall + scheduler latency can stretch event delivery well
	// past short windows). 1s gives plenty of headroom while still
	// keeping the test cheap.
	const debounce = 1 * time.Second
	rec, cleanup := startWatcher(t, dir, debounce)
	defer cleanup()

	path := filepath.Join(dir, "ephemeral.md")
	if err := os.WriteFile(path, []byte("brief life"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Brief gap so fsnotify's CREATE/WRITE arrives + the debounce timer is
	// scheduled, BEFORE we issue the Remove that should cancel it. Without
	// this, on slow machines the Remove can race the Create's delivery and
	// land before there's anything to cancel.
	time.Sleep(50 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, removed := waitFor(t, rec, 3*time.Second,
		func(_, removed []string) bool { return len(removed) >= 1 },
		"OnRemove to fire for the deleted file")

	// Wait past the debounce window: if a stale OnReady was going to fire,
	// it would have fired by now.
	time.Sleep(debounce + 200*time.Millisecond)

	ready, _ := rec.snapshot()
	for _, p := range ready {
		if p == path {
			t.Errorf("OnReady fired for %q after it was deleted before debounce expired", p)
		}
	}
	if len(removed) == 0 || removed[0] != path {
		t.Errorf("OnRemove path: got %v, want first entry %q", removed, path)
	}
}

func TestWatcher_RapidWrites_DebounceCoalescesToSingleReady(t *testing.T) {
	dir := t.TempDir()
	rec, cleanup := startWatcher(t, dir, 50*time.Millisecond)
	defer cleanup()

	path := filepath.Join(dir, "topic.md")
	// Write the same file several times in rapid succession. With a 50ms
	// debounce, all writes should collapse into ONE OnReady.
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte("v"+string(rune('0'+i))), 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for OnReady, then a debounce-window's worth more to confirm no
	// straggler fires.
	waitFor(t, rec, 2*time.Second,
		func(ready, _ []string) bool { return len(ready) >= 1 },
		"OnReady to fire after rapid writes")
	time.Sleep(150 * time.Millisecond)

	ready, _ := rec.snapshot()
	count := 0
	for _, p := range ready {
		if p == path {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 coalesced OnReady for %q, got %d (all ready: %v)", path, count, ready)
	}
}

func TestWatcher_NilCallbacks_DoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Both callbacks nil: the watcher should still run without crashing
	// when events arrive. Guards against future regressions where the
	// dispatch path forgets to nil-check.
	w, err := New([]string{dir}, 20*time.Millisecond, log, Callbacks{})
	if err != nil {
		t.Fatalf("watcher.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)

	path := filepath.Join(dir, "topic.md")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	// Reaching here without a panic is the test.
}
