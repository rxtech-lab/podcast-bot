package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/planner"
)

// TestFakeLLMImageProbe drives the fake through the real llm client and the
// real planner message builder, so it verifies the exact wire shape the
// conversational planner produces: when the user turn carries an image part
// the fake acknowledges it, and when the image was dropped the fake returns
// the loud error the UI test would surface.
func TestFakeLLMImageProbe(t *testing.T) {
	base, stop, err := StartFakeLLM()
	if err != nil {
		t.Fatalf("StartFakeLLM: %v", err)
	}
	defer stop()

	client := llm.New(base, "e2e", "e2e-fake-model")
	tools := []openai.ChatCompletionToolParam{{
		Function: shared.FunctionDefinitionParam{
			Name:       "write_plan",
			Parameters: map[string]any{"type": "object"},
		},
	}}
	prompt := "Design a panel discussion about the following topic.\n\nTopic: Tell a story from this image"

	reply := func(atts []planner.Attachment) string {
		t.Helper()
		msg := planner.UserTurnMessage(prompt, atts)
		stream, err := client.Stream(context.Background(), "system", []llm.Message{msg}, tools)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var sb strings.Builder
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			sb.WriteString(d.TextChunk)
		}
		return sb.String()
	}

	withImage := reply([]planner.Attachment{{
		Filename: "photo.png",
		MIMEType: "image/png",
		URL:      "data:image/png;base64,iVBORw0KGgo=",
	}})
	if !strings.Contains(withImage, ImageProbeSuccess) {
		t.Fatalf("with image = %q, want the success marker %q", withImage, ImageProbeSuccess)
	}

	withoutImage := reply(nil)
	if !strings.Contains(withoutImage, ImageProbeFailure) {
		t.Fatalf("without image = %q, want the failure marker %q", withoutImage, ImageProbeFailure)
	}
}

func TestFakeLLMQAIntentUsesLatestUserTurn(t *testing.T) {
	base, stop, err := StartFakeLLM()
	if err != nil {
		t.Fatalf("StartFakeLLM: %v", err)
	}
	defer stop()

	client := llm.New(base, "e2e", "e2e-fake-model")
	tool := func(name string) openai.ChatCompletionToolParam {
		return openai.ChatCompletionToolParam{Function: shared.FunctionDefinitionParam{
			Name:       name,
			Parameters: map[string]any{"type": "object"},
		}}
	}
	tools := []openai.ChatCompletionToolParam{
		tool("search_content"),
		tool("search_podcasts"),
		tool("display_podcasts"),
		tool("show_highlight_lines"),
	}
	// This mirrors the important words in the real global QA system prompt.
	// They must not override the intent of the latest user turn.
	system := "Use display_podcasts for podcast lists and show_highlight_lines for highlights and quotes."

	firstTool := func(prompt string) string {
		t.Helper()
		stream, err := client.Stream(context.Background(), system, []llm.Message{{
			Role: llm.RoleUser, Content: prompt,
		}}, tools)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for delta := range stream.Deltas() {
			if delta.ToolCall != nil && delta.ToolCall.Name != "" {
				return delta.ToolCall.Name
			}
		}
		t.Fatalf("no tool call for %q (stream error: %v)", prompt, stream.Err())
		return ""
	}

	if got := firstTool("Show me the podcast about testing"); got != "search_podcasts" {
		t.Fatalf("list prompt first tool = %q, want search_podcasts", got)
	}
	if got := firstTool("Show highlights and quotes from my testing podcasts"); got != "search_content" {
		t.Fatalf("highlight prompt first tool = %q, want search_content", got)
	}
}
