package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axiomhq/axiom-go/axiom"
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

// fakeAxiom is a local stand-in for the Axiom query endpoint. It records
// the APL of every request and answers with whatever body is queued.
type fakeAxiom struct {
	t      *testing.T
	mu     sync.Mutex
	apls   []string
	status int
	body   []byte
}

func (f *fakeAxiom) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/datasets/_apl" || r.URL.Query().Get("format") != "tabular" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer xapt-test" {
		f.t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("X-Axiom-Org-Id"); got != "org-test" {
		f.t.Errorf("X-Axiom-Org-Id = %q", got)
	}
	var req struct {
		APL string `json:"apl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("request body: %v", err)
	}
	f.mu.Lock()
	f.apls = append(f.apls, req.APL)
	status, body := f.status, f.body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// reply queues a response for the next request.
func (f *fakeAxiom) reply(status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// replyResult queues a 200 carrying r's tables and a status as JSON. Real
// replies always carry "status", and decoding it is what crashed axiom-go
// before v0.38.0 under Go 1.27's json/v2.
func (f *fakeAxiom) replyResult(r *query.Result) {
	b, err := json.Marshal(struct {
		Tables []query.Table   `json:"tables"`
		Status json.RawMessage `json:"status"`
	}{r.Tables, json.RawMessage(`{"elapsedTime":1,"rowsExamined":1,"rowsMatched":1}`)})
	if err != nil {
		f.t.Fatal(err)
	}
	f.reply(http.StatusOK, b)
}

// lastAPL returns the APL of the most recent request, "" if none.
func (f *fakeAxiom) lastAPL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.apls) == 0 {
		return ""
	}
	return f.apls[len(f.apls)-1]
}

// requests returns how many queries reached the server.
func (f *fakeAxiom) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.apls)
}

// clearAxiomEnv blanks every env var NewAxiomClient or the SDK reads.
func clearAxiomEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AXIOM_PAT", "AXIOM_PERSONAL_TOKEN", "AXIOM_TOKEN", "AXIOM_API_TOKEN", "AXIOM_ORG_ID", "AXIOM_URL"} {
		t.Setenv(k, "")
	}
}

// newFakeAxiomClient opens the README dataset against a local server with a
// test PAT. Retries are off so an error reply fails fast.
func newFakeAxiomClient(t *testing.T) (*AxiomClient, *fakeAxiom) {
	t.Helper()
	clearAxiomEnv(t)
	t.Setenv("AXIOM_PAT", "xapt-test")
	t.Setenv("AXIOM_ORG_ID", "org-test")

	fake := &fakeAxiom{t: t, status: http.StatusOK, body: []byte(`{"tables":[]}`)}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	cfg, err := ParseConfig(readmeConfig)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAxiomClient("my-dataset", cfg.Sources["my-dataset"], axiom.SetURL(srv.URL), axiom.SetNoRetry())
	if err != nil {
		t.Fatal(err)
	}
	return a, fake
}

// goldenAPL reads a pinned query from testdata/apl.
func goldenAPL(t *testing.T, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "apl", key+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readAxiomFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "axiom", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAxiomStatsRoundTrip: Stats sends the pinned APL over HTTP and maps
// the SDK's decoded reply, duration slot 0 mirrored into the scalars.
func TestAxiomStatsRoundTrip(t *testing.T) {
	a, fake := newFakeAxiomClient(t)
	ctx := context.Background()

	fake.reply(http.StatusOK, readAxiomFixture(t, "stats.json"))
	s, err := a.Stats(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.lastAPL(), goldenAPL(t, "stats"); got != want {
		t.Errorf("APL sent\n--- want\n%s\n--- got\n%s", want, got)
	}
	if s.Total != 1200 || s.Error != 7 || s.Warn != 33 || s.Info != 1160 {
		t.Errorf("counts = %+v", s)
	}
	if len(s.Durations) != 1 {
		t.Fatalf("durations = %+v", s.Durations)
	}
	want := DurationStat{Field: "fields.durationMs", Avg: 41.5, P95: 180.25, Max: 2400}
	if s.Durations[0] != want {
		t.Errorf("duration = %+v, want %+v", s.Durations[0], want)
	}
	if s.AvgDur != 41.5 || s.P95Dur != 180.25 || s.MaxDur != 2400 {
		t.Errorf("legacy scalars = %v %v %v", s.AvgDur, s.P95Dur, s.MaxDur)
	}

	// An empty table is zero stats, not an error.
	fake.reply(http.StatusOK, []byte(`{"tables":[]}`))
	s, err = a.Stats(ctx, time.Hour)
	if err != nil || s.Total != 0 || s.Durations != nil {
		t.Errorf("empty = %+v, %v", s, err)
	}
}

// TestAxiomStreamRoundTrip: Stream sends the pinned APL for its cursor,
// maps every row to an event, and advances the cursor to the newest time
// even when rows arrive out of order.
func TestAxiomStreamRoundTrip(t *testing.T) {
	a, fake := newFakeAxiomClient(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	fake.reply(http.StatusOK, readAxiomFixture(t, "stream.json"))
	evs, next, err := a.Stream(ctx, Cursor{Time: since}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.lastAPL(), goldenAPL(t, "stream"); got != want {
		t.Errorf("APL sent\n--- want\n%s\n--- got\n%s", want, got)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if want := since.Add(3500 * time.Millisecond); !next.Time.Equal(want) {
		t.Errorf("cursor = %v, want %v", next.Time, want)
	}

	e0, e1, e2 := evs[0], evs[1], evs[2]
	if !e0.Time.Equal(since.Add(time.Second)) || e0.Level != "info" || e0.Message != "GET /a 200" || e0.Client != "u1" {
		t.Errorf("event 0 = %+v", e0)
	}
	if len(e0.Fields) != 2 || e0.Fields["fields.userId"] != "u1" || e0.Fields["fields.route"] != "/a" {
		t.Errorf("event 0 fields = %v", e0.Fields)
	}
	if e1.Level != "error" || e1.Message != "boom" || e1.Client != "u2" || e1.Fields["fields.requestId"] != "req-9" {
		t.Errorf("event 1 = %+v", e1)
	}
	if e2.Level != "warn" || e2.Message != "slow" || e2.Client != "" || len(e2.Fields) != 0 {
		t.Errorf("event 2 = %+v", e2)
	}
	if e2.Raw["msg"] != "slow" || len(e2.Raw) != 7 {
		t.Errorf("event 2 raw = %v", e2.Raw)
	}

	// An empty batch keeps the cursor where it was.
	fake.reply(http.StatusOK, []byte(`{"tables":[]}`))
	evs, next, err = a.Stream(ctx, Cursor{Time: since}, 100)
	if err != nil || len(evs) != 0 || !next.Time.Equal(since) {
		t.Errorf("empty = %v, %v, %v", evs, next, err)
	}

	// A zero cursor starts a few seconds back from now.
	before := time.Now()
	_, next, err = a.Stream(ctx, Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if lo, hi := before.Add(-6*time.Second), time.Now(); next.Time.Before(lo) || next.Time.After(hi) {
		t.Errorf("zero cursor started at %v, want within [%v, %v]", next.Time, lo, hi)
	}
	if apl := fake.lastAPL(); !strings.Contains(apl, `where _time > datetime("`) || strings.Contains(apl, "0001-01-01") {
		t.Errorf("zero cursor APL = %s", apl)
	}
}

// TestAxiomQueriesRoundTrip: every other query method sends its pinned APL
// and maps a built reply.
func TestAxiomQueriesRoundTrip(t *testing.T) {
	a, fake := newFakeAxiomClient(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ts := func(m int) string { return t0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }

	sent := func(key string) {
		t.Helper()
		if got, want := fake.lastAPL(), goldenAPL(t, key); got != want {
			t.Errorf("%s: APL sent\n--- want\n%s\n--- got\n%s", key, want, got)
		}
	}

	fake.replyResult(axiomResult([]string{"_time", "requests"}, []any{ts(0), 3}, []any{ts(1), 5}))
	tp, err := a.Throughput(ctx, time.Hour)
	if err != nil || tp.Name != "ev/min" || len(tp.Points) != 2 || tp.Points[1].Value != 5 || !tp.Points[1].Time.Equal(t0.Add(time.Minute)) {
		t.Errorf("Throughput = %+v, %v", tp, err)
	}
	sent("throughput")

	fake.replyResult(axiomResult([]string{"_time", "fields.userId", "requests"},
		[]any{ts(0), "u1", 9}, []any{ts(0), "u2", 4}, []any{ts(1), "u3", 1}))
	seg, err := a.ThroughputSegmented(ctx, 1, time.Hour)
	if err != nil || len(seg) != 2 || seg[0].Name != "u1" || seg[1].Name != "Other" || seg[1].Points[0].Value != 4 || seg[1].Points[1].Value != 1 {
		t.Errorf("ThroughputSegmented = %+v, %v", seg, err)
	}
	sent("throughput_segmented")

	fake.replyResult(axiomResult([]string{"_time", "errors", "warns"}, []any{ts(0), 2, 6}))
	er, err := a.ErrorRate(ctx, time.Hour)
	if err != nil || len(er) != 2 || er[0].Name != "Errors" || er[0].Points[0].Value != 2 || er[1].Name != "Warns" || er[1].Points[0].Value != 6 {
		t.Errorf("ErrorRate = %+v, %v", er, err)
	}
	sent("error_rate")

	fake.replyResult(axiomResult([]string{"message", "level", "count"}, []any{"boom", "error", 12}, []any{"slow", "warn", 3}))
	te, err := a.TopErrors(ctx, 10, time.Hour)
	if err != nil || len(te) != 2 || te[0] != (TableRow{Count: 12, Level: "error", Message: "boom"}) || te[1].Count != 3 {
		t.Errorf("TopErrors = %+v, %v", te, err)
	}
	sent("top_errors")

	fake.replyResult(axiomResult([]string{"message", "count"}, []any{"GET /a", 40}))
	tr, err := a.TopRoutes(ctx, 10, time.Hour)
	if err != nil || len(tr) != 1 || tr[0] != (TableRow{Count: 40, Message: "GET /a"}) {
		t.Errorf("TopRoutes = %+v, %v", tr, err)
	}
	sent("top_routes")

	fake.replyResult(axiomResult([]string{"_time", "level", "message", "fields.userId"}, []any{ts(2), "warn", "slow", "u7"}))
	ri, err := a.RecentIssues(ctx, 20, time.Hour)
	if err != nil || len(ri) != 1 || ri[0].Level != "warn" || ri[0].Client != "u7" || !ri[0].Time.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("RecentIssues = %+v, %v", ri, err)
	}
	sent("recent_issues")

	fake.replyResult(axiomResult([]string{"fields.userId", "errors", "warns", "total"},
		[]any{"u1", 4, 1, 5}, []any{nil, 9, 9, 18}, []any{"u2", 0, 2, 2}))
	ch, err := a.TopClientHealth(ctx, 5, time.Hour)
	if err != nil || len(ch) != 2 || ch[0] != (ClientHealth{Client: "u1", Errors: 4, Warns: 1}) || ch[1].Client != "u2" {
		t.Errorf("TopClientHealth = %+v, %v (a nil client row must be skipped)", ch, err)
	}
	sent("top_client_health")

	fake.replyResult(axiomResult([]string{"fields.clientName", "dollars"},
		[]any{"acme", 2.5}, []any{nil, 0.5}, []any{"zero", 0}, []any{"neg", -1}))
	cost, err := a.Cost(ctx, time.Hour)
	if err != nil || cost.Total != 3 || len(cost.ByGroup) != 2 ||
		cost.ByGroup[0] != (ClientCost{Client: "acme", Dollars: 2.5}) || cost.ByGroup[1] != (ClientCost{Client: "(unknown)", Dollars: 0.5}) {
		t.Errorf("Cost = %+v, %v", cost, err)
	}
	sent("cost")
}

// TestAxiomUnconfiguredSkipsQuery: methods whose field is not configured
// return empty without a request.
func TestAxiomUnconfiguredSkipsQuery(t *testing.T) {
	a, fake := newFakeAxiomClient(t)
	ctx := context.Background()
	a.sc.Fields.Group = ""
	a.sc.Fields.Cost = ""
	a.sc.RoutePrefixes = nil

	if s, err := a.ThroughputSegmented(ctx, 3, time.Hour); s != nil || err != nil {
		t.Errorf("ThroughputSegmented = %v, %v", s, err)
	}
	if r, err := a.TopRoutes(ctx, 3, time.Hour); r != nil || err != nil {
		t.Errorf("TopRoutes = %v, %v", r, err)
	}
	if h, err := a.TopClientHealth(ctx, 3, time.Hour); h != nil || err != nil {
		t.Errorf("TopClientHealth = %v, %v", h, err)
	}
	if c, err := a.Cost(ctx, time.Hour); c.ByGroup != nil || c.Total != 0 || err != nil {
		t.Errorf("Cost = %+v, %v", c, err)
	}
	if n := fake.requests(); n != 0 {
		t.Errorf("%d requests sent for unconfigured panels", n)
	}

	// Cost with neither cost_group nor group sums the whole window into
	// one (unknown) row; with only group it groups by that.
	a.sc.Fields.Cost = "fields.c"
	a.sc.Fields.CostGroup = ""
	fake.replyResult(axiomResult([]string{"dollars"}, []any{1.25}))
	c, err := a.Cost(ctx, time.Hour)
	if err != nil || c.Total != 1.25 || len(c.ByGroup) != 1 || c.ByGroup[0].Client != "(unknown)" {
		t.Errorf("ungrouped Cost = %+v, %v", c, err)
	}
	if apl := fake.lastAPL(); strings.Contains(apl, " by ") {
		t.Errorf("ungrouped cost APL grouped: %s", apl)
	}
	a.sc.Fields.Group = "fields.team"
	fake.replyResult(axiomResult([]string{"fields.team", "dollars"}, []any{"core", 2.0}))
	c, err = a.Cost(ctx, time.Hour)
	if err != nil || len(c.ByGroup) != 1 || c.ByGroup[0].Client != "core" {
		t.Errorf("group fallback Cost = %+v, %v", c, err)
	}
	if apl := fake.lastAPL(); !strings.Contains(apl, "by ['fields.team']") {
		t.Errorf("cost did not fall back to the group field: %s", apl)
	}
}

// TestAxiomQueryErrors: an HTTP error from Axiom surfaces from every query
// method, and Stream hands the input cursor back unchanged.
func TestAxiomQueryErrors(t *testing.T) {
	a, fake := newFakeAxiomClient(t)
	ctx := context.Background()
	fake.reply(http.StatusBadRequest, []byte(`{"code":400,"message":"bad apl"}`))

	check := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "bad apl") {
			t.Errorf("%s: err = %v, want the server message", name, err)
		}
	}
	_, err := a.Stats(ctx, time.Hour)
	check("Stats", err)
	_, err = a.Throughput(ctx, time.Hour)
	check("Throughput", err)
	_, err = a.ThroughputSegmented(ctx, 3, time.Hour)
	check("ThroughputSegmented", err)
	_, err = a.ErrorRate(ctx, time.Hour)
	check("ErrorRate", err)
	_, err = a.TopErrors(ctx, 3, time.Hour)
	check("TopErrors", err)
	_, err = a.TopRoutes(ctx, 3, time.Hour)
	check("TopRoutes", err)
	_, err = a.RecentIssues(ctx, 3, time.Hour)
	check("RecentIssues", err)
	_, err = a.TopClientHealth(ctx, 3, time.Hour)
	check("TopClientHealth", err)
	_, err = a.Cost(ctx, time.Hour)
	check("Cost", err)

	cur := Cursor{Time: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	evs, next, err := a.Stream(ctx, cur, 10)
	check("Stream", err)
	if evs != nil || next != cur {
		t.Errorf("Stream on error = %v, %v, want nil and the input cursor", evs, next)
	}
}

// TestNewAxiomClientEnv: token and org selection from the environment,
// without touching the network.
func TestNewAxiomClientEnv(t *testing.T) {
	sc := SourceConfig{Type: "axiom"}
	open := func(env map[string]string) (*AxiomClient, error) {
		t.Helper()
		clearAxiomEnv(t)
		for k, v := range env {
			t.Setenv(k, v)
		}
		return NewAxiomClient("ds", sc)
	}

	if _, err := open(nil); err == nil || !strings.Contains(err.Error(), "AXIOM_PAT") {
		t.Errorf("no token: err = %v", err)
	}
	if _, err := open(map[string]string{"AXIOM_PAT": "xapt-x"}); err == nil || !strings.Contains(err.Error(), "AXIOM_ORG_ID") {
		t.Errorf("PAT without org: err = %v", err)
	}
	if _, err := open(map[string]string{"AXIOM_TOKEN": "nope", "AXIOM_ORG_ID": "o"}); err == nil || !strings.Contains(err.Error(), "axiom client") {
		t.Errorf("unrecognised token: err = %v", err)
	}

	// An API token needs no org; the dataset defaults to the source name.
	a, err := open(map[string]string{"AXIOM_API_TOKEN": "xaat-x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.dataset != "ds" || a.name != "ds" || a.Close() != nil {
		t.Errorf("client = %+v", a)
	}

	// A PAT wins over an API token when both are set.
	if _, err := open(map[string]string{"AXIOM_PERSONAL_TOKEN": "xapt-x", "AXIOM_TOKEN": "xaat-x"}); err == nil || !strings.Contains(err.Error(), "AXIOM_ORG_ID") {
		t.Errorf("PAT should take precedence and demand an org, err = %v", err)
	}
	if _, err := open(map[string]string{"AXIOM_PAT": "xapt-x", "AXIOM_ORG_ID": "o"}); err != nil {
		t.Errorf("PAT with org: %v", err)
	}
}
