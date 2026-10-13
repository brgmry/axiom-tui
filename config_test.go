package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestConfigLegacy(t *testing.T) {
	cfg, err := ParseConfig(readmeConfig)
	if err != nil {
		t.Fatal(err)
	}
	sc, ok := cfg.Sources["my-dataset"]
	if !ok {
		t.Fatalf("legacy dataset not mapped: %v", cfg.SourceNames())
	}
	if sc.Type != "axiom" {
		t.Errorf("type = %q, want axiom", sc.Type)
	}
	if sc.Dataset != "my-dataset" {
		t.Errorf("dataset = %q", sc.Dataset)
	}
	if sc.Fields.Group != "fields.userId" {
		t.Errorf("group = %q", sc.Fields.Group)
	}
	if len(sc.Fields.Duration) != 1 || sc.Fields.Duration[0] != "fields.durationMs" {
		t.Errorf("duration = %v", sc.Fields.Duration)
	}
	if len(sc.Fields.Interesting) != 3 {
		t.Errorf("interesting = %v", sc.Fields.Interesting)
	}
	if len(sc.RoutePrefixes) != 5 {
		t.Errorf("route prefixes = %v", sc.RoutePrefixes)
	}
	if cfg.DefaultSourceName() != "my-dataset" {
		t.Errorf("default source = %q", cfg.DefaultSourceName())
	}
	// Axiom defaults that keep the APL byte-identical.
	if sc.Fields.Cost != "fields.costDollars" || sc.Fields.CostGroup != "fields.clientName" {
		t.Errorf("cost mapping = %q by %q", sc.Fields.Cost, sc.Fields.CostGroup)
	}
	if sc.Fields.DisplayPrefix != "fields." {
		t.Errorf("display prefix = %q", sc.Fields.DisplayPrefix)
	}
	if sc.Fields.Trace != "fields.requestId" {
		t.Errorf("trace = %q", sc.Fields.Trace)
	}
	// Built-in pi is present alongside.
	if pi, ok := cfg.Sources["pi"]; !ok || pi.Type != "pi" || !cfg.IsBuiltin("pi") {
		t.Errorf("built-in pi missing or wrong: %+v", pi)
	}
}

func TestConfigLegacyDefaultsWithoutFields(t *testing.T) {
	cfg, err := ParseConfig(`
[datasets.bare]
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["bare"]
	if len(sc.RoutePrefixes) != 5 {
		t.Errorf("route prefixes = %v", sc.RoutePrefixes)
	}
	if sc.Fields.Group != "" {
		t.Errorf("group should be empty, got %q", sc.Fields.Group)
	}
	if len(sc.Fields.Duration) != 0 {
		t.Errorf("duration should be empty, got %v", sc.Fields.Duration)
	}
}

func TestConfigConflict(t *testing.T) {
	_, err := ParseConfig(`
[datasets.x]
[sources.x]
type = "axiom"
`)
	if err == nil {
		t.Fatal("expected error for a name in both tables")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should name the source: %v", err)
	}
	// main prints "config: <err>"; the error itself must not repeat it.
	if strings.HasPrefix(err.Error(), "config:") {
		t.Errorf("error should not carry its own config: prefix: %v", err)
	}
}

// TestConfigErrorPrefix: every config error is printed by main with one
// "config:" prefix, so none of them may start with it.
func TestConfigErrorPrefix(t *testing.T) {
	for _, text := range []string{
		"[datasets.x]\n[sources.x]\ntype = \"axiom\"\n",
		"[sources.j]\ntype = \"jsonl\"\n",
	} {
		_, err := ParseConfig(text)
		if err == nil {
			t.Fatalf("expected an error for %q", text)
		}
		if strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("doubled prefix: config: %v", err)
		}
	}
}

func TestConfigJsonlNeedsPaths(t *testing.T) {
	_, err := ParseConfig(`
[sources.app]
type = "jsonl"
`)
	if err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("expected a paths error, got %v", err)
	}
}

func TestConfigPathFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacy := filepath.Join(home, ".config", "axiom-tui")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("refresh_seconds = 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 7 {
		t.Errorf("legacy config not read: refresh = %d", cfg.RefreshSeconds)
	}

	modern := filepath.Join(home, ".config", "tailboard")
	if err := os.MkdirAll(modern, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modern, "config.toml"), []byte("refresh_seconds = 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 9 {
		t.Errorf("tailboard config should win: refresh = %d", cfg.RefreshSeconds)
	}

	// No config at all: defaults, still normalised with the built-in pi.
	t.Setenv("HOME", t.TempDir())
	cfg, err = LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 15 || cfg.Sources["pi"].Type != "pi" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
}

func TestConfigStringListAndDurations(t *testing.T) {
	cfg, err := ParseConfig(`
[sources.app]
type = "jsonl"
paths = "~/logs/*.jsonl"
backfill = "2h"
hide_levels = ["debug", "info"]

[sources.app.fields]
time = "ts"
level = ["lvl", "severity"]
duration = "durationMs"
level_map = { "WARNING" = "warn" }
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["app"]
	if len(sc.Paths) != 1 || sc.Paths[0] != "~/logs/*.jsonl" {
		t.Errorf("paths = %v", sc.Paths)
	}
	if sc.Backfill.D() != 2*time.Hour {
		t.Errorf("backfill = %v", sc.Backfill.D())
	}
	if len(sc.HideLevels) != 2 {
		t.Errorf("hide_levels = %v", sc.HideLevels)
	}
	if len(sc.Fields.Time) != 1 || sc.Fields.Time[0] != "ts" {
		t.Errorf("time = %v", sc.Fields.Time)
	}
	if len(sc.Fields.Level) != 2 {
		t.Errorf("level = %v", sc.Fields.Level)
	}
	if len(sc.Fields.Duration) != 1 {
		t.Errorf("duration = %v", sc.Fields.Duration)
	}
	if sc.Fields.LevelMap["WARNING"] != "warn" {
		t.Errorf("level_map = %v", sc.Fields.LevelMap)
	}
	if len(sc.Fields.Message) != 2 || sc.Fields.Message[0] != "message" {
		t.Errorf("message default = %v", sc.Fields.Message)
	}
	if sc.MaxEvents != defaultMaxEvents || sc.MaxLineBytes != defaultMaxLineBytes {
		t.Errorf("file defaults = %d / %d", sc.MaxEvents, sc.MaxLineBytes)
	}
}

func TestConfigPiDefaults(t *testing.T) {
	cfg, err := ParseConfig(`
[sources.agents]
type = "pi"
orphan_after = "30s"
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["agents"]
	if len(sc.Paths) != 1 || sc.Paths[0] != defaultPiPaths {
		t.Errorf("paths = %v", sc.Paths)
	}
	if sc.Backfill.D() != defaultBackfill {
		t.Errorf("backfill = %v", sc.Backfill.D())
	}
	if sc.OrphanAfter.D() != 30*time.Second {
		t.Errorf("orphan_after = %v", sc.OrphanAfter.D())
	}
	if len(sc.HideLevels) != 1 || sc.HideLevels[0] != "debug" {
		t.Errorf("hide_levels = %v", sc.HideLevels)
	}
	if cfg.IsBuiltin("agents") || !cfg.IsBuiltin("pi") {
		t.Error("only the registered pi source is builtin")
	}

	// A config entry named pi replaces the built-in registration.
	cfg, err = ParseConfig(`
[sources.pi]
type = "pi"
backfill = "1h"
`)
	if err != nil {
		t.Fatal(err)
	}
	pi := cfg.Sources["pi"]
	if cfg.IsBuiltin("pi") || pi.Backfill.D() != time.Hour || pi.OrphanAfter.D() != defaultOrphanAfter {
		t.Errorf("pi override wrong: %+v", pi)
	}
	if len(cfg.UserSourceNames()) != 1 {
		t.Errorf("user sources = %v", cfg.UserSourceNames())
	}

	// An ad hoc source named pi replaces the built-in one too.
	cfg = Defaults()
	if err := cfg.Normalise(); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsBuiltin("pi") || len(cfg.UserSourceNames()) != 0 {
		t.Fatalf("fresh config: builtin %v, user sources %v", cfg.IsBuiltin("pi"), cfg.UserSourceNames())
	}
	cfg.AddFileSource("pi", []string{"x.jsonl"})
	if cfg.IsBuiltin("pi") || len(cfg.UserSourceNames()) != 1 {
		t.Errorf("ad hoc pi should not be builtin: user sources %v", cfg.UserSourceNames())
	}
}

// TestStringListPresence: an absent key decodes to nil, an explicit empty
// array to a non-nil empty list. The defaults depend on the difference.
func TestStringListPresence(t *testing.T) {
	var c struct {
		Set    StringList `toml:"set"`
		Absent StringList `toml:"absent"`
	}
	if _, err := toml.Decode("set = []\n", &c); err != nil {
		t.Fatal(err)
	}
	if c.Set == nil || len(c.Set) != 0 {
		t.Errorf("set = []: got %#v, want a non-nil empty list", c.Set)
	}
	if c.Absent != nil {
		t.Errorf("absent key: got %#v, want nil", c.Absent)
	}
}

// TestConfigEmptyListsStayEmpty: an explicit empty list overrides the type
// default; an absent key still takes it.
func TestConfigEmptyListsStayEmpty(t *testing.T) {
	cfg, err := ParseConfig(`
[sources.agents]
type = "pi"
hide_levels = []

[sources.logs]
type = "axiom"
route_prefixes = []

[sources.logs.fields]
time = []
level = []
message = []

[sources.app]
type = "jsonl"
paths = "x.jsonl"

[sources.app.fields]
time = []
`)
	if err != nil {
		t.Fatal(err)
	}
	if hl := cfg.Sources["agents"].HideLevels; hl == nil || len(hl) != 0 {
		t.Errorf("pi hide_levels = []: got %#v", hl)
	}
	logs := cfg.Sources["logs"]
	if logs.RoutePrefixes == nil || len(logs.RoutePrefixes) != 0 {
		t.Errorf("axiom route_prefixes = []: got %#v", logs.RoutePrefixes)
	}
	f := logs.Fields
	if len(f.Time) != 0 || len(f.Level) != 0 || len(f.Message) != 0 {
		t.Errorf("axiom field lists should stay empty: %v %v %v", f.Time, f.Level, f.Message)
	}
	// NewAxiomClient re-applies the defaults; the empty list must survive
	// that too, so the Top Routes panel is off.
	a := &AxiomClient{name: "logs", dataset: logs.Dataset, sc: logs}
	a.sc.applyTypeDefaults("logs")
	if a.Info().Caps.Has(CapRoutes) {
		t.Error("route_prefixes = [] should drop CapRoutes")
	}
	app := cfg.Sources["app"].Fields
	if len(app.Time) != 0 || len(app.Level) != 3 {
		t.Errorf("jsonl: time %v should stay empty, level %v should default", app.Time, app.Level)
	}

	// Absent keys still get the defaults.
	cfg, err = ParseConfig("[sources.agents]\ntype = \"pi\"\n[sources.logs]\ntype = \"axiom\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if hl := cfg.Sources["agents"].HideLevels; len(hl) != 1 || hl[0] != "debug" {
		t.Errorf("pi hide_levels default = %v", hl)
	}
	logs = cfg.Sources["logs"]
	if len(logs.RoutePrefixes) != 5 || len(logs.Fields.Time) != 1 || len(logs.Fields.Message) != 2 {
		t.Errorf("axiom defaults = %v / %v / %v", logs.RoutePrefixes, logs.Fields.Time, logs.Fields.Message)
	}
	a = &AxiomClient{name: "logs", dataset: logs.Dataset, sc: logs}
	if !a.Info().Caps.Has(CapRoutes) {
		t.Error("default route prefixes should set CapRoutes")
	}
}

// TestConfigValidate: every rule Normalise enforces, one row each. The
// broken source is never the default one, so this also proves inactive
// sources are checked.
func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string // substrings the error must contain
	}{
		{"unknown type", "[sources.s]\ntype = \"kafka\"\n",
			[]string{`"s"`, `"kafka"`, "axiom, jsonl, pi"}},
		{"duration unit", "[sources.s]\ntype = \"jsonl\"\npaths = \"x\"\n[sources.s.fields]\nduration_unit = \"minutes\"\n",
			[]string{`"s"`, `"minutes"`}},
		{"max line bytes", "[sources.s]\ntype = \"jsonl\"\npaths = \"x\"\nmax_line_bytes = 2147483648\n",
			[]string{`"s"`, "max_line_bytes"}},
		{"refresh negative", "refresh_seconds = -1\n", []string{"refresh_seconds"}},
		{"poll negative", "stream_poll_ms = -5\n", []string{"stream_poll_ms"}},
		{"buffer negative", "log_buffer_size = -1\n", []string{"log_buffer_size"}},
		// Day and hour run together: on a single-digit day "2" swallows the
		// first hour digit and "03" is left with one.
		{"time format", "[sources.s]\ntype = \"jsonl\"\npaths = \"x\"\n[sources.s.fields]\ntime_format = \"2006-01-203:04 PM\"\n",
			[]string{`"s"`, "time_format"}},
		{"dataset quote", "[sources.s]\ntype = \"axiom\"\ndataset = \"logs'] | delete\"\n",
			[]string{`"s"`, "dataset"}},
		{"field bracket", "[sources.s]\ntype = \"axiom\"\n[sources.s.fields]\ngroup = \"fields.a]\"\n",
			[]string{`"s"`, "fields.group"}},
		{"duration newline", "[sources.s]\ntype = \"axiom\"\n[sources.s.fields]\nduration = \"a\\nb\"\n",
			[]string{`"s"`, "fields.duration"}},
		{"route prefix quote", "[sources.s]\ntype = \"axiom\"\nroute_prefixes = ['GET\" or true or \"']\n",
			[]string{`"s"`, "route_prefixes"}},
		{"legacy dataset backslash", "[datasets.'a\\b']\n",
			[]string{"dataset"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig("default_source = \"pi\"\n" + tc.text)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q should mention %s", err, w)
				}
			}
			if strings.HasPrefix(err.Error(), "config:") {
				t.Errorf("doubled prefix: config: %v", err)
			}
		})
	}
}

// TestConfigValidateAccepts: the values the rules must not reject.
func TestConfigValidateAccepts(t *testing.T) {
	_, err := ParseConfig(`
refresh_seconds = 0
stream_poll_ms = 0
log_buffer_size = 0

[sources.s]
type = "jsonl"
paths = "x"
max_line_bytes = 2147483647

[sources.s.fields]
duration_unit = "us"
time_format = "2006-01-02 15:04:05.000"

[sources.a]
type = "axiom"
dataset = "my-dataset.prod"
route_prefixes = ["GET /api", "POST"]

[sources.a.fields]
group = "fields.user-id"
duration = ["fields.durationMs", "fields.ttfb_ms"]
`)
	if err != nil {
		t.Fatal(err)
	}
}

// TestSourceTypes: the valid list in errors comes from the registry.
func TestSourceTypes(t *testing.T) {
	if got := strings.Join(sourceTypes(), ","); got != "axiom,jsonl,pi" {
		t.Errorf("sourceTypes = %s", got)
	}
	_, err := openSourceConfig("s", SourceConfig{Type: "kafka"}, OpenOptions{})
	if err == nil || !strings.Contains(err.Error(), "(valid: axiom, jsonl, pi)") {
		t.Errorf("open with unknown type: %v", err)
	}
}

// TestRegisterSourceType: one registration gives a type its place in the
// valid list, its defaults, its validation and its opener.
func TestRegisterSourceType(t *testing.T) {
	var opened string
	unregister := registerSourceType("fake", sourceType{
		open: func(name string, sc SourceConfig, _ OpenOptions) (Source, error) {
			opened = name + ":" + sc.Dataset
			return nil, nil
		},
		defaults: func(name string, sc *SourceConfig) {
			if sc.Dataset == "" {
				sc.Dataset = "d-" + name
			}
		},
		validate: func(name string, sc SourceConfig) error {
			if sc.Dataset == "bad" {
				return fmt.Errorf("source %q: bad dataset", name)
			}
			return nil
		},
	})
	if got := strings.Join(sourceTypes(), ","); got != "axiom,fake,jsonl,pi" {
		t.Errorf("sourceTypes = %s", got)
	}
	cfg, err := ParseConfig("[sources.f]\ntype = \"fake\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sources["f"].Dataset; got != "d-f" {
		t.Errorf("registered defaults not applied: dataset = %q", got)
	}
	if _, err := ParseConfig("[sources.f]\ntype = \"fake\"\ndataset = \"bad\"\n"); err == nil || !strings.Contains(err.Error(), "bad dataset") {
		t.Errorf("registered validation not run: %v", err)
	}
	if _, err := OpenSource(cfg, "f"); err != nil || opened != "f:d-f" {
		t.Errorf("registered opener: %q, %v", opened, err)
	}
	unregister()
	if got := strings.Join(sourceTypes(), ","); got != "axiom,jsonl,pi" {
		t.Errorf("after unregister sourceTypes = %s", got)
	}
}

// TestCapsFor: each mapped dimension sets exactly its capability, and an
// explicit empty list counts as unmapped.
func TestCapsFor(t *testing.T) {
	cases := []struct {
		name     string
		f        FieldsConfig
		prefixes []string
		want     Caps
	}{
		{"nothing", FieldsConfig{}, nil, 0},
		{"group", FieldsConfig{Group: "g"}, nil, CapGroup},
		{"cost", FieldsConfig{Cost: "c"}, nil, CapCost},
		{"cost group alone", FieldsConfig{CostGroup: "cg"}, nil, 0},
		{"duration", FieldsConfig{Duration: StringList{"d"}}, nil, CapDuration},
		{"empty duration", FieldsConfig{Duration: StringList{}}, nil, 0},
		{"routes", FieldsConfig{}, []string{"GET"}, CapRoutes},
		{"empty routes", FieldsConfig{}, []string{}, 0},
		{"everything", FieldsConfig{Group: "g", Cost: "c", Duration: StringList{"d"}}, []string{"GET"},
			CapGroup | CapCost | CapDuration | CapRoutes},
	}
	for _, tc := range cases {
		if got := capsFor(tc.f, tc.prefixes); got != tc.want {
			t.Errorf("%s: capsFor = %04b, want %04b", tc.name, got, tc.want)
		}
	}
}

func TestLoadEnvFile(t *testing.T) {
	if err := LoadEnvFile("/dev/null"); err != nil {
		t.Errorf("/dev/null: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.env")
	if err := LoadEnvFile(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing file: err = %v, want one naming %s", err, missing)
	}
}
