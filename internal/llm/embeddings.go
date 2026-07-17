package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

// embedBatchSize caps how many inputs are sent per embeddings request so a
// large re-index never builds a single oversized payload.
const embedBatchSize = 64

// Embed vectorizes inputs with the client's model, returning one vector per
// input in order. dimensions > 0 is passed through to the API (Matryoshka
// truncation on models that support it) and enforced on the response: a
// provider that ignores the parameter and returns a different length is a
// hard error, because stored vectors must all share the pgvector column
// dimension.
func (c *Client) Embed(ctx context.Context, inputs []string, dimensions int) ([][]float32, Usage, error) {
	if c == nil {
		return nil, Usage{}, fmt.Errorf("embed: nil client")
	}
	if len(inputs) == 0 {
		return nil, Usage{}, nil
	}
	out := make([][]float32, 0, len(inputs))
	var total Usage
	total.Model = c.model
	for start := 0; start < len(inputs); start += embedBatchSize {
		end := min(start+embedBatchSize, len(inputs))
		batch := inputs[start:end]
		vectors, usage, err := c.embedBatch(ctx, batch, dimensions)
		if err != nil {
			return nil, total, err
		}
		out = append(out, vectors...)
		total.PromptTokens += usage.PromptTokens
		total.TotalTokens += usage.TotalTokens
	}
	c.recordUsage(total)
	return out, total, nil
}

func (c *Client) embedBatch(ctx context.Context, inputs []string, dimensions int) ([][]float32, Usage, error) {
	params := openai.EmbeddingNewParams{
		Model: c.model,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs},
	}
	if dimensions > 0 {
		params.Dimensions = openai.Int(int64(dimensions))
	}
	resp, err := c.c.Embeddings.New(ctx, params)
	if err != nil && dimensions > 0 && isDimensionsRejection(err) {
		// Some OpenAI-compatible providers reject the dimensions parameter
		// outright; retry without it and rely on the length check below.
		params = openai.EmbeddingNewParams{
			Model: c.model,
			Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: inputs},
		}
		resp, err = c.c.Embeddings.New(ctx, params)
	}
	if err != nil {
		return nil, Usage{}, err
	}
	if len(resp.Data) != len(inputs) {
		return nil, Usage{}, fmt.Errorf("embed: got %d vectors for %d inputs", len(resp.Data), len(inputs))
	}
	vectors := make([][]float32, len(inputs))
	for _, d := range resp.Data {
		if d.Index < 0 || int(d.Index) >= len(vectors) {
			return nil, Usage{}, fmt.Errorf("embed: vector index %d out of range", d.Index)
		}
		if dimensions > 0 && len(d.Embedding) != dimensions {
			return nil, Usage{}, fmt.Errorf("embed: model %q returned %d dimensions, want %d (model likely does not support the dimensions parameter)",
				c.model, len(d.Embedding), dimensions)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		vectors[int(d.Index)] = vec
	}
	usage := Usage{
		Model:        c.model,
		PromptTokens: resp.Usage.PromptTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}
	return vectors, usage, nil
}

// isDimensionsRejection reports whether err is a 400 keyed on the dimensions
// parameter, mirroring isResponseFormatRejection's message-based detection.
func isDimensionsRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "dimensions") &&
		(strings.Contains(msg, "Invalid input") ||
			strings.Contains(msg, "invalid_request_error") ||
			strings.Contains(msg, "unsupported") ||
			strings.Contains(msg, "does not support"))
}
