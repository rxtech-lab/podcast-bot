---
slug: code/internal/util
title: Package internal/util
description: Auto-generated go doc reference for the internal/util package.
---

# Package `internal/util`

_Generated with `go doc -all ./internal/util`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package util // import "github.com/sirily11/debate-bot/internal/util"


FUNCTIONS

func NewFileLogger(dir string) (*slog.Logger, io.Closer, error)
    NewFileLogger returns a slog.Logger writing JSON lines to <dir>/run.log.
    Errors creating the file fall back to stderr so the TUI never sees stray
    output.

func Safe(name string) string
    Safe lowercases name, ASCII-folds, and replaces non-alnum runes with '_'.
    Used for memory file prefixes and audio paths.
```
