package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/openai/openai-go"
)

func TestGeminiThoughtSignatureFromStreamedToolCall(t *testing.T) {
	var call openai.ChatCompletionChunkChoiceDeltaToolCall
	err := json.Unmarshal([]byte(`{
		"index": 0,
		"id": "call_1",
		"type": "function",
		"function": {"name": "search_sources", "arguments": "{\"query\":\"gemini\"}"},
		"extra_content": {"google": {"thought_signature": "signed-step"}}
	}`), &call)
	if err != nil {
		t.Fatalf("unmarshal tool call: %v", err)
	}
	if got := geminiThoughtSignature(call); got != "signed-step" {
		t.Fatalf("thought signature = %q, want signed-step", got)
	}
}

func TestAssembleToolCallsPreservesThoughtSignature(t *testing.T) {
	calls := AssembleToolCalls([]DeltaToolCall{
		{
			Index:            0,
			ID:               "call_1",
			Name:             "search_sources",
			Arguments:        `{"query":`,
			ThoughtSignature: "signed-step",
		},
		{Index: 0, Arguments: `"gemini"}`},
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].ThoughtSignature != "signed-step" {
		t.Fatalf("thought signature = %q, want signed-step", calls[0].ThoughtSignature)
	}
	if calls[0].Arguments != `{"query":"gemini"}` {
		t.Fatalf("arguments = %q", calls[0].Arguments)
	}
}

func TestToOpenAIParamsPreservesGeminiThoughtSignature(t *testing.T) {
	history := []Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{
			{
				ID:               "call_1",
				Name:             "search_sources",
				Arguments:        `{"query":"gemini"}`,
				ThoughtSignature: "signed-step",
			},
			{
				ID:        "call_2",
				Name:      "search_sources",
				Arguments: `{"query":"gateway"}`,
			},
		},
	}}

	got := marshaledToolCallSignatures(t, toOpenAIParams(history, false))
	if len(got) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(got))
	}
	if got[0] != "signed-step" {
		t.Fatalf("first thought signature = %q, want signed-step", got[0])
	}
	if got[1] != "" {
		t.Fatalf("parallel call thought signature = %q, want empty", got[1])
	}
}

func TestToOpenAIParamsRecoversLegacyGemini3ToolCall(t *testing.T) {
	history := []Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "search_sources", Arguments: `{"query":"gemini"}`},
			{ID: "call_2", Name: "search_sources", Arguments: `{"query":"gateway"}`},
		},
	}}

	got := marshaledToolCallSignatures(t, toOpenAIParams(history, true))
	if got[0] != geminiThoughtSignatureBypass {
		t.Fatalf("legacy thought signature = %q, want bypass", got[0])
	}
	if got[1] != "" {
		t.Fatalf("parallel legacy call thought signature = %q, want empty", got[1])
	}

	withoutRecovery := marshaledToolCallSignatures(t, toOpenAIParams(history, false))
	if withoutRecovery[0] != "" {
		t.Fatalf("non-Gemini history received bypass %q", withoutRecovery[0])
	}
}

func TestGemini3ModelDetection(t *testing.T) {
	for _, model := range []string{"google/gemini-3.6-flash", "gemini-3-pro", " GOOGLE/GEMINI-3.1-FLASH "} {
		if !isGemini3Model(model) {
			t.Fatalf("isGemini3Model(%q) = false", model)
		}
	}
	for _, model := range []string{"google/gemini-2.5-flash", "openai/gpt-5.4"} {
		if isGemini3Model(model) {
			t.Fatalf("isGemini3Model(%q) = true", model)
		}
	}
}

func TestIsBadRequest(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	apiErr := &openai.Error{
		StatusCode: http.StatusBadRequest,
		Request:    request,
		Response:   &http.Response{StatusCode: http.StatusBadRequest},
	}
	if !IsBadRequest(fmt.Errorf("planning conversation: %w", apiErr)) {
		t.Fatal("wrapped HTTP 400 was not classified as a bad request")
	}
	apiErr.StatusCode = http.StatusTooManyRequests
	if IsBadRequest(apiErr) {
		t.Fatal("HTTP 429 must remain retryable")
	}
}

func marshaledToolCallSignatures(t *testing.T, params []openai.ChatCompletionMessageParamUnion) []string {
	t.Helper()
	if len(params) != 1 {
		t.Fatalf("messages = %d, want 1", len(params))
	}
	raw, err := json.Marshal(params[0])
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var message struct {
		ToolCalls []struct {
			ExtraContent struct {
				Google struct {
					ThoughtSignature string `json:"thought_signature"`
				} `json:"google"`
			} `json:"extra_content"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("unmarshal message %s: %v", raw, err)
	}
	out := make([]string, len(message.ToolCalls))
	for i := range message.ToolCalls {
		out[i] = message.ToolCalls[i].ExtraContent.Google.ThoughtSignature
	}
	return out
}
