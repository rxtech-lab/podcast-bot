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
type: debate
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
type: debate
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
type: debate
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

func TestLoadTopicTypeRequired(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing-type.md")
	if err := os.WriteFile(missing, []byte(`---
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
	_, err := config.LoadTopic(missing)
	if err == nil {
		t.Fatalf("expected error when type is missing")
	}
	if !strings.Contains(err.Error(), "type must be one of") {
		t.Errorf("error should mention enum values; got %v", err)
	}
}

func TestLoadTopicTypeInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad-type.md")
	if err := os.WriteFile(bad, []byte(`---
title: "x"
type: bogus
language: en-US
channel: tech
affirmative: [{name: A, model: m}]
negative:    [{name: B, model: m}]
judge:       {model: m}
---
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadTopic(bad)
	if err == nil {
		t.Fatalf("expected error for invalid type")
	}
	if !strings.Contains(err.Error(), `"bogus"`) {
		t.Errorf("error should echo the bad value; got %v", err)
	}
}

func TestLoadTopicSituationPuzzle(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "puzzle.md")
	if err := os.WriteFile(good, []byte(`---
title: "海龜湯"
type: situation-puzzle
language: zh-CN
channel: tech
puzzle_host: { model: "gpt-4o" }
players:
  - { name: "Alice", model: "gpt-4o-mini" }
  - { name: "Bob", model: "gpt-4o-mini" }
---

## Surface

A man eats turtle soup at a restaurant and immediately walks home and ends his life.

## Truth

The man had previously been shipwrecked and the soup his crew fed him then was actually made from a fellow survivor, not turtle. Tasting real turtle soup made him realise the truth.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tp, err := config.LoadTopic(good)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tp.Type != config.ContentTypeSituationPuzzle {
		t.Errorf("Type = %q, want %q", tp.Type, config.ContentTypeSituationPuzzle)
	}
	if tp.PuzzleHost.Model != "gpt-4o" {
		t.Errorf("PuzzleHost.Model = %q", tp.PuzzleHost.Model)
	}
	if len(tp.Players) != 2 {
		t.Errorf("Players len = %d, want 2", len(tp.Players))
	}
	if !strings.Contains(tp.Surface, "turtle soup") {
		t.Errorf("Surface missing expected text; got %q", tp.Surface)
	}
	if !strings.Contains(tp.Truth, "shipwrecked") {
		t.Errorf("Truth missing expected text; got %q", tp.Truth)
	}
}

func TestLoadTopicSituationPuzzleMissingTruth(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "puzzle-no-truth.md")
	if err := os.WriteFile(bad, []byte(`---
title: "海龜湯"
type: situation-puzzle
language: zh-CN
channel: tech
puzzle_host: { model: "gpt-4o" }
players: [{ name: "Alice", model: "gpt-4o-mini" }]
---

## Surface

Some surface text.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadTopic(bad)
	if err == nil {
		t.Fatalf("expected error when Truth section is missing")
	}
	if !strings.Contains(err.Error(), "Truth") {
		t.Errorf("error should mention Truth; got %v", err)
	}
}

func TestLoadTopicSituationPuzzleRejectsDebateRoster(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "mixed.md")
	if err := os.WriteFile(bad, []byte(`---
title: "x"
type: situation-puzzle
language: zh-CN
channel: tech
puzzle_host: { model: "gpt-4o" }
players: [{ name: "Alice", model: "m" }]
affirmative: [{ name: "A", model: "m" }]
---

## Surface
s
## Truth
t
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadTopic(bad); err == nil {
		t.Fatalf("expected error when puzzle declares affirmative roster")
	}
}
