---
slug: code/internal/watcher
title: Package internal/watcher
description: Auto-generated go doc reference for the internal/watcher package.
---

# Package `internal/watcher`

_Generated with `go doc -all ./internal/watcher`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package watcher // import "github.com/sirily11/debate-bot/internal/watcher"

Package watcher watches one or more directories for new or modified markdown
files and emits each path via a callback.

fsnotify on macOS reports file creation as a series of events (CREATE, possibly
several WRITE, sometimes RENAME for atomic-save editors that write to a tempfile
then rename). We debounce per-path so the callback only fires once per "settled"
file, after an editor has finished flushing.

TYPES

type Callbacks struct {
	OnReady  func(path string)
	OnRemove func(path string)
}
    Callbacks bundles the per-event hooks the watcher invokes. Both run on the
    watcher's goroutine and should not block.

      - OnReady fires once a markdown file has settled (no further writes for
        the debounce window) — used for create/write/rename arrivals.
      - OnRemove fires immediately when a markdown file disappears
        (fsnotify.Remove). No debounce: a delete is terminal — there's nothing
        left to settle.

type Watcher struct {
	// Has unexported fields.
}
    Watcher watches directories for added / removed .md files.

func New(dirs []string, debounce time.Duration, log *slog.Logger, cb Callbacks) (*Watcher, error)
    New creates a Watcher. dirs are watched non-recursively; pass each
    subdirectory you want covered. debounce is how long to wait after the last
    write event before considering the file ready (200ms is a good default).

func (w *Watcher) Run(ctx context.Context) error
    Run blocks reading events until ctx is cancelled or the underlying watcher
    errors out. Returns ctx.Err() on cancellation.
```
