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
