---
slug: code/internal/llm
title: Package internal/llm
description: Auto-generated go doc reference for the internal/llm package.
---

# Package `internal/llm`

_Generated with `go doc -all ./internal/llm`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package llm // import "github.com/sirily11/debate-bot/internal/llm"


CONSTANTS

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)
    Role values for Message.


FUNCTIONS

func MarshalArgs(v any) string
    MarshalArgs is a convenience for tools that want a JSON string from a map.

func ToOpenAIParams(history []Message) []openai.ChatCompletionMessageParamUnion
    ToOpenAIParams converts a slice of Messages to openai-go's union params.


TYPES

type Client struct {
	// Has unexported fields.
}
    Client wraps an OpenAI-compatible chat completions endpoint with a fixed
    model. One Client per agent so per-agent BaseURL/API-key overrides are
    simple.

func New(baseURL, apiKey, model string) *Client
    New constructs a Client. baseURL must include scheme + path (e.g.
    https://api.openai.com/v1).

func (c *Client) JSON(ctx context.Context, system, user string) ([]byte, error)
    JSON makes a non-streaming chat completion that asks the model to return
    JSON. The raw JSON bytes are returned. Used for short structured calls like
    viewer.WantsToAsk.

    Some gateway-routed models (notably google/gemini-3-flash via the Vercel
    AI Gateway) reject the OpenAI-compatible response_format=json parameter
    with a 400 invalid_request_error. When that specific error surfaces,
    we transparently retry without the response_format hint — the system prompt
    always asks for "strict JSON" so the model usually complies on the second
    pass, and JSON-extraction in the caller (json.Unmarshal) is the source of
    truth for shape correctness.

func (c *Client) Model() string
    Model returns the configured model name.

func (c *Client) Stream(
	ctx context.Context,
	system string,
	history []Message,
	tools []openai.ChatCompletionToolParam,
) (*Stream, error)
    Stream starts a streaming chat completion. The returned Stream emits Deltas
    until the channel closes; callers should drain it.

func (c *Client) StreamWithTools(
	ctx context.Context,
	system string,
	history []Message,
	tools []openai.ChatCompletionToolParam,
	dispatch ToolDispatcher,
) (*Stream, error)
    StreamWithTools runs a multi-round streaming conversation that handles tool
    calls transparently: when the model emits tool_calls, this method dispatches
    them, appends the assistant + tool messages to history, and re-streams until
    the model produces a tool-call-free assistant turn. Only TEXT deltas are
    forwarded to the returned Stream — tool-call deltas are consumed internally
    so downstream consumers (e.g. the TTS pipeline) don't have to know tools
    exist. tools may be empty/nil; in that case this just delegates to Stream.

type Delta struct {
	TextChunk string
	ToolCall  *DeltaToolCall
	Done      bool
}
    Delta is one streamed event from the LLM: a text chunk, a (partial) tool
    call, or the terminal Done marker.

type DeltaToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}
    DeltaToolCall holds one chunk of a streamed tool call. Streaming providers
    emit ID + name in the first chunk and append arguments incrementally;
    the receiver must accumulate by Index.

type Message struct {
	Role       string
	Name       string // optional speaker tag, useful for multi-agent transcripts
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // set when Role == "tool"
}
    Message is the provider-neutral chat history entry.

type Stream struct {
	// Has unexported fields.
}
    Stream wraps the underlying SSE stream and exposes a channel of Deltas plus
    a terminal error.

func (s *Stream) Close()
    Close stops the underlying stream early.

func (s *Stream) Deltas() <-chan Delta
    Deltas returns the read end of the delta channel. Closed when the stream
    finishes.

func (s *Stream) Err() error
    Err returns the terminal error after Deltas() is fully drained, or nil.

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string as emitted by the model
}
    ToolCall is a provider-neutral function call request from the model.

func AssembleToolCalls(deltas []DeltaToolCall) []ToolCall
    AssembleToolCalls turns a sequence of streamed tool-call deltas into
    finalised ToolCall values keyed by index.

type ToolDispatcher func(ctx context.Context, name, jsonArgs string) (string, error)
    ToolDispatcher resolves a single tool call into its result string. The
    caller (typically an agent's Base) wires this through to the tools.Registry
    so that StreamWithTools can stay agnostic of the registry/agent context.
```
