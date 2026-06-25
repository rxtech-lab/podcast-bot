package planner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

func testConversationSession() *conversationSession {
	p := &Planner{env: &config.Env{HostModel: "test-model"}}
	return &conversationSession{
		planner: p,
		opts:    ConversationOptions{Language: "en-US"},
	}
}

func TestConversationDispatchAskQuestionPauses(t *testing.T) {
	s := testConversationSession()
	args := `{"questions":[{"title":"How long?","type":"single_choice","options":[{"title":"Short"},{"title":"Long"}]}]}`
	output, kind, res, questionsJSON, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c1", Name: "ask_question", Arguments: args})
	if kind != dispatchQuestion {
		t.Fatalf("expected dispatchQuestion, got %v", kind)
	}
	if isErr {
		t.Fatalf("ask_question should not error: %q", output)
	}
	if res != nil {
		t.Fatalf("ask_question should not produce a plan")
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(questionsJSON), &items); err != nil || len(items) != 1 {
		t.Fatalf("questionsJSON should be the raw questions array, got %q (%v)", questionsJSON, err)
	}
}

func TestConversationDispatchWritePlanHiddenUntilShowPlan(t *testing.T) {
	s := testConversationSession()
	draft := `{"title":"AI in Education","background":"A neutral framing paragraph one. And paragraph two.","host":{"name":"Dr. Host"},"discussants":[{"name":"Alice","aspect":"economic"},{"name":"Bob","aspect":"ethical"}]}`
	output, kind, res, _, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c2", Name: "write_plan", Arguments: draft})
	if isErr {
		t.Fatalf("write_plan errored: %q", output)
	}
	if kind != dispatchTool {
		t.Fatalf("expected hidden dispatchTool, got %v", kind)
	}
	if res == nil || res.Script == nil {
		t.Fatalf("write_plan should produce an assembled plan")
	}
	if res.Script.Title != "AI in Education" {
		t.Fatalf("unexpected title: %q", res.Script.Title)
	}
	if len(res.Script.Discussants) != 2 {
		t.Fatalf("expected 2 discussants, got %d", len(res.Script.Discussants))
	}
	output, kind, shown, _, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c3", Name: "show_plan", Arguments: `{}`})
	if isErr {
		t.Fatalf("show_plan errored: %q", output)
	}
	if kind != dispatchPlan {
		t.Fatalf("show_plan kind = %v, want dispatchPlan", kind)
	}
	if !strings.Contains(output, "Do not summarize") {
		t.Fatalf("show_plan output should discourage plan summaries, got %q", output)
	}
	if !strings.Contains(output, "no JSON") || !strings.Contains(output, "no bilingual translation map") {
		t.Fatalf("show_plan output should require a plain-text acknowledgement, got %q", output)
	}
	if shown == nil || shown.Script == nil || shown.Script.Title != "AI in Education" {
		t.Fatalf("show_plan did not return the saved plan: %+v", shown)
	}
}

func TestConversationDispatchUpdatePlanReassembles(t *testing.T) {
	s := testConversationSession()
	draft := `{"title":"Revised","background":"Para one here. Para two here.","host":{"name":"Mod"},"discussants":[{"name":"X","aspect":"tech"},{"name":"Y","aspect":"policy"}]}`
	_, kind, res, _, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c3", Name: "update_plan", Arguments: draft})
	if isErr || kind != dispatchTool || res == nil {
		t.Fatalf("update_plan should reassemble a plan (kind=%v err=%v)", kind, isErr)
	}
	if res.Script.Title != "Revised" {
		t.Fatalf("update_plan title not applied: %q", res.Script.Title)
	}
}

func TestConversationDispatchBadDraftErrors(t *testing.T) {
	s := testConversationSession()
	// Only one discussant — decodeDraft requires at least two.
	draft := `{"title":"Bad","background":"x","host":{"name":"h"},"discussants":[{"name":"Solo","aspect":"only"}]}`
	output, kind, res, _, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c4", Name: "write_plan", Arguments: draft})
	if !isErr {
		t.Fatalf("expected an error for an incomplete draft, got output=%q kind=%v res=%v", output, kind, res)
	}
}

func TestQuestionsArgRejectsEmpty(t *testing.T) {
	if _, err := questionsArg(`{"questions":[]}`); err == nil {
		t.Fatalf("expected error for empty questions array")
	}
	if _, err := questionsArg(`{}`); err == nil {
		t.Fatalf("expected error for missing questions")
	}
}

func TestConversationMessageTextIncludesCurrentLanguage(t *testing.T) {
	text := ConversationMessageText("Make it more technical", nil, "zh-Hant-HK")
	if !strings.Contains(text, "Make it more technical") {
		t.Fatalf("message text missing visible prompt: %q", text)
	}
	if !strings.Contains(text, "Current plan settings:") || !strings.Contains(text, "zh-Hant-HK") {
		t.Fatalf("message text missing hidden language settings: %q", text)
	}
}
