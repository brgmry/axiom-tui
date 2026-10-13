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
// The SDK's Query returns a generic *query.Result, everything here flattens
// that into structs the UI layer can consume directly.
type AxiomClient struct {
	c       *axiom.Client
	name    string
	dataset string
	sc      SourceConfig
}

func init() {
	sourceOpeners["axiom"] = func(name string, sc SourceConfig) (Source, error) {
		return NewAxiomClient(name, sc)
	}
}

// NewAxiomClient builds a client from env vars.
//
// Token selection, prefer Personal Access Tokens (`xapt-…`) over API tokens
// (`xaat-…`). API tokens are scoped per-dataset and 403 on query endpoints
// unless the Query permission is explicitly granted; PATs carry full user
// perms and Just Work. When both env vars are present, PAT wins.
//
// Org ID is required for PATs. API tokens don't need it (they're bound to
// an org at creation), so we skip setting it when using one.
func NewAxiomClient(name string, sc SourceConfig) (*AxiomClient, error) {
	sc.applyTypeDefaults(name)
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
	// forward-compatible, worst case axiom rejects at first request.
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
	return &AxiomClient{c: c, name: name, dataset: sc.Dataset, sc: sc}, nil
}

// Info describes the source from its config. Capabilities follow the field
// mapping: a group field unlocks segmentation, a cost field the Cost panel.
func (a *AxiomClient) Info() SourceInfo {
	f := a.sc.Fields
	var caps Caps
	if f.Group != "" {
		caps |= CapGroup
	}
	if f.Cost != "" {
		caps |= CapCost
	}
	if len(f.Duration) > 0 {
		caps |= CapDuration
	}
	if len(a.sc.RoutePrefixes) > 0 {
		caps |= CapRoutes
	}
	return SourceInfo{
		Name:              a.name,
		Kind:              "axiom",
		Caps:              caps,
		GroupLabel:        "client",
		InterestingFields: []string(f.Interesting),
		DisplayPrefix:     f.DisplayPrefix,
		Filters: FilterFields{
			Provider:  f.Provider,
			Operation: f.Operation,
			Trace:     f.Trace,
		},
		HideLevels: []string(a.sc.HideLevels),
	}
}

// Close is a no-op: the SDK client holds no goroutines of its own.
func (a *AxiomClient) Close() error { return nil }

// Hydrate returns the flattened row the event was built from. Axiom rows
// always carry Raw, so there is nothing to re-read.
func (a *AxiomClient) Hydrate(ctx context.Context, ev LogEvent) (map[string]any, error) {
	return ev.Raw, nil
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

// pickBinDuration returns a sensible bin size for a time range, balancing
// chart resolution against query cost. Aim for ~60 buckets across the window.
func pickBinDuration(d time.Duration) time.Duration {
	target := d / 60
	switch {
	case target < time.Minute:
		return 30 * time.Second
	case target < 5*time.Minute:
		return time.Minute
	case target < 15*time.Minute:
		return 5 * time.Minute
	case target < time.Hour:
		return 15 * time.Minute
	case target < 4*time.Hour:
		return 30 * time.Minute
	}
	return time.Hour
}

// pickBin formats pickBinDuration as an APL bin() literal.
func pickBin(d time.Duration) string {
	return aplDuration(pickBinDuration(d))
}

// APL builders. Pure functions of their inputs so the exact query text can be
// golden-tested without an Axiom client.

func aplStats(dataset string, lookback time.Duration, durFields []string) string {
	// Always query level counts, duration roll-ups append per-field.
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
	return fmt.Sprintf(`['%s'] | where _time > ago(%s) | summarize %s`,
		dataset, aplDuration(lookback), strings.Join(aggParts, ", "))
}

func aplThroughput(dataset string, lookback time.Duration) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s)
       | sort by _time asc`, dataset, aplDuration(lookback), pickBin(lookback))
}

func aplThroughputSegmented(dataset string, lookback time.Duration, field string) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s), ['%s']
       | sort by _time asc`, dataset, aplDuration(lookback), pickBin(lookback), field)
}

func aplErrorRate(dataset string, lookback time.Duration) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by bin(_time, %s)
       | sort by _time asc`, dataset, aplDuration(lookback), pickBin(lookback))
}

func aplTopErrors(dataset string, lookback time.Duration, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize count = count() by message, level
       | sort by count desc
       | take %d`, dataset, aplDuration(lookback), n)
}

func aplTopRoutes(dataset string, lookback time.Duration, prefixes []string, n int) string {
	clauses := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		clauses = append(clauses, fmt.Sprintf(`message startswith "%s"`, p))
	}
	where := strings.Join(clauses, " or ")
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where %s
       | summarize count = count() by message
       | sort by count desc
       | take %d`, dataset, aplDuration(lookback), where, n)
}

func aplRecentIssues(dataset string, lookback time.Duration, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | sort by _time desc
       | take %d`, dataset, aplDuration(lookback), n)
}

func aplTopClientHealth(dataset string, lookback time.Duration, field string, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by ['%s']
       | extend total = errors + warns
       | sort by total desc
       | take %d`, dataset, aplDuration(lookback), field, n)
}

func aplCost(dataset string, lookback time.Duration, costField, groupField string) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where isnotnull(['%s'])
       | summarize dollars = sum(toreal(['%s'])) by ['%s']
       | sort by dollars desc`,
		dataset, aplDuration(lookback), costField, costField, groupField)
}

func aplStream(dataset string, since time.Time) string {
	cutoff := since.UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf(`['%s'] | where _time > datetime("%s")
       | sort by _time asc
       | take 500`, dataset, cutoff)
}

// Stats runs the level-distribution + duration roll-up over the last `lookback`.
// Duration fields being empty is fine, the aggregate section is skipped.
func (a *AxiomClient) Stats(ctx context.Context, lookback time.Duration) (Stats, error) {
	durFields := []string(a.sc.Fields.Duration)
	apl := aplStats(a.dataset, lookback, durFields)

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
	apl := aplThroughput(a.dataset, lookback)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return Series{}, err
	}
	return seriesFromTimeBin(r, "requests", "Req/min"), nil
}

// ThroughputSegmented groups by a dimension field (typically clientName) and
// returns top-N lines + a synthetic "Other" line for the rest.
func (a *AxiomClient) ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]Series, error) {
	field := a.sc.Fields.Group
	if field == "" {
		return nil, nil
	}
	apl := aplThroughputSegmented(a.dataset, lookback, field)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	return topSeriesFromBin(r, field, "requests", topN), nil
}

// ErrorRate returns parallel errors + warns series over `lookback`.
func (a *AxiomClient) ErrorRate(ctx context.Context, lookback time.Duration) ([]Series, error) {
	apl := aplErrorRate(a.dataset, lookback)

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
	apl := aplTopErrors(a.dataset, lookback, n)

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
// Returns nil when no prefixes are configured.
func (a *AxiomClient) TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	prefixes := []string(a.sc.RoutePrefixes)
	if len(prefixes) == 0 {
		return nil, nil
	}
	apl := aplTopRoutes(a.dataset, lookback, prefixes, n)

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
	apl := aplRecentIssues(a.dataset, lookback, n)

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

// TopClientHealth returns the noisiest N clients by error+warn count over
// `lookback`. Useful as a "who's on fire right now" bar at the top of the UI.
// Returns nil if the dataset has no group-by field configured.
func (a *AxiomClient) TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]ClientHealth, error) {
	field := a.sc.Fields.Group
	if field == "" {
		return nil, nil
	}
	apl := aplTopClientHealth(a.dataset, lookback, field, n)

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

// Cost sums the cost field by the cost group over `lookback`. Returns groups
// sorted by spend desc with the grand total. Missing field just yields an
// empty report rather than erroring, the panel handles that.
func (a *AxiomClient) Cost(ctx context.Context, lookback time.Duration) (CostReport, error) {
	costField := a.sc.Fields.Cost
	groupField := a.sc.Fields.CostGroup
	if costField == "" {
		return CostReport{}, nil
	}
	if groupField == "" {
		groupField = a.sc.Fields.Group
	}
	apl := aplCost(a.dataset, lookback, costField, groupField)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return CostReport{}, err
	}
	rep := CostReport{ByGroup: []ClientCost{}}
	for row := range rows(r) {
		name := asString(row[groupField])
		if name == "" {
			name = "(unknown)"
		}
		d := asFloat(row["dollars"])
		if d <= 0 {
			continue
		}
		rep.ByGroup = append(rep.ByGroup, ClientCost{Client: name, Dollars: d})
		rep.Total += d
	}
	return rep, nil
}

// Stream pulls events newer than the cursor. A zero cursor starts 5 s ago.
// The returned cursor's Time is the newest event time in the batch, or the
// input position when the batch is empty. The query takes 500 regardless of
// limit, matching the original behaviour.
func (a *AxiomClient) Stream(ctx context.Context, cur Cursor, limit int) ([]LogEvent, Cursor, error) {
	since := cur.Time
	if cur.IsZero() {
		since = time.Now().Add(-5 * time.Second)
	}
	apl := aplStream(a.dataset, since)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, cur, err
	}
	out := []LogEvent{}
	next := since
	for row := range rows(r) {
		ev := a.eventFromRow(row)
		if ev.Time.After(next) {
			next = ev.Time
		}
		out = append(out, ev)
	}
	return out, Cursor{Time: next}, nil
}

// ─── Internals ───────────────────────────────────────────────────────────────

// eventFromRow turns a flattened row map into a LogEvent. clientName is
// pulled from the configured group-by field when available.
func (a *AxiomClient) eventFromRow(row map[string]any) LogEvent {
	f := a.sc.Fields
	ev := LogEvent{
		Level:   strings.ToLower(firstString(row, f.Level)),
		Message: firstString(row, f.Message),
		Fields:  map[string]any{},
		Raw:     row,
	}
	for _, key := range f.Time {
		if t := asTime(row[key]); !t.IsZero() {
			ev.Time = t
			break
		}
	}

	// Extract the client dimension when configured. Falls back to the
	// clientName field so datasets without config still get colors.
	if f.Group != "" {
		ev.Client = asString(row[f.Group])
	}
	if ev.Client == "" {
		ev.Client = asString(row["fields.clientName"])
	}

	// Project the interesting fields plus every field a filter reads into
	// ev.Fields. Everything else still lives in ev.Raw for the expand modal.
	for _, key := range f.Interesting {
		if v, ok := row[key]; ok && v != nil && v != "" {
			ev.Fields[key] = v
		}
	}
	for _, key := range []string{f.Provider, f.Operation, f.Trace} {
		if key == "" {
			continue
		}
		if v, ok := row[key]; ok && v != nil && v != "" {
			ev.Fields[key] = v
		}
	}
	return ev
}

// firstString returns the first non-empty string among keys in row.
func firstString(row map[string]any, keys []string) string {
	for _, k := range keys {
		if s := asString(row[k]); s != "" {
			return s
		}
	}
	return ""
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

	// Stable x-axis across all series, missing buckets zero out.
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
