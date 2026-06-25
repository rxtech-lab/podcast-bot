package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

func newTestPlanningStore(t *testing.T) (*DiscussionStore, *PlanningStore, string) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "planning.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	ps, err := NewPlanningStore(ds)
	if err != nil {
		t.Fatalf("NewPlanningStore: %v", err)
	}
	d, err := ds.CreatePlaceholder(context.Background(), "u1", "AI in education", "en-US")
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}
	return ds, ps, d.ID
}

func TestPlanningEnsureConversationIdempotent(t *testing.T) {
	_, ps, discID := newTestPlanningStore(t)
	ctx := context.Background()
	a, err := ps.EnsureConversation(ctx, "u1", discID)
	if err != nil || a == nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	b, err := ps.EnsureConversation(ctx, "u1", discID)
	if err != nil || b == nil {
		t.Fatalf("EnsureConversation (2): %v", err)
	}
	if a.ID != b.ID {
		t.Fatalf("expected one conversation per discussion, got %q and %q", a.ID, b.ID)
	}
}

func TestPlanningTurnsRebuildAndQuestionRoundTrip(t *testing.T) {
	_, ps, discID := newTestPlanningStore(t)
	ctx := context.Background()
	conv, err := ps.EnsureConversation(ctx, "u1", discID)
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	mustAppend := func(in planningTurnInput) {
		if err := ps.AppendTurn(ctx, conv.ID, in); err != nil {
			t.Fatalf("AppendTurn(%s): %v", in.Role, err)
		}
	}

	mustAppend(planningTurnInput{Role: "user", Text: "make me a podcast"})
	mustAppend(planningTurnInput{Role: "assistant", Text: "", ToolCalls: []llm.ToolCall{{
		ID:        "call_1",
		Name:      "ask_question",
		Arguments: `{"questions":[{"title":"How long?","type":"single_choice"}]}`,
	}}})
	mustAppend(planningTurnInput{
		Role:           "question",
		ToolCallID:     "call_1",
		ToolName:       "ask_question",
		QuestionID:     "q1",
		QuestionsJSON:  `[{"title":"How long?","type":"single_choice"}]`,
		QuestionStatus: "pending",
	})

	pending, err := ps.PendingQuestion(ctx, conv.ID, "q1")
	if err != nil || pending == nil {
		t.Fatalf("PendingQuestion: %v (pending=%v)", err, pending)
	}
	if pending.ToolCallID != "call_1" {
		t.Fatalf("pending question tool_call_id = %q, want call_1", pending.ToolCallID)
	}

	if err := ps.RecordAnswer(ctx, conv.ID, "q1", `[{"questionIndex":0,"answer":"Short"}]`, "answered"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	// Pending question is now answered, so PendingQuestion returns nil.
	if again, _ := ps.PendingQuestion(ctx, conv.ID, "q1"); again != nil {
		t.Fatalf("expected no pending question after answering")
	}
	// Synthetic tool result closes the ask_question call.
	mustAppend(planningTurnInput{Role: "tool", ToolCallID: "call_1", ToolName: "ask_question", ResultText: "The user answered."})

	turns, err := ps.Turns(ctx, conv.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}

	// LLM rebuild: user, assistant(with tool call), tool — question turn skipped.
	msgs := planningMessagesForLLM(turns)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 LLM messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleUser {
		t.Fatalf("msg0 role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != llm.RoleAssistant || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("msg1 should be assistant with tool call call_1: %+v", msgs[1])
	}
	if msgs[2].Role != llm.RoleTool || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("msg2 should be a tool result for call_1: %+v", msgs[2])
	}

	// Client rebuild: a user text part and a tool part (question card, completed).
	parts := planningConversationParts(turns)
	var sawUser, sawQuestion bool
	for _, p := range parts {
		if p.Kind == "text" && p.Role == "user" {
			sawUser = true
		}
		if p.Kind == "tool" && p.ToolCallID == "call_1" {
			sawQuestion = true
			if p.QuestionID != "q1" {
				t.Fatalf("question part question_id = %q, want q1", p.QuestionID)
			}
			if p.Status != "completed" {
				t.Fatalf("answered question status = %q, want completed", p.Status)
			}
		}
	}
	if !sawUser || !sawQuestion {
		t.Fatalf("expected a user part and a question tool part (user=%v question=%v)", sawUser, sawQuestion)
	}
}

func TestPlanningAppendTurnIdempotentOnOpID(t *testing.T) {
	_, ps, discID := newTestPlanningStore(t)
	ctx := context.Background()
	conv, _ := ps.EnsureConversation(ctx, "u1", discID)
	in := planningTurnInput{Role: "user", Text: "hi", OpID: "fixed-op"}
	if err := ps.AppendTurn(ctx, conv.ID, in); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := ps.AppendTurn(ctx, conv.ID, in); err != nil {
		t.Fatalf("AppendTurn (retry): %v", err)
	}
	turns, _ := ps.Turns(ctx, conv.ID)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn after idempotent re-append, got %d", len(turns))
	}
}

func TestPlanningConversationOnlyShowsLatestShowPlan(t *testing.T) {
	_, ps, discID := newTestPlanningStore(t)
	ctx := context.Background()
	conv, _ := ps.EnsureConversation(ctx, "u1", discID)
	topic := func(title string) *config.DebateTopic {
		return &config.DebateTopic{
			Title:             title,
			Type:              config.ContentTypeDiscussion,
			Language:          "en-US",
			TotalMinutes:      30,
			SegmentMaxSeconds: 60,
			TTSProvider:       config.TTSProviderAzure,
			Resolution:        config.Resolution1080p,
			Channel:           "test",
			Storage:           config.StoragePlaintext,
			Host:              config.AgentSpec{Name: "Host", Model: "test"},
			Discussants: []config.AgentSpec{
				{Name: "A", Aspect: "policy", Model: "test"},
				{Name: "B", Aspect: "tech", Model: "test"},
			},
		}
	}
	mustAppend := func(in planningTurnInput) {
		if err := ps.AppendTurn(ctx, conv.ID, in); err != nil {
			t.Fatalf("AppendTurn(%s): %v", in.Role, err)
		}
	}
	mustAppend(planningTurnInput{Role: "user", Text: "plan"})
	mustAppend(planningTurnInput{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "write_1", Name: "write_plan", Arguments: `{"title":"hidden"}`},
		{ID: "show_1", Name: "show_plan", Arguments: `{}`},
		{ID: "show_2", Name: "show_plan", Arguments: `{}`},
	}})
	mustAppend(planningTurnInput{Role: "tool", ToolCallID: "write_1", ToolName: "write_plan", ResultText: "hidden", Script: topic("Hidden")})
	mustAppend(planningTurnInput{Role: "tool", ToolCallID: "show_1", ToolName: "show_plan", ResultText: "shown", Script: topic("First")})
	mustAppend(planningTurnInput{Role: "tool", ToolCallID: "show_2", ToolName: "show_plan", ResultText: "shown", Script: topic("Second")})

	turns, err := ps.Turns(ctx, conv.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	parts := planningConversationParts(turns)
	var planTitles []string
	for _, p := range parts {
		if p.ToolName == "show_plan" && p.Script != nil {
			planTitles = append(planTitles, p.Script.Title)
		}
		if p.ToolName == "write_plan" && p.Script != nil {
			t.Fatalf("write_plan should not be projected as a visible plan")
		}
	}
	if len(planTitles) != 1 || planTitles[0] != "Second" {
		t.Fatalf("visible plans = %+v, want only Second", planTitles)
	}
}

func TestPlanningConversationNeedsRunAfterSeededUser(t *testing.T) {
	_, ps, discID := newTestPlanningStore(t)
	ctx := context.Background()
	conv, _ := ps.EnsureConversation(ctx, "u1", discID)
	if err := ps.AppendTurn(ctx, conv.ID, planningTurnInput{Role: "user", Text: "seeded"}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	turns, _ := ps.Turns(ctx, conv.ID)
	if !planningConversationNeedsRun(turns) {
		t.Fatalf("seeded user turn should need a run")
	}
	if err := ps.AppendTurn(ctx, conv.ID, planningTurnInput{Role: "assistant", Text: "done"}); err != nil {
		t.Fatalf("AppendTurn assistant: %v", err)
	}
	turns, _ = ps.Turns(ctx, conv.ID)
	if planningConversationNeedsRun(turns) {
		t.Fatalf("assistant-terminal conversation should not need a run")
	}
}

func TestPlanningConversationHidesSeededSettingsInUserBubble(t *testing.T) {
	seeded := `Design a panel discussion about the following topic.

Topic: Battery supply chains

Plan settings:
- Language for all names and text: zh-Hant-HK
- Number of discussants: 4

Use exactly 4 discussants.`
	parts := planningConversationParts([]planningTurnRow{{ID: 1, Role: "user", Text: seeded}})
	if len(parts) != 1 || parts[0].Text != "Battery supply chains" {
		t.Fatalf("seeded display text = %+v, want only topic", parts)
	}
	plain := "Please make it more technical"
	parts = planningConversationParts([]planningTurnRow{{ID: 2, Role: "user", Text: plain}})
	if len(parts) != 1 || parts[0].Text != plain {
		t.Fatalf("plain display text = %+v, want unchanged", parts)
	}
	withLanguage := `Please make it more technical

Current plan settings:
- Language for all names and text: zh-Hant-HK`
	parts = planningConversationParts([]planningTurnRow{{ID: 3, Role: "user", Text: withLanguage}})
	if len(parts) != 1 || parts[0].Text != "Please make it more technical" {
		t.Fatalf("language settings display text = %+v, want only visible message", parts)
	}
}
