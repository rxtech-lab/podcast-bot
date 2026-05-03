package llm

import (
	"encoding/json"

	"github.com/openai/openai-go"
)

// Role values for Message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolCall is a provider-neutral function call request from the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string as emitted by the model
}

// Message is the provider-neutral chat history entry.
type Message struct {
	Role       string
	Name       string // optional speaker tag, useful for multi-agent transcripts
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // set when Role == "tool"
}

// ToOpenAIParams converts a slice of Messages to openai-go's union params.
func ToOpenAIParams(history []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case RoleAssistant:
			asst := openai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" {
				asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(m.Content),
				}
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					})
				}
				asst.ToolCalls = calls
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

// MarshalArgs is a convenience for tools that want a JSON string from a map.
func MarshalArgs(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
