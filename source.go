package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Cursor is an opaque stream position. Axiom uses Time; file sources use Seq.
// The zero Cursor means "not yet positioned".
type Cursor struct {
	Time time.Time
	Seq  uint64
}

// IsZero reports whether the cursor has not been positioned yet.
func (c Cursor) IsZero() bool { return c.Time.IsZero() && c.Seq == 0 }

// Caps declares which optional dimensions a source has configured. Panels
// and keys that depend on a missing capability are not drawn.
type Caps uint8

const (
	CapGroup    Caps = 1 << iota // group field configured: segmented throughput, health bar, @ filter
	CapCost                      // cost field configured: Cost panel
	CapDuration                  // duration fields configured: avg/p95/max rows
	CapRoutes                    // route prefixes configured: Top Routes panel
)

// Has reports whether every bit of want is set.
func (c Caps) Has(want Caps) bool { return c&want == want }

// FilterFields names the LogEvent.Fields keys the p, o and t keys read.
// An empty key disables that filter.
type FilterFields struct {
	Provider  string // key read by the p filter
	Operation string // key read by the o filter
	Trace     string // key read by the t filter (exact match)
}

// SourceInfo is the static description of an open source: what it is
// called, what it can do and how its fields map onto the UI.
type SourceInfo struct {
	Name              string // config name; shown in the stream panel title and footer
	Kind              string // "axiom" | "jsonl" | "pi"
	Caps              Caps
	GroupLabel        string   // "client", "agent": placeholder of the @ prompt and label in the health bar
	InterestingFields []string // inline field order on log rows
	DisplayPrefix     string   // stripped from field names for display; "fields." for axiom
	Filters           FilterFields
	HideLevels        []string // levels hidden in the stream at startup
}

// Label strips the display prefix from a field key.
func (i SourceInfo) Label(key string) string {
	return strings.TrimPrefix(key, i.DisplayPrefix)
}

// streamLimit is the batch size the model asks Stream for.
const streamLimit = 500

// Source is what the UI talks to. Every backend implements it; methods for
// a capability the source lacks return their zero value and a nil error.
type Source interface {
	Info() SourceInfo
	Stats(ctx context.Context, lookback time.Duration) (Stats, error)
	Throughput(ctx context.Context, lookback time.Duration) (Series, error)
	ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]Series, error) // nil, nil without CapGroup
	ErrorRate(ctx context.Context, lookback time.Duration) ([]Series, error)                     // exactly two series, "Errors" then "Warns"
	TopErrors(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error)
	TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) // nil, nil without CapRoutes
	RecentIssues(ctx context.Context, n int, lookback time.Duration) ([]LogEvent, error)
	TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]ClientHealth, error) // nil, nil without CapGroup
	Cost(ctx context.Context, lookback time.Duration) (CostReport, error)                       // zero value without CapCost
	Stream(ctx context.Context, cur Cursor, limit int) ([]LogEvent, Cursor, error)
	Hydrate(ctx context.Context, ev LogEvent) (map[string]any, error) // full raw object for expand and copy
	Close() error
}

// AgentSource is optional. The model checks for it once with a type
// assertion when the source is opened; when present the Agents panel
// replaces the bottom-right area.
type AgentSource interface {
	Agents(ctx context.Context, lookback time.Duration) ([]AgentStat, error)
}

// Progress is the ingest state of a file-backed source, read by the footer
// and by --check. Counters are cumulative since open.
type Progress struct {
	Loading         bool  // first full backfill pass still running
	FilesDone       int   // files fully read in the first pass
	FilesTotal      int   // files discovered
	Bytes           int64 // bytes consumed so far
	Events          int64 // events in the store
	Untimed         int64 // events that fell back to ingestion time
	SkippedLines    int64 // lines over max_line_bytes
	ParseErrors     int64 // lines that were not JSON objects
	UnreadableFiles int64 // files that could not be opened or had no usable header
}

// ProgressSource is optional. Sources that read files implement it so the
// footer can show backfill progress and ingest counters.
type ProgressSource interface {
	Progress() Progress
}

// Stats is the level roll-up shown in the stats panel.
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
	Field string // dotted key, e.g. "fields.durationMs"
	Avg   float64
	P95   float64
	Max   float64
}

// ClientCost is the aggregate spend for one group value over a window.
type ClientCost struct {
	Client  string
	Dollars float64
}

// CostReport is the Cost panel's data.
type CostReport struct {
	ByGroup      []ClientCost // over lookback, sorted desc, rows with Dollars <= 0 omitted
	Total        float64      // over lookback
	HasSummary   bool         // true when Today and TrailingHour are populated
	Today        float64      // local midnight to now
	TrailingHour float64      // now-60m to now
	ByModel      []ClientCost // over lookback, sorted desc; empty when the source has no model dimension
}

// AgentStat is one row of the Agents panel.
type AgentStat struct {
	Name          string       // group value
	LastSeen      time.Time    // newest event time in the group
	SessionFile   string       // newest file in the group (basename)
	SpendToday    float64      //
	SpendHour     float64      // trailing 60 min
	ContextTokens int64        // usage.totalTokens of the newest assistant message in SessionFile
	Compactions   int          // in lookback
	ToolCalls     int          // in lookback
	ToolErrors    int          // in lookback
	OrphanCalls   int          // tool calls older than orphan_after with no result, any age
	TopModel      string       // model with the highest cost in lookback, "" when none
	ModelMix      []ClientCost // cost by model in lookback, desc
	FallbackN     int          // router entries with by == "fallback" in lookback
	FallbackD     int          // all router state entries in lookback
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

// ClientHealth is a per-group hot-spot: how many error+warn events that
// group emitted in the last window. Used for the top-bar summary.
type ClientHealth struct {
	Client string
	Errors int64
	Warns  int64
}

// eventRef locates an event's source line on disk so Hydrate can re-read it.
type eventRef struct {
	file       int32  // index into the tailer's file table
	generation uint32 // bumped on truncation or rotation
	offset     int64  // byte offset of the line
	length     int32  // line length in bytes, newline excluded
}

// LogEvent is a single event from the live stream or recent-issues query.
type LogEvent struct {
	ID      string         // stable identity for dedup; "" allowed (axiom)
	Time    time.Time      //
	Level   string         // "error" | "warn" | "info" | "debug" | other lowercased raw value
	Message string         //
	Client  string         // value of the group field
	Fields  map[string]any // interesting fields plus every field a configured filter reads
	Raw     map[string]any // nil for file sources until Hydrate
	ref     eventRef       // file index, generation, offset, length
}

// OpenSource looks up cfg.Sources[name] and opens the backend for its type.
func OpenSource(cfg Config, name string) (Source, error) {
	sc, ok := cfg.Sources[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q; known sources: %s", name, strings.Join(cfg.SourceNames(), ", "))
	}
	return openSourceConfig(name, sc)
}

// openSourceConfig dispatches on Type. Backends register themselves in
// sourceOpeners so this file stays free of backend details.
func openSourceConfig(name string, sc SourceConfig) (Source, error) {
	open, ok := sourceOpeners[sc.Type]
	if !ok {
		return nil, fmt.Errorf("source %q: unknown type %q (valid: axiom, jsonl, pi)", name, sc.Type)
	}
	return open(name, sc)
}

// sourceOpeners maps a source type to its constructor.
var sourceOpeners = map[string]func(name string, sc SourceConfig) (Source, error){}
