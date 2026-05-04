package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// ToolDispatcher resolves a single tool call into its result string. The
// caller (typically an agent's Base) wires this through to the tools.Registry
// so that StreamWithTools can stay agnostic of the registry/agent context.
type ToolDispatcher func(ctx context.Context, name, jsonArgs string) (string, error)

// maxToolRounds caps the assistant↔tool ping-pong inside StreamWithTools so a
// model that keeps re-calling tools can never loop forever.
const maxToolRounds = 8

// Client wraps an OpenAI-compatible chat completions endpoint with a fixed model.
// One Client per agent so per-agent BaseURL/API-key overrides are simple.
type Client struct {
	c     openai.Client
	model string
}

// New constructs a Client. baseURL must include scheme + path (e.g.
// https://api.openai.com/v1).
func New(baseURL, apiKey, model string) *Client {
	c := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	)
	return &Client{c: c, model: model}
}

// Model returns the configured model name.
func (c *Client) Model() string { return c.model }

// Stream starts a streaming chat completion. The returned Stream emits Deltas
// until the channel closes; callers should drain it.
func (c *Client) Stream(
	ctx context.Context,
	system string,
	history []Message,
	tools []openai.ChatCompletionToolParam,
) (*Stream, error) {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+1)
	if system != "" {
		msgs = append(msgs, openai.SystemMessage(system))
	}
	msgs = append(msgs, ToOpenAIParams(history)...)

	params := openai.ChatCompletionNewParams{
		Model:    c.model,
		Messages: msgs,
	}
	if len(tools) > 0 {
		params.Tools = tools
	}

	streamCtx, cancel := context.WithCancel(ctx)
	raw := c.c.Chat.Completions.NewStreaming(streamCtx, params)

	out := &Stream{
		deltas: make(chan Delta, 16),
		errCh:  make(chan error, 1),
		stop:   cancel,
	}
	go func() {
		defer close(out.deltas)
		defer raw.Close()
		for raw.Next() {
			chunk := raw.Current()
			if len(chunk.Choices) == 0 {
				continue
			}
			d := chunk.Choices[0].Delta
			if d.Content != "" {
				select {
				case out.deltas <- Delta{TextChunk: d.Content}:
				case <-streamCtx.Done():
					return
				}
			}
			for _, tc := range d.ToolCalls {
				select {
				case out.deltas <- Delta{ToolCall: &DeltaToolCall{
					Index:     int(tc.Index),
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}}:
				case <-streamCtx.Done():
					return
				}
			}
		}
		if err := raw.Err(); err != nil {
			select {
			case out.errCh <- err:
			default:
			}
		}
		select {
		case out.deltas <- Delta{Done: true}:
		case <-streamCtx.Done():
		}
	}()
	return out, nil
}

// StreamWithTools runs a multi-round streaming conversation that handles tool
// calls transparently: when the model emits tool_calls, this method dispatches
// them, appends the assistant + tool messages to history, and re-streams until
// the model produces a tool-call-free assistant turn. Only TEXT deltas are
// forwarded to the returned Stream — tool-call deltas are consumed internally
// so downstream consumers (e.g. the TTS pipeline) don't have to know tools
// exist. tools may be empty/nil; in that case this just delegates to Stream.
func (c *Client) StreamWithTools(
	ctx context.Context,
	system string,
	history []Message,
	tools []openai.ChatCompletionToolParam,
	dispatch ToolDispatcher,
) (*Stream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	out := &Stream{
		deltas: make(chan Delta, 16),
		errCh:  make(chan error, 1),
		stop:   cancel,
	}

	go func() {
		defer close(out.deltas)
		msgs := append([]Message(nil), history...)

		for round := 0; round < maxToolRounds; round++ {
			inner, err := c.Stream(streamCtx, system, msgs, tools)
			if err != nil {
				select {
				case out.errCh <- err:
				default:
				}
				return
			}

			var assistantText strings.Builder
			var tcDeltas []DeltaToolCall
			for d := range inner.Deltas() {
				if d.Done {
					break
				}
				if d.TextChunk != "" {
					assistantText.WriteString(d.TextChunk)
					select {
					case out.deltas <- Delta{TextChunk: d.TextChunk}:
					case <-streamCtx.Done():
						return
					}
				}
				if d.ToolCall != nil {
					tcDeltas = append(tcDeltas, *d.ToolCall)
				}
			}
			if err := inner.Err(); err != nil {
				select {
				case out.errCh <- err:
				default:
				}
				return
			}

			// Tool-call-free round → we're done; surface the terminal Done.
			if len(tcDeltas) == 0 {
				select {
				case out.deltas <- Delta{Done: true}:
				case <-streamCtx.Done():
				}
				return
			}

			calls := AssembleToolCalls(tcDeltas)
			msgs = append(msgs, Message{
				Role:      RoleAssistant,
				Content:   assistantText.String(),
				ToolCalls: calls,
			})
			for _, tc := range calls {
				var result string
				if dispatch == nil {
					result = "tool error: no dispatcher configured"
				} else if r, derr := dispatch(streamCtx, tc.Name, tc.Arguments); derr != nil {
					result = "tool error: " + derr.Error()
				} else {
					result = r
				}
				msgs = append(msgs, Message{
					Role:       RoleTool,
					Content:    result,
					ToolCallID: tc.ID,
				})
			}
		}

		select {
		case out.errCh <- fmt.Errorf("tool-call loop exceeded %d rounds", maxToolRounds):
		default:
		}
	}()

	return out, nil
}

// JSON makes a non-streaming chat completion that asks the model to return
// JSON. The raw JSON bytes are returned. Used for short structured calls
// like viewer.WantsToAsk.
func (c *Client) JSON(ctx context.Context, system, user string) ([]byte, error) {
	resp, err := c.c.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(system),
			openai.UserMessage(user),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty completion")
	}
	return []byte(resp.Choices[0].Message.Content), nil
}

// AssembleToolCalls turns a sequence of streamed tool-call deltas into
// finalised ToolCall values keyed by index.
func AssembleToolCalls(deltas []DeltaToolCall) []ToolCall {
	type acc struct {
		id, name string
		args     []byte
	}
	byIdx := map[int]*acc{}
	for _, d := range deltas {
		a := byIdx[d.Index]
		if a == nil {
			a = &acc{}
			byIdx[d.Index] = a
		}
		if d.ID != "" {
			a.id = d.ID
		}
		if d.Name != "" {
			a.name = d.Name
		}
		if d.Arguments != "" {
			a.args = append(a.args, d.Arguments...)
		}
	}
	out := make([]ToolCall, 0, len(byIdx))
	for i := 0; i < len(byIdx); i++ {
		a := byIdx[i]
		if a == nil {
			continue
		}
		// Validate args is JSON; if not, wrap as raw string.
		if !json.Valid(a.args) {
			a.args = []byte(fmt.Sprintf("%q", string(a.args)))
		}
		out = append(out, ToolCall{ID: a.id, Name: a.name, Arguments: string(a.args)})
	}
	return out
}
