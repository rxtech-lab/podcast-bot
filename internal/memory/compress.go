package memory

import (
	"context"
	"fmt"

	"github.com/sirily11/debate-bot/internal/llm"
)

// DefaultThreshold is the rough memory-token cap before compression triggers.
const DefaultThreshold = 4000

// Compressor summarises an agent's memory using a separate compression LLM.
type Compressor struct {
	LLM       *llm.Client
	Threshold int // 0 → DefaultThreshold
}

// New creates a Compressor.
func New(client *llm.Client, threshold int) *Compressor {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	return &Compressor{LLM: client, Threshold: threshold}
}

// MaybeCompress reads m's content, and if it exceeds the threshold, summarises
// it via the compression LLM and replaces the file.
func (c *Compressor) MaybeCompress(ctx context.Context, m *Memory) error {
	cur, err := m.Read()
	if err != nil {
		return err
	}
	if Estimate(cur) < c.Threshold {
		return nil
	}
	system := "You are compressing a debate participant's running notes. " +
		"Output a concise markdown digest preserving: speaker names, claims they made, contradictions, " +
		"open attacks the agent should answer, and defenses they have prepared. " +
		"Drop greetings and filler. Keep it under 1500 tokens."
	user := fmt.Sprintf("# Existing notes\n\n%s\n\n# Output the compressed notes only.", cur)
	out, err := c.LLM.JSON(ctx, system, user)
	if err != nil {
		// Fall back to plain text streaming completion: re-issue without JSON mode.
		// We don't actually want JSON for compression — call Stream and accumulate.
		return c.streamingCompress(ctx, m, cur)
	}
	return m.Replace(string(out))
}

// streamingCompress is the fallback path that streams a plain-text summary.
func (c *Compressor) streamingCompress(ctx context.Context, m *Memory, cur string) error {
	system := "You are compressing a debate participant's running notes. Output a concise markdown digest."
	hist := []llm.Message{{Role: llm.RoleUser, Content: cur}}
	stream, err := c.LLM.Stream(ctx, system, hist, nil)
	if err != nil {
		return err
	}
	var b []byte
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		if d.TextChunk != "" {
			b = append(b, d.TextChunk...)
		}
	}
	if e := stream.Err(); e != nil {
		return e
	}
	return m.Replace(string(b))
}
