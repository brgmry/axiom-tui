package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
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

	// Ad hoc dataset: no group configured, clientName fallback still colours.
	var bare Config = Defaults()
	bare.Normalise()
	bare.AddAxiomSource("bare")
	b := &AxiomClient{name: "bare", dataset: "bare", sc: bare.Sources["bare"]}
	ev = b.eventFromRow(map[string]any{"message": "m", "fields.clientName": "acme"})
	if ev.Client != "acme" || ev.Message != "m" {
		t.Errorf("fallback mapping wrong: %+v", ev)
	}
	info := b.Info()
	if info.Caps&CapGroup != 0 || info.Caps&CapCost == 0 || info.Caps&CapRoutes == 0 {
		t.Errorf("caps = %b", info.Caps)
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
