package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration loaded from ~/.config/axiom-tui/config.toml.
//
// Global settings live at the root. Per-dataset tuning lives under [datasets.<name>].
// Everything is optional — sensible defaults apply when a field is missing.
type Config struct {
	DefaultDataset  string                   `toml:"default_dataset"`
	RefreshSeconds  int                      `toml:"refresh_seconds"`
	StreamPollMs    int                      `toml:"stream_poll_ms"`
	LogBufferSize   int                      `toml:"log_buffer_size"`
	Datasets        map[string]DatasetConfig `toml:"datasets"`
}

// DatasetConfig overrides defaults for one Axiom dataset. Identified by the
// dataset name (the slug used in APL queries, e.g. `throxy-mega`).
//
// InterestingFields controls which dotted keys are extracted from each event
// and shown inline in the log stream. Axiom flattens structured logs into
// `fields.X` keys — list the ones you care about here.
//
// GroupByField is the field used to segment the throughput chart (top-5 lines).
// DurationField, if set, unlocks avg/p95/max latency in the stats panel.
// RoutePrefixes defines which message prefixes count as HTTP routes.
type DatasetConfig struct {
	InterestingFields []string `toml:"interesting_fields"`
	GroupByField      string   `toml:"group_by_field"`
	DurationField     string   `toml:"duration_field"`
	// DurationFields is the multi-field successor to DurationField. When set,
	// each field gets its own avg/p95/max row in the stats panel (e.g. separate
	// durationMs vs latencyMs). DurationField stays for backward compat — if
	// DurationFields is empty we fall back to [DurationField].
	DurationFields []string `toml:"duration_fields"`
	RoutePrefixes  []string `toml:"route_prefixes"`
}

// ResolvedDurationFields returns the ordered list of duration fields to query.
// Falls back to the legacy single DurationField when DurationFields is empty.
func (d DatasetConfig) ResolvedDurationFields() []string {
	if len(d.DurationFields) > 0 {
		return d.DurationFields
	}
	if d.DurationField != "" {
		return []string{d.DurationField}
	}
	return nil
}

// Defaults returns a config with reasonable defaults when no file exists.
func Defaults() Config {
	return Config{
		RefreshSeconds: 15,
		StreamPollMs:   2000,
		LogBufferSize:  2000,
		Datasets:       map[string]DatasetConfig{},
	}
}

// Resolve fills in defaults for missing fields.
func (c *Config) Resolve() {
	if c.RefreshSeconds <= 0 {
		c.RefreshSeconds = 15
	}
	if c.StreamPollMs <= 0 {
		c.StreamPollMs = 2000
	}
	if c.LogBufferSize <= 0 {
		c.LogBufferSize = 2000
	}
	if c.Datasets == nil {
		c.Datasets = map[string]DatasetConfig{}
	}
}

// DatasetOrDefault returns the DatasetConfig for name, or an empty one.
func (c Config) DatasetOrDefault(name string) DatasetConfig {
	if ds, ok := c.Datasets[name]; ok {
		return ds
	}
	return DatasetConfig{RoutePrefixes: []string{"GET", "POST", "PUT", "DELETE", "PATCH"}}
}

// LoadConfig reads config from path. If path is empty, uses the default
// ~/.config/axiom-tui/config.toml. A missing file is not an error — defaults
// are returned silently so the tool works out of the box.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Defaults(), nil
		}
		path = filepath.Join(home, ".config", "axiom-tui", "config.toml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c := Defaults()
			c.Resolve()
			return c, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	c.Resolve()
	return c, nil
}

// LoadEnvFile sources KEY=value lines from an .env-style file into the
// process environment, without overwriting existing values. Quiet on missing
// or unreadable files — this is a convenience, not a requirement.
func LoadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		if _, ok := os.LookupEnv(key); !ok {
			os.Setenv(key, val)
		}
	}
}
