package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirily11/debate-bot/internal/util"
)

// Store creates per-agent memory files in a single directory.
type Store struct {
	dir string
	mu  sync.Mutex
	all map[string]*Memory
}

// NewStore initialises (and creates) the memory directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, all: map[string]*Memory{}}, nil
}

// For returns the Memory for the named agent, creating it if needed.
// File path is <dir>/<safe>_memory.md.
func (s *Store) For(agent string) *Memory {
	safe := util.Safe(agent)
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.all[safe]; ok {
		return m
	}
	m := &Memory{Path: filepath.Join(s.dir, safe+"_memory.md"), agentName: agent}
	s.all[safe] = m
	return m
}

// Memory wraps a single agent's memory.md file.
type Memory struct {
	Path      string
	agentName string
	mu        sync.Mutex
}

// Append writes one line to the memory file (newline appended).
func (m *Memory) Append(line string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.OpenFile(m.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open memory: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

// Read returns the entire current memory content (empty string if file absent).
func (m *Memory) Read() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := os.ReadFile(m.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// Replace atomically rewrites the memory file with new content.
func (m *Memory) Replace(content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tmp := m.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path)
}
