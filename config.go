package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration loaded from ~/.config/tailboard/config.toml.
//
// Global settings live at the root. Per-source tuning lives under
// [sources.<name>]. The legacy [datasets.<name>] table is still decoded and
// mapped onto sources by Normalise. Everything is optional, sensible
// defaults apply when a field is missing.
type Config struct {
	DefaultSource  string                   `toml:"default_source"`
	DefaultDataset string                   `toml:"default_dataset"`
	RefreshSeconds int                      `toml:"refresh_seconds"`
	StreamPollMs   int                      `toml:"stream_poll_ms"`
	LogBufferSize  int                      `toml:"log_buffer_size"`
	Datasets       map[string]DatasetConfig `toml:"datasets"`
	Sources        map[string]SourceConfig  `toml:"sources"`

	// builtin names the sources Normalise registered itself (the pi
	// source) rather than the config file or the command line. They are
	// excluded from the "exactly one configured source" resolution rule.
	builtin map[string]bool
}

// IsBuiltin reports whether name is a source the tool registered itself
// and the config has not replaced.
func (c Config) IsBuiltin(name string) bool { return c.builtin[name] }

// DatasetConfig is the legacy per-dataset table. It is decoded for
// backwards compatibility and mapped onto a SourceConfig by Normalise.
type DatasetConfig struct {
	InterestingFields []string `toml:"interesting_fields"`
	GroupByField      string   `toml:"group_by_field"`
	DurationField     string   `toml:"duration_field"`
	DurationFields    []string `toml:"duration_fields"`
	RoutePrefixes     []string `toml:"route_prefixes"`
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

// SourceConfig describes one source: where events come from and how the
// fields map onto the dashboard. Keys that only apply to one type are
// ignored by the others.
type SourceConfig struct {
	Type          string       `toml:"type"`
	Dataset       string       `toml:"dataset"`
	Paths         StringList   `toml:"paths"`
	Backfill      Duration     `toml:"backfill"`
	MaxEvents     int          `toml:"max_events"`
	MaxLineBytes  int          `toml:"max_line_bytes"`
	OrphanAfter   Duration     `toml:"orphan_after"`
	RoutePrefixes StringList   `toml:"route_prefixes"`
	HideLevels    StringList   `toml:"hide_levels"`
	Fields        FieldsConfig `toml:"fields"`
}

// FieldsConfig maps event keys onto the dashboard's dimensions. Dot paths
// are tried as a literal key first, then as nested object navigation.
type FieldsConfig struct {
	Time          StringList        `toml:"time"`
	TimeFormat    string            `toml:"time_format"`
	Level         StringList        `toml:"level"`
	LevelMap      map[string]string `toml:"level_map"`
	Message       StringList        `toml:"message"`
	Group         string            `toml:"group"`
	Cost          string            `toml:"cost"`
	CostGroup     string            `toml:"cost_group"`
	Duration      StringList        `toml:"duration"`
	DurationUnit  string            `toml:"duration_unit"`
	Provider      string            `toml:"provider"`
	Operation     string            `toml:"operation"`
	Trace         string            `toml:"trace"`
	Interesting   StringList        `toml:"interesting"`
	DisplayPrefix string            `toml:"display_prefix"`
}

// StringList accepts either a single string or an array of strings in TOML.
//
// Presence is kept: an absent key decodes to nil, while an explicit empty
// array decodes to a non-nil empty list. applyTypeDefaults relies on that to
// default only the keys the config did not set.
type StringList []string

// UnmarshalTOML implements toml.Unmarshaler.
func (l *StringList) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case nil:
		*l = nil
	case string:
		*l = StringList{t}
	case []any:
		out := make(StringList, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("expected string in list, got %T", item)
			}
			out = append(out, s)
		}
		*l = out
	case []string:
		*l = StringList(t)
	default:
		return fmt.Errorf("expected string or list of strings, got %T", v)
	}
	return nil
}

// Duration is a time.Duration decoded from a Go duration string ("48h").
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// defaultRoutePrefixes is the axiom default for route_prefixes.
var defaultRoutePrefixes = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

const (
	defaultBackfill     = 48 * time.Hour
	defaultMaxEvents    = 500000
	defaultMaxLineBytes = 16 * 1024 * 1024
	defaultOrphanAfter  = 120 * time.Second
	defaultPiPaths      = "~/.pi/agent/sessions"
	builtinPiName       = "pi"
)

// Defaults returns a config with reasonable defaults when no file exists.
func Defaults() Config {
	return Config{
		RefreshSeconds: 15,
		StreamPollMs:   2000,
		LogBufferSize:  2000,
		Datasets:       map[string]DatasetConfig{},
		Sources:        map[string]SourceConfig{},
	}
}

// Resolve fills in defaults for missing root fields.
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
	if c.Sources == nil {
		c.Sources = map[string]SourceConfig{}
	}
}

// Normalise runs after decode and before anything else reads the config:
// registers the built-in pi source, maps legacy [datasets] onto sources,
// applies per-type defaults and validates what can be validated statically.
func (c *Config) Normalise() error {
	// Zero means unset and takes the default; a negative value is a mistake.
	for _, r := range []struct {
		key string
		v   int
	}{
		{"refresh_seconds", c.RefreshSeconds},
		{"stream_poll_ms", c.StreamPollMs},
		{"log_buffer_size", c.LogBufferSize},
	} {
		if r.v < 0 {
			return fmt.Errorf("%s = %d must not be negative", r.key, r.v)
		}
	}
	c.Resolve()

	// The built-in pi source exists before the config is applied; a config
	// entry with the same name replaces it.
	if _, ok := c.Sources[builtinPiName]; !ok {
		if _, legacy := c.Datasets[builtinPiName]; !legacy {
			c.Sources[builtinPiName] = SourceConfig{Type: "pi"}
			if c.builtin == nil {
				c.builtin = map[string]bool{}
			}
			c.builtin[builtinPiName] = true
		}
	}

	for name, ds := range c.Datasets {
		if _, dup := c.Sources[name]; dup {
			return fmt.Errorf("%q is defined under both [datasets] and [sources]", name)
		}
		c.Sources[name] = legacyToSource(name, ds)
	}

	// Every source is checked, not only the one about to open, so a broken
	// entry fails at load. Sorted so the first error is stable.
	for _, name := range c.SourceNames() {
		sc := c.Sources[name]
		sc.applyTypeDefaults(name)
		if err := sc.validate(name); err != nil {
			return err
		}
		c.Sources[name] = sc
	}
	return nil
}

// layoutReference is Go's reference instant. Formatting it yields
// single-digit month, day, hour, minute and second, which flushes out
// layouts whose fields run together and only parse back on some dates.
var layoutReference = time.Date(2006, time.January, 2, 15, 4, 5, 0, time.FixedZone("MST", -7*60*60))

// checkTimeLayout reports whether layout parses back what it formats, now
// and at the reference instant.
func checkTimeLayout(layout string) error {
	for _, at := range []time.Time{time.Now(), layoutReference} {
		if _, err := time.Parse(layout, at.Format(layout)); err != nil {
			return err
		}
	}
	return nil
}

// validate checks what can be checked without opening the source: the
// rules every type shares, then the type's own. Unset keys are allowed, so
// it holds both before and after applyTypeDefaults.
func (sc SourceConfig) validate(name string) error {
	t, ok := lookupSourceType(sc.Type)
	if !ok {
		return unknownTypeError(name, sc.Type)
	}
	f := sc.Fields
	if _, ok := durationScale(f.DurationUnit); !ok {
		return fmt.Errorf("source %q: unknown duration_unit %q (valid: ms, s, us, ns)", name, f.DurationUnit)
	}
	if sc.MaxLineBytes > math.MaxInt32 {
		return fmt.Errorf("source %q: max_line_bytes %d is over the limit of %d", name, sc.MaxLineBytes, math.MaxInt32)
	}
	if f.TimeFormat != "" {
		if err := checkTimeLayout(f.TimeFormat); err != nil {
			return fmt.Errorf("source %q: time_format %q does not parse its own output: %v", name, f.TimeFormat, err)
		}
	}
	if t.validate != nil {
		return t.validate(name, sc)
	}
	return nil
}

// legacyAxiomFields is the field mapping axiom-tui hardcoded before sources
// existed: cost, cost group, the p/o filters and the trace key. Only the
// legacy paths ([datasets.<name>] and an ad hoc dataset) get it, so their
// APL stays byte-identical; a [sources.<name>] axiom source maps its own.
func legacyAxiomFields() FieldsConfig {
	return FieldsConfig{
		Cost:      "fields.costDollars",
		CostGroup: "fields.clientName",
		Provider:  "fields.provider",
		Operation: "fields.operation",
		Trace:     "fields.requestId",
	}
}

// legacyToSource maps a [datasets.<name>] entry onto an axiom SourceConfig.
// A legacy entry without route_prefixes carries nil through, so it still
// gets the five default prefixes.
func legacyToSource(name string, ds DatasetConfig) SourceConfig {
	sc := SourceConfig{
		Type:          "axiom",
		Dataset:       name,
		RoutePrefixes: StringList(ds.RoutePrefixes),
		Fields:        legacyAxiomFields(),
	}
	sc.Fields.Interesting = StringList(ds.InterestingFields)
	sc.Fields.Group = ds.GroupByField
	sc.Fields.Duration = StringList(ds.ResolvedDurationFields())
	return sc
}

// applyTypeDefaults fills every unset key with the default for the source's
// type, which each backend registers next to its opener. A list key is
// unset only when nil: an explicit empty list in the config stays empty,
// so `hide_levels = []` hides nothing and `route_prefixes = []` turns the
// Top Routes panel off.
func (sc *SourceConfig) applyTypeDefaults(name string) {
	if t, ok := lookupSourceType(sc.Type); ok && t.defaults != nil {
		t.defaults(name, sc)
	}
}

// applyFileDefaults fills the keys every file-backed type shares: the
// backfill window and the event and line size limits.
func (sc *SourceConfig) applyFileDefaults() {
	if sc.Backfill == 0 {
		sc.Backfill = Duration(defaultBackfill)
	}
	if sc.MaxEvents <= 0 {
		sc.MaxEvents = defaultMaxEvents
	}
	if sc.MaxLineBytes <= 0 {
		sc.MaxLineBytes = defaultMaxLineBytes
	}
}

// DefaultSourceName returns default_source, else default_dataset, else "".
func (c Config) DefaultSourceName() string {
	if c.DefaultSource != "" {
		return c.DefaultSource
	}
	return c.DefaultDataset
}

// SourceNames returns the configured source names sorted, built-in pi included.
func (c Config) SourceNames() []string {
	names := make([]string, 0, len(c.Sources))
	for n := range c.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// UserSourceNames returns the names of sources that came from the config
// file (or the CLI), excluding the built-in pi registration.
func (c Config) UserSourceNames() []string {
	names := []string{}
	for n := range c.Sources {
		if !c.builtin[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// AddAxiomSource registers an ad hoc axiom source for a dataset that is not
// in the config, with the legacy field mapping. A configured axiom source
// of that name is kept; a dataset name always means Axiom, so a configured
// source of another type is replaced for this run.
func (c *Config) AddAxiomSource(name string) {
	if sc, ok := c.Sources[name]; ok && sc.Type == "axiom" {
		return
	}
	sc := SourceConfig{Type: "axiom", Dataset: name, Fields: legacyAxiomFields()}
	sc.applyTypeDefaults(name)
	c.Sources[name] = sc
	delete(c.builtin, name)
}

// AddFileSource registers an ad hoc jsonl source over paths, named after the
// first path.
func (c *Config) AddFileSource(name string, paths []string) {
	sc := SourceConfig{Type: "jsonl", Paths: StringList(paths)}
	sc.applyTypeDefaults(name)
	c.Sources[name] = sc
	delete(c.builtin, name)
}

// configDirs returns the config directories in lookup order: the tailboard
// directory first, then the legacy axiom-tui one. Reads fall through the
// list; writes always target the first entry.
func configDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".config", "tailboard"),
		filepath.Join(home, ".config", "axiom-tui"),
	}
}

// findConfigFile returns the first existing file named base across
// configDirs(), or "" when none exists.
func findConfigFile(base string) string {
	for _, dir := range configDirs() {
		p := filepath.Join(dir, base)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// LoadConfig reads config from path. If path is empty, uses the default
// ~/.config/tailboard/config.toml, falling back silently to the legacy
// ~/.config/axiom-tui/config.toml. A missing file is not an error, defaults
// are returned silently so the tool works out of the box. The returned
// config is normalised.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = findConfigFile("config.toml")
		if path == "" {
			c := Defaults()
			return c, c.Normalise()
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c := Defaults()
			return c, c.Normalise()
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return ParseConfig(string(data))
}

// ParseConfig decodes TOML text and normalises the result.
func ParseConfig(text string) (Config, error) {
	var c Config
	if _, err := toml.Decode(text, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Normalise(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// LoadEnvFile sources KEY=value lines from an .env-style file into the
// process environment, without overwriting existing values. A file that
// cannot be read is an error: it was asked for by name.
func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
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
	return nil
}
