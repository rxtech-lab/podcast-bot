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
	ID               string
	Name             string
	Arguments        string // JSON string as emitted by the model
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// Message is the provider-neutral chat history entry.
type Message struct {
	Role       string
	Name       string // optional speaker tag, useful for multi-agent transcripts
	Content    string
	Parts      []InputPart // optional multimodal user content
	ToolCalls  []ToolCall
	ToolCallID string // set when Role == "tool"
}

// ToOpenAIParams converts a slice of Messages to openai-go's union params.
func ToOpenAIParams(history []Message) []openai.ChatCompletionMessageParamUnion {
	return toOpenAIParams(history, false)
}

// toOpenAIParams converts provider-neutral history to OpenAI-compatible
// messages. legacyGemini3 enables the documented validator bypass only for the
// first call in an assistant step whose Gemini thought signature was persisted
// before signature support existed. Parallel calls after the first correctly
// remain unsigned.
func toOpenAIParams(history []Message, legacyGemini3 bool) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			if len(m.Parts) > 0 {
				out = append(out, openai.UserMessage(openAIContentParts(m.Parts)))
			} else {
				out = append(out, openai.UserMessage(m.Content))
			}
		case RoleAssistant:
			asst := openai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" {
				asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(m.Content),
				}
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					call := openai.ChatCompletionMessageToolCallParam{
						ID: tc.ID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					}
					signature := tc.ThoughtSignature
					if signature == "" && legacyGemini3 && i == 0 {
						signature = geminiThoughtSignatureBypass
					}
					if signature != "" {
						call.SetExtraFields(map[string]any{
							"extra_content": map[string]any{
								"google": map[string]any{
									"thought_signature": signature,
								},
							},
						})
					}
					calls = append(calls, call)
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

func openAIContentParts(parts []InputPart) []openai.ChatCompletionContentPartUnionParam {
	out := make([]openai.ChatCompletionContentPartUnionParam, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			out = append(out, openai.TextContentPart(part.Text))
		}
		if part.ImageURL != "" {
			detail := part.Detail
			if detail == "" {
				detail = "auto"
			}
			out = append(out, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL:    part.ImageURL,
				Detail: detail,
			}))
		}
	}
	return out
}
