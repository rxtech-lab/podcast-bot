package config_test

import (
	"os"
	"path/filepath"
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

func TestLoadTopicChannelRequired(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "no-channel.md")
	if err := os.WriteFile(bad, []byte(`---
title: "x"
language: en-US
affirmative: [{name: A, model: m}]
negative:    [{name: B, model: m}]
judge:       {model: m}
---
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadTopic(bad); err == nil {
		t.Errorf("expected error when channel is missing")
	}

	good := filepath.Join(dir, "with-channel.md")
	if err := os.WriteFile(good, []byte(`---
title: "x"
language: en-US
channel: tech
affirmative: [{name: A, model: m}]
negative:    [{name: B, model: m}]
judge:       {model: m}
---
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tp, err := config.LoadTopic(good)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tp.Channel != "tech" {
		t.Errorf("Channel = %q, want %q", tp.Channel, "tech")
	}
}

func TestLoadTopicTTSProviderDefaultAndValidation(t *testing.T) {
	tp, err := config.LoadTopic("../../examples/topic.md")
	if err != nil {
		t.Fatalf("load topic: %v", err)
	}
	if tp.TTSProvider != config.TTSProviderAzure && tp.TTSProvider != config.TTSProviderEleven {
		t.Errorf("tts_provider must be azure or eleven; got %q", tp.TTSProvider)
	}

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(bad, []byte(`---
title: "x"
language: en-US
tts_provider: bogus
affirmative: [{name: A, model: m}]
negative:    [{name: B, model: m}]
judge:       {model: m}
---
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadTopic(bad); err == nil {
		t.Errorf("expected error for bogus tts_provider")
	}
}
