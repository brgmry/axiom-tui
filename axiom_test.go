package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
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
func aplCases(dataset string, ds DatasetConfig) map[string]string {
	lookback := time.Hour
	since := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	prefixes := ds.RoutePrefixes
	if len(prefixes) == 0 {
		prefixes = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	}
	return map[string]string{
		"stats":                aplStats(dataset, lookback, ds.ResolvedDurationFields()),
		"throughput":           aplThroughput(dataset, lookback),
		"throughput_segmented": aplThroughputSegmented(dataset, lookback, ds.GroupByField),
		"error_rate":           aplErrorRate(dataset, lookback),
		"top_errors":           aplTopErrors(dataset, lookback, 10),
		"top_routes":           aplTopRoutes(dataset, lookback, prefixes, 10),
		"recent_issues":        aplRecentIssues(dataset, lookback, 20),
		"top_client_health":    aplTopClientHealth(dataset, lookback, ds.GroupByField, 5),
		"cost":                 aplCost(dataset, lookback, "fields.costDollars", "fields.clientName"),
		"stream":               aplStream(dataset, since),
	}
}

func TestAxiomAPL(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(readmeConfig, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Resolve()
	name := cfg.DefaultDataset
	cases := aplCases(name, cfg.DatasetOrDefault(name))
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
