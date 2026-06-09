package tools

import (
	"context"
	"testing"
)

type stubAgent struct{ name string }

func (s stubAgent) AgentName() string          { return s.name }
func (stubAgent) AppendMemory(string) error    { return nil }
func (stubAgent) Transcript() []TranscriptLine { return nil }

func TestDataStoreSaveLoadList(t *testing.T) {
	tool := NewDataStoreTool(t.TempDir())
	ctx := context.Background()
	ann := stubAgent{name: "Ann"}

	if _, err := tool.Call(ctx, map[string]any{"op": "save", "key": "gdp", "value": "grew 2%"}, ann); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := tool.Call(ctx, map[string]any{"op": "load", "key": "gdp"}, ann)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != "grew 2%" {
		t.Fatalf("load got %q, want %q", got, "grew 2%")
	}

	// Per-agent isolation: a different agent does not see Ann's entry.
	bo := stubAgent{name: "Bo"}
	if got, _ := tool.Call(ctx, map[string]any{"op": "load", "key": "gdp"}, bo); got != `no entry for "gdp"` {
		t.Fatalf("isolation broken: Bo saw %q", got)
	}

	list, err := tool.Call(ctx, map[string]any{"op": "list"}, ann)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list != "gdp" {
		t.Fatalf("list got %q, want %q", list, "gdp")
	}

	// save requires a value.
	if _, err := tool.Call(ctx, map[string]any{"op": "save", "key": "x"}, ann); err == nil {
		t.Fatalf("expected error saving empty value")
	}
}
