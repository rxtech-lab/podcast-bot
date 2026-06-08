---
slug: code/internal/memory
title: Package internal/memory
description: Auto-generated go doc reference for the internal/memory package.
---

# Package `internal/memory`

_Generated with `go doc -all ./internal/memory`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package memory // import "github.com/sirily11/debate-bot/internal/memory"


CONSTANTS

const DefaultThreshold = 4000
    DefaultThreshold is the rough memory-token cap before compression triggers.


FUNCTIONS

func Estimate(s string) int
    Estimate returns an approximate token count for s. Falls back to byte/4 if
    the tokenizer cannot be loaded.


TYPES

type Compressor struct {
	LLM       *llm.Client
	Threshold int // 0 → DefaultThreshold
}
    Compressor summarises an agent's memory using a separate compression LLM.

func New(client *llm.Client, threshold int) *Compressor
    New creates a Compressor.

func (c *Compressor) MaybeCompress(ctx context.Context, m *Memory) error
    MaybeCompress reads m's content, and if it exceeds the threshold, summarises
    it via the compression LLM and replaces the file.

type Memory struct {
	Path string

	// Has unexported fields.
}
    Memory wraps a single agent's memory.md file.

func (m *Memory) Append(line string) error
    Append writes one line to the memory file (newline appended).

func (m *Memory) Read() (string, error)
    Read returns the entire current memory content (empty string if file
    absent).

func (m *Memory) Replace(content string) error
    Replace atomically rewrites the memory file with new content.

type Store struct {
	// Has unexported fields.
}
    Store creates per-agent memory files in a single directory.

func NewStore(dir string) (*Store, error)
    NewStore initialises (and creates) the memory directory.

func (s *Store) For(agent string) *Memory
    For returns the Memory for the named agent, creating it if needed. File path
    is <dir>/<safe>_memory.md.
```
