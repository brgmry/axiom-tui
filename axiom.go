package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/axiomhq/axiom-go/axiom"
	"github.com/axiomhq/axiom-go/axiom/query"
)

// AxiomClient is a thin wrapper over axiom-go sized for our query shapes.
// The SDK's Query returns a generic *query.Result — everything here flattens
// that into structs the UI layer can consume directly.
type AxiomClient struct {
	c       *axiom.Client
	dataset string
	ds      DatasetConfig
}

// NewAxiomClient builds a client from env vars.
//
// Token selection — prefer Personal Access Tokens (`xapt-…`) over API tokens
// (`xaat-…`). API tokens are scoped per-dataset and 403 on query endpoints
// unless the Query permission is explicitly granted; PATs carry full user
// perms and Just Work. When both env vars are present, PAT wins.
//
// Org ID is required for PATs. API tokens don't need it (they're bound to
// an org at creation), so we skip setting it when using one.
func NewAxiomClient(dataset string, ds DatasetConfig) (*AxiomClient, error) {
	opts := []axiom.Option{axiom.SetNoEnv()}

	token, _ := lookupAny("AXIOM_PAT", "AXIOM_PERSONAL_TOKEN")
	if token == "" {
		token, _ = lookupAny("AXIOM_TOKEN", "AXIOM_API_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("missing AXIOM_PAT (or AXIOM_TOKEN)")
	}
	opts = append(opts, axiom.SetToken(token))

	// Prefix tells us what kind of token we're holding. Leading `xapt-` is
	// personal, `xaat-` is API. Anything else we treat as personal to stay
	// forward-compatible — worst case axiom rejects at first request.
	isPersonal := strings.HasPrefix(token, "xapt-")
	if isPersonal || !strings.HasPrefix(token, "xaat-") {
		orgID := os.Getenv("AXIOM_ORG_ID")
		if orgID == "" {
			return nil, fmt.Errorf("AXIOM_ORG_ID required for personal tokens")
		}
		opts = append(opts, axiom.SetOrganizationID(orgID))
	}

	c, err := axiom.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("axiom client: %w", err)
	}
	return &AxiomClient{c: c, dataset: dataset, ds: ds}, nil
}

// ─── Public query types ──────────────────────────────────────────────────────

// Stats is the last-hour roll-up shown in the stats panel.
//
// AvgDur/P95Dur/MaxDur are the legacy single-field numbers, populated from the
// first configured duration field for backward compat. Durations holds the
// per-field breakdown when multiple fields are configured.
type Stats struct {
	Total     int64
	Info      int64
	Warn      int64
	Error     int64
	AvgDur    float64
	P95Dur    float64
	MaxDur    float64
	Durations []DurationStat
}

// DurationStat is a per-field latency roll-up (avg/p95/max) for the stats panel.
type DurationStat struct {
	Field string // dotted key, e.g. "fields.durationMs" — Label() strips prefix
	Avg   float64
	P95   float64
	Max   float64
}

// Label returns the human-readable field name for a DurationStat, stripping
// the "fields." prefix that Axiom adds to flattened keys.
func (d DurationStat) Label() string {
	return strings.TrimPrefix(d.Field, "fields.")
}

// ClientCost is the aggregate AI spend for one client over the current window.
type ClientCost struct {
	Client  string
	Dollars float64
}

// ChartPoint is a single (time, value) pair across all series shapes.
type ChartPoint struct {
	Time  time.Time
	Value float64
}

// Series is a named time-series (e.g. "Req/min" or a client name).
type Series struct {
	Name   string
	Points []ChartPoint
}

// TableRow is a flat row for the top-errors / top-routes panels.
type TableRow struct {
	Count   int64
	Level   string
	Message string
}

// LogEvent is a single raw event from the live stream or recent-issues query.
// Raw holds the flattened map so the expand modal can pretty-print the whole
// payload without running another query.
type LogEvent struct {
	Time    time.Time
	Level   string
	Message string
	Client  string
	Fields  map[string]any
	Raw     map[string]any
}

// ─── Query helpers ───────────────────────────────────────────────────────────

// aplDuration formats a Go duration as an APL `ago()`-compatible literal.
// APL accepts ms/s/m/h/d. Round to the nearest reasonable unit so the query
// is human-readable in logs.
func aplDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// pickBin returns a sensible bin size for a time range, balancing chart
// resolution against query cost. Aim for ~60 buckets across the window.
func pickBin(d time.Duration) string {
	target := d / 60
	switch {
	case target < time.Minute:
		return "30s"
	case target < 5*time.Minute:
		return "1m"
	case target < 15*time.Minute:
		return "5m"
	case target < time.Hour:
		return "15m"
	case target < 4*time.Hour:
		return "30m"
	}
	return "1h"
}

// Stats runs the level-distribution + duration roll-up over the last `lookback`.
// Duration fields being empty is fine — the aggregate section is skipped.
func (a *AxiomClient) Stats(ctx context.Context, lookback time.Duration) (Stats, error) {
	durFields := a.ds.ResolvedDurationFields()

	// Always query level counts — duration roll-ups append per-field.
	var aggParts []string
	aggParts = append(aggParts,
		"total = count()",
		`errors = countif(level == "error")`,
		`warns = countif(level == "warn")`,
		`infos = countif(level == "info")`,
	)
	// Per-field aliases: avg_0, p95_0, max_0, avg_1, p95_1, ...
	// Indexed to dodge Axiom's quoting rules on aliases that contain dots.
	for i, f := range durFields {
		aggParts = append(aggParts,
			fmt.Sprintf("avg_%d = avg(toreal(['%s']))", i, f),
			fmt.Sprintf("p95_%d = percentile(toreal(['%s']), 95)", i, f),
			fmt.Sprintf("max_%d = max(toreal(['%s']))", i, f),
		)
	}
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s) | summarize %s`,
		a.dataset, aplDuration(lookback), strings.Join(aggParts, ", "))

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return Stats{}, err
	}
	row, ok := firstRow(r)
	if !ok {
		return Stats{}, nil
	}
	s := Stats{
		Total: asInt(row["total"]),
		Info:  asInt(row["infos"]),
		Warn:  asInt(row["warns"]),
		Error: asInt(row["errors"]),
	}
	for i, f := range durFields {
		ds := DurationStat{
			Field: f,
			Avg:   asFloat(row[fmt.Sprintf("avg_%d", i)]),
			P95:   asFloat(row[fmt.Sprintf("p95_%d", i)]),
			Max:   asFloat(row[fmt.Sprintf("max_%d", i)]),
		}
		s.Durations = append(s.Durations, ds)
		if i == 0 {
			// Mirror the first field into the legacy scalars so older view
			// code + callers keep working.
			s.AvgDur = ds.Avg
			s.P95Dur = ds.P95
			s.MaxDur = ds.Max
		}
	}
	return s, nil
}

// Throughput returns events/minute over `lookback` as a single series.
func (a *AxiomClient) Throughput(ctx context.Context, lookback time.Duration) (Series, error) {
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s)
       | sort by _time asc`, a.dataset, aplDuration(lookback), pickBin(lookback))

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return Series{}, err
	}
	return seriesFromTimeBin(r, "requests", "Req/min"), nil
}

// ThroughputSegmented groups by a dimension field (typically clientName) and
// returns top-N lines + a synthetic "Other" line for the rest.
func (a *AxiomClient) ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]Series, error) {
	field := a.ds.GroupByField
	if field == "" {
		return nil, nil
	}
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s), ['%s']
       | sort by _time asc`, a.dataset, aplDuration(lookback), pickBin(lookback), field)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	return topSeriesFromBin(r, field, "requests", topN), nil
}

// ErrorRate returns parallel errors + warns series over `lookback`.
func (a *AxiomClient) ErrorRate(ctx context.Context, lookback time.Duration) ([]Series, error) {
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by bin(_time, %s)
       | sort by _time asc`, a.dataset, aplDuration(lookback), pickBin(lookback))

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	return []Series{
		seriesFromTimeBin(r, "errors", "Errors"),
		seriesFromTimeBin(r, "warns", "Warns"),
	}, nil
}

// TopErrors returns the top-N distinct error/warn messages over `lookback`.
func (a *AxiomClient) TopErrors(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize count = count() by message, level
       | sort by count desc
       | take %d`, a.dataset, aplDuration(lookback), n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []TableRow{}
	for row := range rows(r) {
		out = append(out, TableRow{
			Count:   asInt(row["count"]),
			Level:   asString(row["level"]),
			Message: asString(row["message"]),
		})
	}
	return out, nil
}

// TopRoutes filters messages to route prefixes and aggregates hits.
// Falls back to GET/POST/PUT/DELETE/PATCH when the dataset config has none.
func (a *AxiomClient) TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	prefixes := a.ds.RoutePrefixes
	if len(prefixes) == 0 {
		prefixes = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	}
	clauses := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		clauses = append(clauses, fmt.Sprintf(`message startswith "%s"`, p))
	}
	where := strings.Join(clauses, " or ")

	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where %s
       | summarize count = count() by message
       | sort by count desc
       | take %d`, a.dataset, aplDuration(lookback), where, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []TableRow{}
	for row := range rows(r) {
		out = append(out, TableRow{
			Count:   asInt(row["count"]),
			Message: asString(row["message"]),
		})
	}
	return out, nil
}

// RecentIssues returns the last N error/warn events over `lookback`.
func (a *AxiomClient) RecentIssues(ctx context.Context, n int, lookback time.Duration) ([]LogEvent, error) {
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | sort by _time desc
       | take %d`, a.dataset, aplDuration(lookback), n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []LogEvent{}
	for row := range rows(r) {
		out = append(out, a.eventFromRow(row))
	}
	return out, nil
}

// ClientHealth is a per-client hot-spot: how many error+warn events that
// client emitted in the last window. Used for the top-bar summary.
type ClientHealth struct {
	Client string
	Errors int64
	Warns  int64
}

// TopClientHealth returns the noisiest N clients by error+warn count over
// `lookback`. Useful as a "who's on fire right now" bar at the top of the UI.
// Returns nil if the dataset has no group-by field configured.
func (a *AxiomClient) TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]ClientHealth, error) {
	field := a.ds.GroupByField
	if field == "" {
		return nil, nil
	}
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by ['%s']
       | extend total = errors + warns
       | sort by total desc
       | take %d`, a.dataset, aplDuration(lookback), field, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []ClientHealth{}
	for row := range rows(r) {
		client := asString(row[field])
		if client == "" {
			continue
		}
		out = append(out, ClientHealth{
			Client: client,
			Errors: asInt(row["errors"]),
			Warns:  asInt(row["warns"]),
		})
	}
	return out, nil
}

// QueryCost sums fields.costDollars by fields.clientName over `lookback`.
// Returns top-N clients sorted by spend desc. Empty dataset / missing field
// just yields an empty slice rather than erroring — the panel handles that.
func (a *AxiomClient) QueryCost(ctx context.Context, lookback time.Duration) ([]ClientCost, error) {
	apl := fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where isnotnull(['fields.costDollars'])
       | summarize dollars = sum(toreal(['fields.costDollars'])) by ['fields.clientName']
       | sort by dollars desc`,
		a.dataset, aplDuration(lookback))

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []ClientCost{}
	for row := range rows(r) {
		name := asString(row["fields.clientName"])
		if name == "" {
			name = "(unknown)"
		}
		d := asFloat(row["dollars"])
		if d <= 0 {
			continue
		}
		out = append(out, ClientCost{Client: name, Dollars: d})
	}
	return out, nil
}

// StreamSince pulls events newer than `since` (usually ~5s window). The live
// log stream polls this repeatedly; dedup at the caller by timestamp + index.
func (a *AxiomClient) StreamSince(ctx context.Context, since time.Time) ([]LogEvent, error) {
	cutoff := since.UTC().Format(time.RFC3339Nano)
	apl := fmt.Sprintf(`['%s'] | where _time > datetime("%s")
       | sort by _time asc
       | take 500`, a.dataset, cutoff)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []LogEvent{}
	for row := range rows(r) {
		out = append(out, a.eventFromRow(row))
	}
	return out, nil
}

// ─── Internals ───────────────────────────────────────────────────────────────

// eventFromRow turns a flattened row map into a LogEvent. clientName is
// pulled from the configured group-by field when available.
func (a *AxiomClient) eventFromRow(row map[string]any) LogEvent {
	ev := LogEvent{
		Level:   strings.ToLower(asString(row["level"])),
		Message: asString(row["message"]),
		Fields:  map[string]any{},
		Raw:     row,
	}
	if ev.Message == "" {
		// Some loggers emit as `msg` rather than `message`; tolerate either.
		ev.Message = asString(row["msg"])
	}
	ev.Time = asTime(row["_time"])

	// Extract the client dimension when configured. Falls back to common
	// Throxy-style fields so datasets without config still get colors.
	if a.ds.GroupByField != "" {
		ev.Client = asString(row[a.ds.GroupByField])
	}
	if ev.Client == "" {
		ev.Client = asString(row["fields.clientName"])
	}

	// Project the configured interesting fields into ev.Fields. This is the
	// subset that will be inlined on the log row — everything else still
	// lives in ev.Raw for the expand-modal view.
	for _, key := range a.ds.InterestingFields {
		if v, ok := row[key]; ok && v != nil && v != "" {
			ev.Fields[key] = v
		}
	}
	return ev
}

func firstRow(r *query.Result) (map[string]any, bool) {
	for row := range rows(r) {
		return row, true
	}
	return nil, false
}

// rows iterates every row of the first table in a Result, yielding a
// name-keyed map so callers can look up by column name.
func rows(r *query.Result) func(yield func(map[string]any) bool) {
	return func(yield func(map[string]any) bool) {
		if r == nil || len(r.Tables) == 0 {
			return
		}
		tbl := r.Tables[0]
		names := make([]string, len(tbl.Fields))
		for i, f := range tbl.Fields {
			names[i] = f.Name
		}
		for row := range tbl.Rows() {
			m := make(map[string]any, len(names))
			for i, v := range row {
				if i < len(names) {
					m[names[i]] = v
				}
			}
			if !yield(m) {
				return
			}
		}
	}
}

// seriesFromTimeBin pulls (_time, <valueField>) pairs from the first table
// into a single Series. Used for 1-dimensional time bins.
func seriesFromTimeBin(r *query.Result, valueField, seriesName string) Series {
	points := []ChartPoint{}
	for row := range rows(r) {
		points = append(points, ChartPoint{
			Time:  asTime(row["_time"]),
			Value: asFloat(row[valueField]),
		})
	}
	return Series{Name: seriesName, Points: points}
}

// topSeriesFromBin reshapes a (_time, groupField, valueField) table into N
// top-traffic series. Anything outside the top N is summed into an "Other"
// bucket so the chart still sums to the real total.
func topSeriesFromBin(r *query.Result, groupField, valueField string, topN int) []Series {
	// group → time → value
	byGroup := map[string]map[time.Time]float64{}
	totals := map[string]float64{}
	times := map[time.Time]struct{}{}

	for row := range rows(r) {
		g := asString(row[groupField])
		if g == "" {
			g = "(none)"
		}
		t := asTime(row["_time"])
		v := asFloat(row[valueField])
		if byGroup[g] == nil {
			byGroup[g] = map[time.Time]float64{}
		}
		byGroup[g][t] = v
		totals[g] += v
		times[t] = struct{}{}
	}

	// Rank groups by total, take top N.
	ranked := make([]string, 0, len(totals))
	for k := range totals {
		ranked = append(ranked, k)
	}
	sort.Slice(ranked, func(i, j int) bool { return totals[ranked[i]] > totals[ranked[j]] })

	top := ranked
	if len(top) > topN {
		top = ranked[:topN]
	}
	inTop := map[string]bool{}
	for _, g := range top {
		inTop[g] = true
	}

	// Stable x-axis across all series — missing buckets zero out.
	ts := make([]time.Time, 0, len(times))
	for t := range times {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })

	series := make([]Series, 0, len(top)+1)
	for _, g := range top {
		pts := make([]ChartPoint, len(ts))
		for i, t := range ts {
			pts[i] = ChartPoint{Time: t, Value: byGroup[g][t]}
		}
		series = append(series, Series{Name: g, Points: pts})
	}
	// Fold the long tail into "Other" so the chart area stays honest.
	if len(ranked) > topN {
		other := make([]ChartPoint, len(ts))
		for i, t := range ts {
			var sum float64
			for _, g := range ranked[topN:] {
				sum += byGroup[g][t]
			}
			other[i] = ChartPoint{Time: t, Value: sum}
		}
		series = append(series, Series{Name: "Other", Points: other})
	}
	return series
}
