package config_test

import (
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestModelsFromIDs(t *testing.T) {
	defaults := config.ModelDefaults{
		Host:         "anthropic/claude-opus-4-8",
		ScenePlanner: "anthropic/claude-opus-4-8",
		Compression:  "openai/gpt-4o-mini",
	}
	ids := []string{
		"anthropic/claude-opus-4-8",
		"  openai/gpt-4o  ", // surrounding whitespace is trimmed
		"",                  // dropped
		"gpt-4o",            // no "/" -> provider defaults to openai
		"openai/gpt-4o",     // duplicate of the trimmed entry -> dropped
		"openai/gpt-4o-mini",
	}

	got := config.ModelsFromIDs(ids, defaults)

	if len(got) != 4 {
		t.Fatalf("expected 4 unique models, got %d: %+v", len(got), got)
	}

	byID := make(map[string]config.ModelInfo, len(got))
	for _, m := range got {
		byID[m.ID] = m
	}

	opus, ok := byID["anthropic/claude-opus-4-8"]
	if !ok {
		t.Fatal("opus missing from roster")
	}
	if opus.Provider != "anthropic" {
		t.Errorf("opus provider = %q, want anthropic", opus.Provider)
	}
	if opus.Label != "anthropic/claude-opus-4-8" {
		t.Errorf("opus label = %q, want the raw id", opus.Label)
	}
	if len(opus.DefaultFor) != 2 {
		t.Errorf("opus DefaultFor = %v, want host + scene_planner", opus.DefaultFor)
	}

	mini, ok := byID["openai/gpt-4o-mini"]
	if !ok {
		t.Fatal("gpt-4o-mini missing from roster")
	}
	if len(mini.DefaultFor) != 1 || mini.DefaultFor[0] != "compression" {
		t.Errorf("mini DefaultFor = %v, want [compression]", mini.DefaultFor)
	}

	bare, ok := byID["gpt-4o"]
	if !ok {
		t.Fatal("bare id gpt-4o missing from roster")
	}
	if bare.Provider != "openai" {
		t.Errorf("bare provider = %q, want openai fallback", bare.Provider)
	}
	if len(bare.DefaultFor) != 0 {
		t.Errorf("bare DefaultFor = %v, want none", bare.DefaultFor)
	}
}

func TestModelsFromDescriptorsCarriesType(t *testing.T) {
	got := config.ModelsFromDescriptors([]config.ModelDescriptor{
		{ID: "openai/gpt-4o", Type: config.ModelTypeLanguage},
		{ID: "openai/text-embedding-3-small", Type: config.ModelTypeEmbedding},
		{ID: "legacy/untyped"},
	}, config.ModelDefaults{})

	if len(got) != 3 {
		t.Fatalf("expected 3 models, got %d: %+v", len(got), got)
	}
	if got[0].Type != config.ModelTypeLanguage || got[1].Type != config.ModelTypeEmbedding || got[2].Type != "" {
		t.Errorf("types = %q/%q/%q, want language/embedding/empty", got[0].Type, got[1].Type, got[2].Type)
	}
}

func TestAnnotateModelDefaultsPreservesType(t *testing.T) {
	cached := config.ModelsFromDescriptors([]config.ModelDescriptor{
		{ID: "openai/gpt-4o", Type: config.ModelTypeLanguage},
		{ID: "openai/text-embedding-3-small", Type: config.ModelTypeEmbedding},
	}, config.ModelDefaults{})

	got := config.AnnotateModelDefaults(cached, config.ModelDefaults{Host: "openai/gpt-4o"})

	if len(got) != 2 {
		t.Fatalf("expected 2 models, got %d", len(got))
	}
	if len(got[0].DefaultFor) != 1 || got[0].DefaultFor[0] != "host" {
		t.Errorf("DefaultFor = %v, want [host] re-stamped from current defaults", got[0].DefaultFor)
	}
	if got[1].Type != config.ModelTypeEmbedding {
		t.Errorf("cache-hit annotation dropped Type: %+v", got[1])
	}
}

func TestDefaultsForEnvNil(t *testing.T) {
	if got := config.DefaultsForEnv(nil); got != (config.ModelDefaults{}) {
		t.Errorf("DefaultsForEnv(nil) = %+v, want zero value", got)
	}
}
