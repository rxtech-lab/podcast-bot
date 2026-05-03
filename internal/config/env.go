package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Env holds all process-level configuration loaded from .env / environment.
// It is treated as immutable after LoadEnv returns.
type Env struct {
	OpenAIBaseURL string
	OpenAIKey     string
	HostModel     string

	CompressionBaseURL string
	CompressionKey     string
	CompressionModel   string

	AzureSpeechKey    string
	AzureSpeechRegion string

	OutDir string
}

// LoadEnv reads .env (if present) then env vars, validates, and freezes config.
// Compression endpoint/key fall back to OpenAI ones when blank.
func LoadEnv() (*Env, error) {
	_ = godotenv.Load() // .env optional; env vars take precedence

	e := &Env{
		OpenAIBaseURL:      strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		OpenAIKey:          strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		HostModel:          strings.TrimSpace(os.Getenv("HOST_MODEL")),
		CompressionBaseURL: strings.TrimSpace(os.Getenv("COMPRESSION_BASE_URL")),
		CompressionKey:     strings.TrimSpace(os.Getenv("COMPRESSION_API_KEY")),
		CompressionModel:   strings.TrimSpace(os.Getenv("COMPRESSION_MODEL")),
		AzureSpeechKey:     strings.TrimSpace(os.Getenv("AZURE_SPEECH_KEY")),
		AzureSpeechRegion:  strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION")),
		OutDir:             strings.TrimSpace(os.Getenv("OUT_DIR")),
	}

	if e.CompressionBaseURL == "" {
		e.CompressionBaseURL = e.OpenAIBaseURL
	}
	if e.CompressionKey == "" {
		e.CompressionKey = e.OpenAIKey
	}
	if e.OutDir == "" {
		e.OutDir = "./out"
	}

	var missing []string
	if e.OpenAIBaseURL == "" {
		missing = append(missing, "OPENAI_BASE_URL")
	}
	if e.OpenAIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if e.HostModel == "" {
		missing = append(missing, "HOST_MODEL")
	}
	if e.CompressionModel == "" {
		missing = append(missing, "COMPRESSION_MODEL")
	}
	if e.AzureSpeechKey == "" {
		missing = append(missing, "AZURE_SPEECH_KEY")
	}
	if e.AzureSpeechRegion == "" {
		missing = append(missing, "AZURE_SPEECH_REGION")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return e, nil
}

// ErrEnvNotLoaded is returned when an Env was expected but not initialised.
var ErrEnvNotLoaded = errors.New("env not loaded")
