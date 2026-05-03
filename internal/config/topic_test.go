package config_test

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestLoadTopicExample(t *testing.T) {
	tp, err := config.LoadTopic("../../examples/topic.md")
	if err != nil {
		t.Fatalf("load topic: %v", err)
	}
	if tp.Title == "" {
		t.Errorf("title empty")
	}
	if tp.Language == "" {
		t.Errorf("language empty")
	}
	if tp.TotalMinutes <= 0 {
		t.Errorf("total_minutes not positive: %d", tp.TotalMinutes)
	}
	if len(tp.Affirmative) < 2 || len(tp.Negative) < 2 {
		t.Errorf("expected 2+ candidates per side; got aff=%d neg=%d", len(tp.Affirmative), len(tp.Negative))
	}
	for _, a := range tp.Affirmative {
		if a.Name == "" || a.Model == "" {
			t.Errorf("affirmative entry missing fields: %+v", a)
		}
	}
	if tp.Judge.Model == "" {
		t.Errorf("judge.model empty")
	}
	if !strings.Contains(tp.Background, "AI") && !strings.Contains(tp.Background, "ai") {
		t.Errorf("background section missing or wrong content; got %q", tp.Background)
	}
	if tp.AffirmativePos == "" || tp.NegativePos == "" {
		t.Errorf("position sections empty")
	}
}

func TestLoadTopicMissing(t *testing.T) {
	_, err := config.LoadTopic("does-not-exist.md")
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}
