// Package axiom is the Axiom backend: it answers every Source query with
// APL against one dataset through the axiom-go SDK. It exports nothing;
// importing it registers the "axiom" source type from init.
package axiom

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/axiomhq/axiom-go/axiom"
	"github.com/axiomhq/axiom-go/axiom/query"
	"github.com/brgmry/tailboard/internal/source"
)

// client is a thin wrapper over axiom-go sized for our query shapes.
// The SDK's Query returns a generic *query.Result, everything here flattens
// that into structs the UI layer can consume directly.
type client struct {
	c       *axiom.Client
	name    string
	dataset string
	sc      source.SourceConfig
}

func init() {
	source.Register("axiom", source.Type{
		Open: func(name string, sc source.SourceConfig, _ source.OpenOptions) (source.Source, error) {
			return newClient(name, sc)
		},
		Defaults: axiomDefaults,
		Validate: validateAxiom,
	})
}

// defaultRoutePrefixes is the axiom default for route_prefixes.
var defaultRoutePrefixes = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

// axiomDefaults covers only the generic keys (dataset, time, level,
// message, display prefix, route prefixes). Cost and filter fields come
// from the config, or from the source package's legacy mapping on the
// legacy paths ([datasets] and --dataset).
func axiomDefaults(name string, sc *source.SourceConfig) {
	if sc.Dataset == "" {
		sc.Dataset = name
	}
	if sc.RoutePrefixes == nil {
		sc.RoutePrefixes = append(source.StringList{}, defaultRoutePrefixes...)
	}
	f := &sc.Fields
	if f.Time == nil {
		f.Time = source.StringList{"_time"}
	}
	if f.Level == nil {
		f.Level = source.StringList{"level"}
	}
	if f.Message == nil {
		f.Message = source.StringList{"message", "msg"}
	}
	if f.DisplayPrefix == "" {
		f.DisplayPrefix = "fields."
	}
	if f.DurationUnit == "" {
		f.DurationUnit = "ms"
	}
}

// aplUnsafe holds the characters that could close an APL ['...'] identifier
// or "..." string literal. Values used in APL may not contain any of them.
const aplUnsafe = "'\"\\[]\n\r"

// validateAxiom rejects config values that would be interpolated into APL
// with a quoting character in them, so none can close ['...'] or "..."
// early.
func validateAxiom(name string, sc source.SourceConfig) error {
	f := sc.Fields
	check := func(key, v string) error {
		if i := strings.IndexAny(v, aplUnsafe); i >= 0 {
			return fmt.Errorf("source %q: %s %q contains %q, which is not allowed in an APL name", name, key, v, v[i:i+1])
		}
		return nil
	}
	values := []struct{ key, v string }{
		{"dataset", sc.Dataset},
		{"fields.group", f.Group},
		{"fields.cost", f.Cost},
		{"fields.cost_group", f.CostGroup},
	}
	for _, d := range f.Duration {
		values = append(values, struct{ key, v string }{"fields.duration", d})
	}
	for _, p := range sc.RoutePrefixes {
		values = append(values, struct{ key, v string }{"route_prefixes", p})
	}
	for _, kv := range values {
		if err := check(kv.key, kv.v); err != nil {
			return err
		}
	}
	return nil
}

// newClient builds a client from env vars. extra SDK options are applied
// last, so tests can point the client at a local server.
//
// Token selection, prefer Personal Access Tokens (`xapt-…`) over API tokens
// (`xaat-…`). API tokens are scoped per-dataset and 403 on query endpoints
// unless the Query permission is explicitly granted; PATs carry full user
// perms and Just Work. When both env vars are present, PAT wins.
//
// Org ID is required for PATs. API tokens don't need it (they're bound to
// an org at creation), so we skip setting it when using one.
func newClient(name string, sc source.SourceConfig, extra ...axiom.Option) (*client, error) {
	sc.ApplyTypeDefaults(name)
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

	c, err := axiom.NewClient(append(opts, extra...)...)
	if err != nil {
		return nil, fmt.Errorf("axiom client: %w", err)
	}
	return &client{c: c, name: name, dataset: sc.Dataset, sc: sc}, nil
}

// Info describes the source from its config. Capabilities follow the field
// mapping, see CapsFor.
func (a *client) Info() source.Info {
	f := a.sc.Fields
	return source.Info{
		Name:              a.name,
		Kind:              "axiom",
		Caps:              source.CapsFor(f, a.sc.RoutePrefixes),
		GroupLabel:        "client",
		InterestingFields: []string(f.Interesting),
		DisplayPrefix:     f.DisplayPrefix,
		Filters: source.FilterFields{
			Provider:  f.Provider,
			Operation: f.Operation,
			Trace:     f.Trace,
		},
		HideLevels: []string(a.sc.HideLevels),
	}
}

// Close is a no-op: the SDK client holds no goroutines of its own.
func (a *client) Close() error { return nil }

// Hydrate returns the flattened row the event was built from. Axiom rows
// always carry Raw, so there is nothing to re-read.
func (a *client) Hydrate(ctx context.Context, ev source.LogEvent) (map[string]any, error) {
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

// pickBin formats PickBinDuration as an APL bin() literal.
func pickBin(d time.Duration) string {
	return aplDuration(source.PickBinDuration(d))
}

// APL builders. Pure functions of their inputs so the exact query text can be
// golden-tested without an Axiom client.
//
// Config validation already rejects quotes, brackets, backslashes and line
// breaks in every value that lands here. The builders escape anyway, as a
// second line: aplName for text inside ['...'], aplString for text inside
// "...". Neither changes a value without those characters.

var (
	aplNameEscaper   = strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	aplStringEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
)

// aplName escapes s for use inside an APL ['...'] identifier.
func aplName(s string) string { return aplNameEscaper.Replace(s) }

// aplString escapes s for use inside an APL "..." string literal.
func aplString(s string) string { return aplStringEscaper.Replace(s) }

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
			fmt.Sprintf("avg_%d = avg(toreal(['%s']))", i, aplName(f)),
			fmt.Sprintf("p95_%d = percentile(toreal(['%s']), 95)", i, aplName(f)),
			fmt.Sprintf("max_%d = max(toreal(['%s']))", i, aplName(f)),
		)
	}
	return fmt.Sprintf(`['%s'] | where _time > ago(%s) | summarize %s`,
		aplName(dataset), aplDuration(lookback), strings.Join(aggParts, ", "))
}

func aplThroughput(dataset string, lookback time.Duration) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s)
       | sort by _time asc`, aplName(dataset), aplDuration(lookback), pickBin(lookback))
}

func aplThroughputSegmented(dataset string, lookback time.Duration, field string) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize requests = count() by bin(_time, %s), ['%s']
       | sort by _time asc`, aplName(dataset), aplDuration(lookback), pickBin(lookback), aplName(field))
}

func aplErrorRate(dataset string, lookback time.Duration) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by bin(_time, %s)
       | sort by _time asc`, aplName(dataset), aplDuration(lookback), pickBin(lookback))
}

func aplTopErrors(dataset string, lookback time.Duration, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize count = count() by message, level
       | sort by count desc
       | take %d`, aplName(dataset), aplDuration(lookback), n)
}

func aplTopRoutes(dataset string, lookback time.Duration, prefixes []string, n int) string {
	clauses := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		clauses = append(clauses, fmt.Sprintf(`message startswith "%s"`, aplString(p)))
	}
	where := strings.Join(clauses, " or ")
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where %s
       | summarize count = count() by message
       | sort by count desc
       | take %d`, aplName(dataset), aplDuration(lookback), where, n)
}

func aplRecentIssues(dataset string, lookback time.Duration, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | sort by _time desc
       | take %d`, aplName(dataset), aplDuration(lookback), n)
}

func aplTopClientHealth(dataset string, lookback time.Duration, field string, n int) string {
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where level == "error" or level == "warn"
       | summarize errors = countif(level == "error"), warns = countif(level == "warn") by ['%s']
       | extend total = errors + warns
       | sort by total desc
       | take %d`, aplName(dataset), aplDuration(lookback), aplName(field), n)
}

// aplCost sums costField by groupField. Without a group it sums the whole
// window into one row, which the Cost panel shows as (unknown).
func aplCost(dataset string, lookback time.Duration, costField, groupField string) string {
	if groupField == "" {
		return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where isnotnull(['%s'])
       | summarize dollars = sum(toreal(['%s']))`,
			aplName(dataset), aplDuration(lookback), aplName(costField), aplName(costField))
	}
	return fmt.Sprintf(`['%s'] | where _time > ago(%s)
       | where isnotnull(['%s'])
       | summarize dollars = sum(toreal(['%s'])) by ['%s']
       | sort by dollars desc`,
		aplName(dataset), aplDuration(lookback), aplName(costField), aplName(costField), aplName(groupField))
}

func aplStream(dataset string, since time.Time) string {
	cutoff := since.UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf(`['%s'] | where _time > datetime("%s")
       | sort by _time asc
       | take 500`, aplName(dataset), cutoff)
}

// Stats runs the level-distribution + duration roll-up over the last `lookback`.
// Duration fields being empty is fine, the aggregate section is skipped.
func (a *client) Stats(ctx context.Context, lookback time.Duration) (source.Stats, error) {
	durFields := []string(a.sc.Fields.Duration)
	apl := aplStats(a.dataset, lookback, durFields)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return source.Stats{}, err
	}
	row, ok := firstRow(r)
	if !ok {
		return source.Stats{}, nil
	}
	s := source.Stats{
		Total: asInt(row["total"]),
		Info:  asInt(row["infos"]),
		Warn:  asInt(row["warns"]),
		Error: asInt(row["errors"]),
	}
	for i, f := range durFields {
		ds := source.DurationStat{
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
func (a *client) Throughput(ctx context.Context, lookback time.Duration) (source.Series, error) {
	apl := aplThroughput(a.dataset, lookback)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return source.Series{}, err
	}
	return seriesFromTimeBin(r, "requests", "ev/min"), nil
}

// ThroughputSegmented groups by the configured group field and
// returns top-N lines + a synthetic "Other" line for the rest.
func (a *client) ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]source.Series, error) {
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
func (a *client) ErrorRate(ctx context.Context, lookback time.Duration) ([]source.Series, error) {
	apl := aplErrorRate(a.dataset, lookback)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	return []source.Series{
		seriesFromTimeBin(r, "errors", "Errors"),
		seriesFromTimeBin(r, "warns", "Warns"),
	}, nil
}

// TopErrors returns the top-N distinct error/warn messages over `lookback`.
func (a *client) TopErrors(ctx context.Context, n int, lookback time.Duration) ([]source.TableRow, error) {
	apl := aplTopErrors(a.dataset, lookback, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []source.TableRow{}
	for row := range rows(r) {
		out = append(out, source.TableRow{
			Count:   asInt(row["count"]),
			Level:   asString(row["level"]),
			Message: asString(row["message"]),
		})
	}
	return out, nil
}

// TopRoutes filters messages to route prefixes and aggregates hits.
// Returns nil when no prefixes are configured.
func (a *client) TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]source.TableRow, error) {
	prefixes := []string(a.sc.RoutePrefixes)
	if len(prefixes) == 0 {
		return nil, nil
	}
	apl := aplTopRoutes(a.dataset, lookback, prefixes, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []source.TableRow{}
	for row := range rows(r) {
		out = append(out, source.TableRow{
			Count:   asInt(row["count"]),
			Message: asString(row["message"]),
		})
	}
	return out, nil
}

// RecentIssues returns the last N error/warn events over `lookback`.
func (a *client) RecentIssues(ctx context.Context, n int, lookback time.Duration) ([]source.LogEvent, error) {
	apl := aplRecentIssues(a.dataset, lookback, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []source.LogEvent{}
	for row := range rows(r) {
		out = append(out, a.eventFromRow(row))
	}
	return out, nil
}

// TopClientHealth returns the noisiest N clients by error+warn count over
// `lookback`. Useful as a "who's on fire right now" bar at the top of the UI.
// Returns nil if the dataset has no group-by field configured.
func (a *client) TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]source.ClientHealth, error) {
	field := a.sc.Fields.Group
	if field == "" {
		return nil, nil
	}
	apl := aplTopClientHealth(a.dataset, lookback, field, n)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, err
	}
	out := []source.ClientHealth{}
	for row := range rows(r) {
		group := asString(row[field])
		if group == "" {
			continue
		}
		out = append(out, source.ClientHealth{
			Client: group,
			Errors: asInt(row["errors"]),
			Warns:  asInt(row["warns"]),
		})
	}
	return out, nil
}

// Cost sums the cost field by the cost group over `lookback`. Returns groups
// sorted by spend desc with the grand total. Missing field just yields an
// empty report rather than erroring, the panel handles that.
func (a *client) Cost(ctx context.Context, lookback time.Duration) (source.CostReport, error) {
	costField := a.sc.Fields.Cost
	groupField := a.sc.Fields.CostGroup
	if costField == "" {
		return source.CostReport{}, nil
	}
	if groupField == "" {
		groupField = a.sc.Fields.Group
	}
	apl := aplCost(a.dataset, lookback, costField, groupField)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return source.CostReport{}, err
	}
	rep := source.CostReport{ByGroup: []source.ClientCost{}}
	for row := range rows(r) {
		name := asString(row[groupField])
		if name == "" {
			name = "(unknown)"
		}
		d := asFloat(row["dollars"])
		if d <= 0 {
			continue
		}
		rep.ByGroup = append(rep.ByGroup, source.ClientCost{Client: name, Dollars: d})
		rep.Total += d
	}
	return rep, nil
}

// Stream pulls events newer than the cursor. A zero cursor starts 5 s ago.
// The returned cursor's Time is the newest event time in the batch, or the
// input position when the batch is empty. The query takes 500 regardless of
// limit, matching the original behaviour.
func (a *client) Stream(ctx context.Context, cur source.Cursor, limit int) ([]source.LogEvent, source.Cursor, error) {
	since := cur.Time
	if cur.IsZero() {
		since = time.Now().Add(-5 * time.Second)
	}
	apl := aplStream(a.dataset, since)

	r, err := a.c.Query(ctx, apl)
	if err != nil {
		return nil, cur, err
	}
	out := []source.LogEvent{}
	next := since
	for row := range rows(r) {
		ev := a.eventFromRow(row)
		if ev.Time.After(next) {
			next = ev.Time
		}
		out = append(out, ev)
	}
	return out, source.Cursor{Time: next}, nil
}

// ─── Internals ───────────────────────────────────────────────────────────────

// eventFromRow turns a flattened row map into a LogEvent. Client is the
// configured group field's value, "" when no group is configured.
func (a *client) eventFromRow(row map[string]any) source.LogEvent {
	f := a.sc.Fields
	ev := source.LogEvent{
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

	// The client dimension exists only when a group field is configured.
	if f.Group != "" {
		ev.Client = asString(row[f.Group])
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
func seriesFromTimeBin(r *query.Result, valueField, seriesName string) source.Series {
	points := []source.ChartPoint{}
	for row := range rows(r) {
		points = append(points, source.ChartPoint{
			Time:  asTime(row["_time"]),
			Value: asFloat(row[valueField]),
		})
	}
	return source.Series{Name: seriesName, Points: points}
}

// topSeriesFromBin reshapes a (_time, groupField, valueField) table into N
// top-traffic series. Anything outside the top N is summed into an "Other"
// bucket so the chart still sums to the real total.
func topSeriesFromBin(r *query.Result, groupField, valueField string, topN int) []source.Series {
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

	series := make([]source.Series, 0, len(top)+1)
	for _, g := range top {
		pts := make([]source.ChartPoint, len(ts))
		for i, t := range ts {
			pts[i] = source.ChartPoint{Time: t, Value: byGroup[g][t]}
		}
		series = append(series, source.Series{Name: g, Points: pts})
	}
	// Fold the long tail into "Other" so the chart area stays honest.
	if len(ranked) > topN {
		other := make([]source.ChartPoint, len(ts))
		for i, t := range ts {
			var sum float64
			for _, g := range ranked[topN:] {
				sum += byGroup[g][t]
			}
			other[i] = source.ChartPoint{Time: t, Value: sum}
		}
		series = append(series, source.Series{Name: "Other", Points: other})
	}
	return series
}
