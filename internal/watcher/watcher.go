// Package watcher watches one or more directories for new or modified
// markdown files and emits each path via a callback.
//
// fsnotify on macOS reports file creation as a series of events (CREATE,
// possibly several WRITE, sometimes RENAME for atomic-save editors that write
// to a tempfile then rename). We debounce per-path so the callback only fires
// once per "settled" file, after an editor has finished flushing.
package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Callbacks bundles the per-event hooks the watcher invokes. Both run on the
// watcher's goroutine and should not block.
//
//   - OnReady fires once a markdown file has settled (no further writes for
//     the debounce window) — used for create/write/rename arrivals.
//   - OnRemove fires immediately when a markdown file disappears
//     (fsnotify.Remove). No debounce: a delete is terminal — there's nothing
//     left to settle.
type Callbacks struct {
	OnReady  func(path string)
	OnRemove func(path string)
}

// Watcher watches directories for added / removed .md files.
type Watcher struct {
	fs       *fsnotify.Watcher
	log      *slog.Logger
	cb       Callbacks
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]*time.Timer
}

// New creates a Watcher. dirs are watched non-recursively; pass each
// subdirectory you want covered. debounce is how long to wait after the last
// write event before considering the file ready (200ms is a good default).
func New(dirs []string, debounce time.Duration, log *slog.Logger, cb Callbacks) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fs:       fw,
		log:      log,
		cb:       cb,
		debounce: debounce,
		pending:  map[string]*time.Timer{},
	}
	for _, d := range dirs {
		if err := fw.Add(d); err != nil {
			_ = fw.Close()
			return nil, err
		}
		log.Info("watching directory for new debates", "dir", d)
	}
	return w, nil
}

// Run blocks reading events until ctx is cancelled or the underlying watcher
// errors out. Returns ctx.Err() on cancellation.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.fs.Events:
			if !ok {
				return nil
			}
			w.handleEvent(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("fsnotify error", "err", err)
		}
	}
}

func (w *Watcher) handleEvent(ev fsnotify.Event) {
	if !strings.EqualFold(filepath.Ext(ev.Name), ".md") {
		return
	}
	// Remove is unambiguous — fire OnRemove immediately.
	if ev.Op&fsnotify.Remove != 0 {
		w.fireRemove(ev.Name)
		return
	}
	// Rename is ambiguous on macOS / Linux: it fires when a file moves AWAY
	// from this path (Finder "move to trash", `mv` to a different dir →
	// behaves like a delete) AND when an atomic-save editor renames a
	// tempfile INTO this path (vim writebackup → behaves like a modify).
	// Distinguish by stat: if nothing exists at the path, the rename moved
	// the file out → treat as remove. Otherwise the rename brought new
	// content in → fall through to the debounced reload.
	if ev.Op&fsnotify.Rename != 0 {
		if _, err := os.Stat(ev.Name); err != nil {
			w.fireRemove(ev.Name)
			return
		}
	}
	if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
		return
	}
	w.schedule(ev.Name)
}

// fireRemove cancels any pending debounced reload for path (no point loading
// a file that's gone) and dispatches OnRemove. Shared by the Remove and
// rename-out branches of handleEvent.
func (w *Watcher) fireRemove(path string) {
	w.mu.Lock()
	if t, ok := w.pending[path]; ok {
		t.Stop()
		delete(w.pending, path)
	}
	w.mu.Unlock()
	if w.cb.OnRemove != nil {
		w.cb.OnRemove(path)
	}
}

// schedule (re)arms the debounce timer for path. Whichever event arrives last
// determines when the callback fires.
func (w *Watcher) schedule(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.pending[path]; ok {
		t.Stop()
	}
	w.pending[path] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, path)
		w.mu.Unlock()
		if w.cb.OnReady != nil {
			w.cb.OnReady(path)
		}
	})
}

func (w *Watcher) close() {
	w.mu.Lock()
	for _, t := range w.pending {
		t.Stop()
	}
	w.pending = nil
	w.mu.Unlock()
	_ = w.fs.Close()
}
