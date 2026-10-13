package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axiomhq/axiom-go/axiom/query"
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

// axiomResult builds a one-table query.Result from field names and rows, the
// way the SDK hands it back: column-major, one Column per field.
func axiomResult(names []string, data ...[]any) *query.Result {
	tbl := query.Table{Name: "0", Columns: make([]query.Column, len(names))}
	for _, n := range names {
		tbl.Fields = append(tbl.Fields, query.Field{Name: n})
	}
	for _, r := range data {
		for i := range names {
			tbl.Columns[i] = append(tbl.Columns[i], r[i])
		}
	}
	return &query.Result{Tables: []query.Table{tbl}}
}

func collectAxiomRows(r *query.Result) []map[string]any {
	var out []map[string]any
	for row := range rows(r) {
		out = append(out, row)
	}
	return out
}

func TestAxiomRows(t *testing.T) {
	if got := collectAxiomRows(nil); got != nil {
		t.Errorf("nil result: %v", got)
	}
	if got := collectAxiomRows(&query.Result{}); got != nil {
		t.Errorf("no tables: %v", got)
	}
	if got := collectAxiomRows(axiomResult([]string{"a"})); got != nil {
		t.Errorf("no rows: %v", got)
	}

	r := axiomResult([]string{"a", "b"}, []any{"x", 1.0}, []any{nil, 2.0})
	// Only the first table is read.
	r.Tables = append(r.Tables, axiomResult([]string{"z"}, []any{"ignored"}).Tables[0])
	got := collectAxiomRows(r)
	if len(got) != 2 {
		t.Fatalf("rows = %v", got)
	}
	if got[0]["a"] != "x" || got[0]["b"] != 1.0 {
		t.Errorf("row 0 = %v", got[0])
	}
	if v, ok := got[1]["a"]; !ok || v != nil {
		t.Errorf("nil value should stay a present key, row 1 = %v", got[1])
	}
	if _, ok := got[0]["z"]; ok {
		t.Error("second table leaked into rows")
	}

	// A column with no matching field is dropped rather than panicking.
	extra := axiomResult([]string{"a"}, []any{"x"})
	extra.Tables[0].Columns = append(extra.Tables[0].Columns, query.Column{"orphan"})
	if got := collectAxiomRows(extra); len(got) != 1 || len(got[0]) != 1 || got[0]["a"] != "x" {
		t.Errorf("orphan column: %v", got)
	}

	// Breaking out of the loop stops the iterator.
	n := 0
	for range rows(r) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("break yielded %d rows", n)
	}
}

func TestAxiomFirstRow(t *testing.T) {
	if row, ok := firstRow(nil); ok || row != nil {
		t.Errorf("nil result: %v, %v", row, ok)
	}
	if _, ok := firstRow(axiomResult([]string{"total"})); ok {
		t.Error("empty table should have no first row")
	}
	row, ok := firstRow(axiomResult([]string{"total"}, []any{3.0}, []any{4.0}))
	if !ok || row["total"] != 3.0 {
		t.Errorf("firstRow = %v, %v", row, ok)
	}
}

func TestAxiomSeriesFromTimeBin(t *testing.T) {
	s := seriesFromTimeBin(nil, "requests", "ev/min")
	if s.Name != "ev/min" || s.Points == nil || len(s.Points) != 0 {
		t.Errorf("empty = %+v, want a named series with no points", s)
	}

	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	r := axiomResult([]string{"_time", "requests"},
		[]any{t0.Format(time.RFC3339), 4.0},
		[]any{t0.Add(time.Minute).Format(time.RFC3339Nano), nil},
		[]any{"not a time", "7"},
	)
	s = seriesFromTimeBin(r, "requests", "ev/min")
	if len(s.Points) != 3 {
		t.Fatalf("points = %+v", s.Points)
	}
	if !s.Points[0].Time.Equal(t0) || s.Points[0].Value != 4 {
		t.Errorf("point 0 = %+v", s.Points[0])
	}
	if !s.Points[1].Time.Equal(t0.Add(time.Minute)) || s.Points[1].Value != 0 {
		t.Errorf("nil value should map to 0: %+v", s.Points[1])
	}
	if !s.Points[2].Time.IsZero() || s.Points[2].Value != 7 {
		t.Errorf("bad time and string number: %+v", s.Points[2])
	}

	// Missing value column: every point is 0, times still mapped.
	s = seriesFromTimeBin(r, "errors", "Errors")
	if len(s.Points) != 3 || !s.Points[0].Time.Equal(t0) {
		t.Fatalf("missing column = %+v", s.Points)
	}
	for _, p := range s.Points {
		if p.Value != 0 {
			t.Errorf("missing column gave %v", p.Value)
		}
	}
}

func TestAxiomTopSeriesFromBin(t *testing.T) {
	if got := topSeriesFromBin(nil, "g", "requests", 3); len(got) != 0 {
		t.Errorf("empty = %+v", got)
	}

	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ts := func(m int) string { return t0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	// Rows out of time order; c appears only in the later bucket, and one
	// row has a nil group.
	r := axiomResult([]string{"_time", "g", "requests"},
		[]any{ts(1), "a", 10.0},
		[]any{ts(0), "a", 5.0},
		[]any{ts(0), "b", 8.0},
		[]any{ts(1), "c", 2.0},
		[]any{ts(0), nil, 1.0},
	)

	got := topSeriesFromBin(r, "g", "requests", 2)
	if len(got) != 3 {
		t.Fatalf("series = %+v", got)
	}
	if got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "Other" {
		t.Fatalf("names = %s %s %s, want a b Other", got[0].Name, got[1].Name, got[2].Name)
	}
	for _, s := range got {
		if len(s.Points) != 2 || !s.Points[0].Time.Equal(t0) || !s.Points[1].Time.Equal(t0.Add(time.Minute)) {
			t.Errorf("%s: x axis not shared and ascending: %+v", s.Name, s.Points)
		}
	}
	if got[0].Points[0].Value != 5 || got[0].Points[1].Value != 10 {
		t.Errorf("a = %+v", got[0].Points)
	}
	if got[1].Points[0].Value != 8 || got[1].Points[1].Value != 0 {
		t.Errorf("b missing bucket should be 0: %+v", got[1].Points)
	}
	// Other folds c (2 at t1) and the nil group (1 at t0).
	if got[2].Points[0].Value != 1 || got[2].Points[1].Value != 2 {
		t.Errorf("Other = %+v", got[2].Points)
	}

	// topN covering every group: no Other, and the nil group is "(none)".
	got = topSeriesFromBin(r, "g", "requests", 10)
	if len(got) != 4 {
		t.Fatalf("series = %+v", got)
	}
	for _, s := range got {
		if s.Name == "Other" {
			t.Error("Other present with no tail")
		}
	}
	if got[3].Name != "(none)" {
		t.Errorf("smallest series = %q, want (none)", got[3].Name)
	}

	// topN of 0 folds everything into Other.
	got = topSeriesFromBin(r, "g", "requests", 0)
	if len(got) != 1 || got[0].Name != "Other" || got[0].Points[0].Value != 14 || got[0].Points[1].Value != 12 {
		t.Errorf("topN 0 = %+v", got)
	}
}

// TestAxiomEventFromRowEdges: missing and nil columns leave zero values and
// stay out of Fields, the first parseable time key wins, and the first
// non-empty message key wins.
func TestAxiomEventFromRowEdges(t *testing.T) {
	a := &AxiomClient{sc: SourceConfig{Fields: FieldsConfig{
		Time:        StringList{"ts", "_time"},
		Level:       StringList{"level"},
		Message:     StringList{"message", "msg"},
		Group:       "client",
		Interesting: StringList{"route", "user"},
		Provider:    "provider",
	}}}

	ev := a.eventFromRow(map[string]any{})
	if ev.Level != "" || ev.Message != "" || ev.Client != "" || !ev.Time.IsZero() || len(ev.Fields) != 0 {
		t.Errorf("empty row = %+v", ev)
	}

	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	row := map[string]any{
		"ts":       "garbage",
		"_time":    t0.Format(time.RFC3339Nano),
		"level":    "Warn",
		"message":  "",
		"msg":      "fallback",
		"client":   nil,
		"route":    "",
		"user":     nil,
		"provider": "anthropic",
	}
	ev = a.eventFromRow(row)
	if !ev.Time.Equal(t0) {
		t.Errorf("time = %v, want the _time fallback", ev.Time)
	}
	if ev.Level != "warn" || ev.Message != "fallback" || ev.Client != "" {
		t.Errorf("mapping = %+v", ev)
	}
	if len(ev.Fields) != 1 || ev.Fields["provider"] != "anthropic" {
		t.Errorf("Fields = %v, want only provider", ev.Fields)
	}
	if len(ev.Raw) != len(row) {
		t.Errorf("Raw should be the full row: %v", ev.Raw)
	}
}
