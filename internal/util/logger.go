package util

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// NewFileLogger returns a slog.Logger writing JSON lines to <dir>/run.log and
// stderr, so long-running server modes remain inspectable from the terminal.
// Errors creating the file fall back to stderr.
func NewFileLogger(dir string) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return slog.New(slog.NewJSONHandler(os.Stderr, nil)), io.NopCloser(nil), err
	}
	h := slog.NewJSONHandler(io.MultiWriter(f, os.Stderr), &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h), f, nil
}
