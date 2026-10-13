package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files")

// readmeConfig is the example from the README. The golden APL strings are
// captured for exactly this dataset so a refactor of the query builders
// cannot drift without the diff showing up here.
const readmeConfig = `
default_dataset = "my-dataset"

[datasets.my-dataset]
interesting_fields = ["fields.userId", "fields.route", "fields.requestId"]
group_by_field = "fields.userId"
duration_field = "fields.durationMs"
route_prefixes = ["GET", "POST", "PUT", "DELETE", "PATCH"]
`

// aplCases builds every APL string for the README dataset with fixed
// arguments. Shared by the golden test and, later, by the legacy mapping test.
func aplCases(sc SourceConfig) map[string]string {
	dataset := sc.Dataset
	f := sc.Fields
	lookback := time.Hour
	since := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	return map[string]string{
		"stats":                aplStats(dataset, lookback, f.Duration),
		"throughput":           aplThroughput(dataset, lookback),
		"throughput_segmented": aplThroughputSegmented(dataset, lookback, f.Group),
		"error_rate":           aplErrorRate(dataset, lookback),
		"top_errors":           aplTopErrors(dataset, lookback, 10),
		"top_routes":           aplTopRoutes(dataset, lookback, sc.RoutePrefixes, 10),
		"recent_issues":        aplRecentIssues(dataset, lookback, 20),
		"top_client_health":    aplTopClientHealth(dataset, lookback, f.Group, 5),
		"cost":                 aplCost(dataset, lookback, f.Cost, f.CostGroup),
		"stream":               aplStream(dataset, since),
	}
}

func TestAxiomAPL(t *testing.T) {
	cfg, err := ParseConfig(readmeConfig)
	if err != nil {
		t.Fatal(err)
	}
	cases := aplCases(cfg.Sources[cfg.DefaultSourceName()])
	if len(cases) != 10 {
		t.Fatalf("expected 10 cases, got %d", len(cases))
	}
	for key, apl := range cases {
		path := filepath.Join("testdata", "apl", key+".golden")
		if *updateGolden {
			if err := os.WriteFile(path, []byte(apl), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update to create)", key, err)
		}
		if string(want) != apl {
			t.Errorf("%s: APL drifted from golden\n--- want\n%s\n--- got\n%s", key, want, apl)
		}
	}
}

func TestAxiomEventFromRow(t *testing.T) {
	cfg, err := ParseConfig(readmeConfig)
	if err != nil {
		t.Fatal(err)
	}
	a := &AxiomClient{name: "my-dataset", dataset: "my-dataset", sc: cfg.Sources["my-dataset"]}
	row := map[string]any{
		"_time":            "2026-10-12T09:00:00Z",
		"level":            "ERROR",
		"msg":              "boom",
		"fields.userId":    "u1",
		"fields.route":     "/x",
		"fields.provider":  "openai",
		"fields.requestId": "req-1",
		"fields.other":     "hidden",
	}
	ev := a.eventFromRow(row)
	if ev.Level != "error" || ev.Message != "boom" || ev.Client != "u1" {
		t.Errorf("basic mapping wrong: %+v", ev)
	}
	if ev.Time.IsZero() {
		t.Error("time not parsed")
	}
	if ev.Fields["fields.userId"] != "u1" || ev.Fields["fields.route"] != "/x" {
		t.Errorf("interesting fields missing: %v", ev.Fields)
	}
	if ev.Fields["fields.provider"] != "openai" || ev.Fields["fields.requestId"] != "req-1" {
		t.Errorf("filter fields missing: %v", ev.Fields)
	}
	if _, ok := ev.Fields["fields.other"]; ok {
		t.Error("unlisted field leaked into Fields")
	}

	// Ad hoc dataset: no group configured, so no client, even when the row
	// has a clientName. The legacy cost mapping still applies.
	var bare Config = Defaults()
	bare.Normalise()
	bare.AddAxiomSource("bare")
	b := &AxiomClient{name: "bare", dataset: "bare", sc: bare.Sources["bare"]}
	ev = b.eventFromRow(map[string]any{"message": "m", "fields.clientName": "acme"})
	if ev.Client != "" || ev.Message != "m" {
		t.Errorf("ungrouped mapping wrong: %+v", ev)
	}
	info := b.Info()
	if info.Caps&CapGroup != 0 || info.Caps&CapCost == 0 || info.Caps&CapRoutes == 0 {
		t.Errorf("caps = %b", info.Caps)
	}
	if info.Filters.Trace != "fields.requestId" {
		t.Errorf("ad hoc dataset lost the legacy trace key: %+v", info.Filters)
	}

	// Legacy single duration_field falls back into the duration list.
	legacy, err := ParseConfig("[datasets.d]\nduration_field = \"fields.latencyMs\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if d := legacy.Sources["d"].Fields.Duration; len(d) != 1 || d[0] != "fields.latencyMs" {
		t.Errorf("legacy duration_field = %v", d)
	}
}

// TestAxiomSourceNoAuthorSchema: a [sources.<name>] axiom source maps only
// what its config says. No cost, cost group, filter or trace key is
// assumed, so a stranger's dataset gets no Cost panel.
func TestAxiomSourceNoAuthorSchema(t *testing.T) {
	cfg, err := ParseConfig("[sources.x]\ntype = \"axiom\"\n")
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["x"]
	f := sc.Fields
	if f.Cost != "" || f.CostGroup != "" || f.Provider != "" || f.Operation != "" || f.Trace != "" {
		t.Errorf("author schema leaked into a plain axiom source: %+v", f)
	}
	if len(f.Time) != 1 || f.DisplayPrefix != "fields." || len(sc.RoutePrefixes) != 5 {
		t.Errorf("generic axiom defaults missing: %+v", sc)
	}
	a := &AxiomClient{name: "x", dataset: sc.Dataset, sc: sc}
	info := a.Info()
	if info.Caps.Has(CapCost) {
		t.Errorf("caps = %b, want no CapCost", info.Caps)
	}
	if info.Filters != (FilterFields{}) {
		t.Errorf("filters = %+v, want none", info.Filters)
	}
	if apl := aplCost("x", time.Hour, "fields.c", ""); strings.Contains(apl, " by ") {
		t.Errorf("cost without a group should not group: %s", apl)
	}
}

// TestAxiomHydrateReturnsRaw: Hydrate on an Axiom event returns the raw
// row it was built from, exactly as stored, including when it is nil.
func TestAxiomHydrateReturnsRaw(t *testing.T) {
	c := &AxiomClient{}
	raw := map[string]any{"message": "m", "fields.x": 1}
	ev := LogEvent{Message: "m", Fields: map[string]any{"fields.x": 1}, Raw: raw}
	got, err := c.Hydrate(context.Background(), ev)
	if err != nil || len(got) != 2 || got["message"] != "m" {
		t.Fatalf("Hydrate = %v, %v", got, err)
	}
	ev.Raw = nil
	got, err = c.Hydrate(context.Background(), ev)
	if err != nil || got != nil {
		t.Errorf("Hydrate without Raw should return Raw (nil), got %v, %v", got, err)
	}
}

// TestAxiomRejectsQuotedPrefix: a route prefix with a double quote fails
// validation, at load and at open, before any APL is built or any client
// (or token) is touched.
func TestAxiomRejectsQuotedPrefix(t *testing.T) {
	for _, k := range []string{"AXIOM_PAT", "AXIOM_PERSONAL_TOKEN", "AXIOM_TOKEN", "AXIOM_API_TOKEN"} {
		t.Setenv(k, "")
	}
	bad := `GET" or true or "`

	_, err := ParseConfig("[sources.a]\ntype = \"axiom\"\nroute_prefixes = ['" + bad + "']\n")
	if err == nil || !strings.Contains(err.Error(), "route_prefixes") {
		t.Fatalf("load: expected a route_prefixes error, got %v", err)
	}

	sc := SourceConfig{Type: "axiom", RoutePrefixes: StringList{bad}}
	_, err = openSourceConfig("a", sc, OpenOptions{})
	if err == nil || !strings.Contains(err.Error(), "route_prefixes") {
		t.Fatalf("open: expected a route_prefixes error before the token check, got %v", err)
	}
}

// TestAPLEscaping: the builders escape what validation would reject, and
// leave everything else alone.
func TestAPLEscaping(t *testing.T) {
	if got := aplName(`a'b\c`); got != `a\'b\\c` {
		t.Errorf("aplName = %s", got)
	}
	if got := aplString(`a"b\c`); got != `a\"b\\c` {
		t.Errorf("aplString = %s", got)
	}
	for _, s := range []string{"my-dataset", "fields.durationMs", "GET", "fields.user id"} {
		if aplName(s) != s || aplString(s) != s {
			t.Errorf("%q should pass through unchanged", s)
		}
	}
	apl := aplTopRoutes("d", time.Hour, []string{`GET"`}, 1)
	if !strings.Contains(apl, `message startswith "GET\""`) {
		t.Errorf("prefix not escaped: %s", apl)
	}
}
