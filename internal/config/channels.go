package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Channel is one TV-style channel definition: a stable id (referenced from
// debate.md frontmatter), a human-facing channel number, and a display title.
type Channel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// ChannelsConfig is the on-disk shape of channels.json. The file is just a
// `{"channels": [...]}` wrapper so future top-level fields (defaults, etc.)
// can be added without breaking the schema.
type ChannelsConfig struct {
	Channels []Channel `json:"channels"`
}

// LoadChannels parses channels.json and validates it: ids must be non-empty
// and unique, channel numbers must be unique, titles must be non-empty.
func LoadChannels(path string) (*ChannelsConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read channels: %w", err)
	}
	var cfg ChannelsConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse channels: %w", err)
	}
	if len(cfg.Channels) == 0 {
		return nil, fmt.Errorf("channels.json defines zero channels")
	}
	seenID := map[string]bool{}
	seenNum := map[int]string{}
	for i, c := range cfg.Channels {
		if c.ID == "" {
			return nil, fmt.Errorf("channels[%d]: id is required", i)
		}
		if c.Title == "" {
			return nil, fmt.Errorf("channels[%d] (%s): title is required", i, c.ID)
		}
		if c.Number <= 0 {
			return nil, fmt.Errorf("channels[%d] (%s): number must be > 0", i, c.ID)
		}
		if seenID[c.ID] {
			return nil, fmt.Errorf("duplicate channel id %q", c.ID)
		}
		if other, dup := seenNum[c.Number]; dup {
			return nil, fmt.Errorf("channel %q reuses number %d already taken by %q",
				c.ID, c.Number, other)
		}
		seenID[c.ID] = true
		seenNum[c.Number] = c.ID
	}
	return &cfg, nil
}

// Find returns the channel with the given id, or nil if not defined.
func (c *ChannelsConfig) Find(id string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].ID == id {
			return &c.Channels[i]
		}
	}
	return nil
}
