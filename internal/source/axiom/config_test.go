package axiom

import (
	"strings"
	"testing"

	"github.com/brgmry/tailboard/internal/source"
)

func TestConfigLegacy(t *testing.T) {
	cfg, err := source.ParseConfig(readmeConfig)
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
	cfg, err := source.ParseConfig(`
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
	_, err := source.ParseConfig(`
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

// TestConfigEmptyListsStayEmpty: an explicit empty list overrides the type
// default; an absent key still takes it.
func TestConfigEmptyListsStayEmpty(t *testing.T) {
	cfg, err := source.ParseConfig(`
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
	// newClient re-applies the defaults; the empty list must survive
	// that too, so the Top Routes panel is off.
	a := &client{name: "logs", dataset: logs.Dataset, sc: logs}
	a.sc.ApplyTypeDefaults("logs")
	if a.Info().Caps.Has(source.CapRoutes) {
		t.Error("route_prefixes = [] should drop CapRoutes")
	}
	app := cfg.Sources["app"].Fields
	if len(app.Time) != 0 || len(app.Level) != 3 {
		t.Errorf("jsonl: time %v should stay empty, level %v should default", app.Time, app.Level)
	}

	// Absent keys still get the defaults.
	cfg, err = source.ParseConfig("[sources.agents]\ntype = \"pi\"\n[sources.logs]\ntype = \"axiom\"\n")
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
	a = &client{name: "logs", dataset: logs.Dataset, sc: logs}
	if !a.Info().Caps.Has(source.CapRoutes) {
		t.Error("default route prefixes should set CapRoutes")
	}
}

// TestConfigValidateAxiom: the rules the axiom backend adds, one row each,
// and the valid list in an unknown type error naming it.
func TestConfigValidateAxiom(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string // substrings the error must contain
	}{
		{"unknown type", "[sources.s]\ntype = \"kafka\"\n",
			[]string{`"s"`, `"kafka"`, "(valid: axiom, jsonl, pi)"}},
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
			_, err := source.ParseConfig("default_source = \"pi\"\n" + tc.text)
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

// TestConfigValidateAcceptsAxiom: the axiom values the rules must not
// reject.
func TestConfigValidateAcceptsAxiom(t *testing.T) {
	_, err := source.ParseConfig(`
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
