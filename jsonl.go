package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/buger/jsonparser"
)

func init() {
	registerSourceType("jsonl", sourceType{
		open: func(name string, sc SourceConfig) (Source, error) {
			return NewFileSource(name, sc)
		},
		defaults: jsonlDefaults,
		validate: func(name string, sc SourceConfig) error {
			if len(sc.Paths) == 0 {
				return fmt.Errorf("source %q has type jsonl but no paths", name)
			}
			return nil
		},
	})
}

// jsonlDefaults takes the common spellings of time, level and message, and
// lets cost_group follow group.
func jsonlDefaults(name string, sc *SourceConfig) {
	sc.applyFileDefaults()
	f := &sc.Fields
	if f.Time == nil {
		f.Time = StringList{"timestamp", "ts", "_time", "time"}
	}
	if f.Level == nil {
		f.Level = StringList{"level", "severity", "lvl"}
	}
	if f.Message == nil {
		f.Message = StringList{"message", "msg"}
	}
	if f.CostGroup == "" {
		f.CostGroup = f.Group
	}
	if f.DurationUnit == "" {
		f.DurationUnit = "ms"
	}
}

// FileSource follows JSONL files and answers every query from an in-memory
// store. The tailer goroutine writes, Source methods read.
type FileSource struct {
	name     string
	sc       SourceConfig
	info     SourceInfo
	store    *store
	tailer   *tailer
	counters *counters
	once     sync.Once
}

// NewFileSource builds a generic jsonl source and starts following its
// paths. Paths that resolve to nothing are fine: files may appear later.
func NewFileSource(name string, sc SourceConfig) (*FileSource, error) {
	return newFileSource(name, sc, true)
}

// newFileSource is NewFileSource with the goroutine optional, for tests
// that drive discoverOnce and pollOnce by hand.
func newFileSource(name string, sc SourceConfig, start bool) (*FileSource, error) {
	sc.applyTypeDefaults(name)
	if len(sc.Paths) == 0 {
		return nil, fmt.Errorf("source %s: type jsonl needs paths", name)
	}
	fm := newFieldMap(sc.Fields)
	st := newStore(sc.MaxEvents)
	st.durFields = fm.durationFields()
	if fm.costGroup.raw != fm.group.raw {
		st.costDimRole = costDimGroup
	}
	c := &counters{}
	h := newGenericHandler(fm, st, c, sc.RoutePrefixes)
	t := newTailer(sc, st, h, c)
	fs := &FileSource{name: name, sc: sc, store: st, tailer: t, counters: c}
	fs.info = fileSourceInfo(name, "jsonl", sc)
	if start {
		t.start()
	}
	return fs, nil
}

// fileSourceInfo derives the SourceInfo of a file-backed source from its
// config. Capabilities follow which fields are mapped, see capsFor.
func fileSourceInfo(name, kind string, sc SourceConfig) SourceInfo {
	f := sc.Fields
	return SourceInfo{
		Name:              name,
		Kind:              kind,
		Caps:              capsFor(f, sc.RoutePrefixes),
		GroupLabel:        "client",
		InterestingFields: []string(f.Interesting),
		DisplayPrefix:     f.DisplayPrefix,
		Filters: FilterFields{
			Provider:  f.Provider,
			Operation: f.Operation,
			Trace:     f.Trace,
		},
		HideLevels: []string(sc.HideLevels),
	}
}

func (f *FileSource) Info() SourceInfo { return f.info }

// Progress implements ProgressSource.
func (f *FileSource) Progress() Progress { return f.tailer.progress() }

func (f *FileSource) Stats(ctx context.Context, lookback time.Duration) (Stats, error) {
	return f.store.stats(time.Now(), lookback), nil
}

func (f *FileSource) Throughput(ctx context.Context, lookback time.Duration) (Series, error) {
	return f.store.throughput(time.Now(), lookback), nil
}

func (f *FileSource) ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]Series, error) {
	if !f.info.Caps.Has(CapGroup) {
		return nil, nil
	}
	return f.store.throughputSegmented(time.Now(), topN, lookback), nil
}

func (f *FileSource) ErrorRate(ctx context.Context, lookback time.Duration) ([]Series, error) {
	return f.store.errorRate(time.Now(), lookback), nil
}

func (f *FileSource) TopErrors(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	return f.store.topErrors(time.Now(), n, lookback), nil
}

func (f *FileSource) TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	if !f.info.Caps.Has(CapRoutes) {
		return nil, nil
	}
	return f.store.topRoutes(time.Now(), n, lookback), nil
}

func (f *FileSource) RecentIssues(ctx context.Context, n int, lookback time.Duration) ([]LogEvent, error) {
	return f.store.recentIssues(time.Now(), n, lookback), nil
}

func (f *FileSource) TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]ClientHealth, error) {
	if !f.info.Caps.Has(CapGroup) {
		return nil, nil
	}
	return f.store.topClientHealth(time.Now(), n, lookback), nil
}

func (f *FileSource) Cost(ctx context.Context, lookback time.Duration) (CostReport, error) {
	if !f.info.Caps.Has(CapCost) {
		return CostReport{}, nil
	}
	return f.store.cost(time.Now(), lookback), nil
}

func (f *FileSource) Stream(ctx context.Context, cur Cursor, limit int) ([]LogEvent, Cursor, error) {
	ready := !f.tailer.progress().Loading
	events, next := f.store.stream(cur, limit, ready)
	return events, next, nil
}

// Hydrate re-reads the event's line. When the file has been truncated or
// rotated since, the projected fields come back with a note instead.
func (f *FileSource) Hydrate(ctx context.Context, ev LogEvent) (map[string]any, error) {
	raw, err := f.tailer.hydrate(ev.ref)
	if err != nil {
		out := make(map[string]any, len(ev.Fields)+1)
		for k, v := range ev.Fields {
			out[k] = v
		}
		out["_tailboard"] = errStaleRef.Error()
		return out, nil
	}
	return raw, nil
}

// Close stops the tailer. Idempotent; the store stays readable.
func (f *FileSource) Close() error {
	f.once.Do(f.tailer.close)
	return nil
}

// ─── Generic handler ─────────────────────────────────────────────────────────

// genericHandler maps a JSON object onto an event with a FieldMap.
type genericHandler struct {
	fm       *FieldMap
	st       *store
	counters *counters
	prefixes []string
	intern   interner
}

func newGenericHandler(fm *FieldMap, st *store, c *counters, prefixes []string) *genericHandler {
	return &genericHandler{fm: fm, st: st, counters: c, prefixes: prefixes, intern: interner{}}
}

func (h *genericHandler) reset(file int32) {}

func (h *genericHandler) handle(file int32, gen uint32, offset int64, line []byte, ingest time.Time) (event, string, bool) {
	fm := h.fm
	ev := event{cost: math.NaN()}

	// Time: first present field, else ingestion time.
	if v, t, ok := firstOf(line, fm.time); ok {
		if ts, ok := parseTime(v, t, fm.timeFormat); ok {
			ev.ts = ts.UnixNano()
		}
	}
	if ev.ts == 0 {
		ev.ts = ingest.UnixNano()
		h.counters.untimed.Add(1)
	}

	level := "info"
	if v, t, ok := firstOf(line, fm.level); ok {
		level = normaliseLevel(valueString(v, t), fm.levelMap)
	}
	if v, t, ok := firstOf(line, fm.message); ok {
		ev.message = capMessage(valueString(v, t), maxMessageBytes)
	}

	client := ""
	if v, t, ok := fm.group.get(line); ok {
		client = valueString(v, t)
	}
	if v, t, ok := fm.cost.get(line); ok {
		if c, ok := valueFloat(v, t); ok {
			ev.cost = c
		}
	}
	// cost_group only costs a second string when it differs from group.
	if h.st.costDimRole == costDimGroup {
		if v, t, ok := fm.costGroup.get(line); ok {
			ev.costDim = h.intern.get(valueString(v, t))
		}
	}
	if len(fm.duration) > 0 {
		ev.duration = make([]float64, len(fm.duration))
		for i, p := range fm.duration {
			ev.duration[i] = math.NaN()
			if v, t, ok := p.get(line); ok {
				if d, ok := valueFloat(v, t); ok {
					ev.duration[i] = d * fm.durScale
				}
			}
		}
	}
	if len(fm.projected) > 0 {
		for _, p := range fm.projected {
			if v, t, ok := p.get(line); ok {
				if t == jsonparser.Object || t == jsonparser.Array {
					continue
				}
				ev.fields = append(ev.fields, kv{k: p.raw, v: valueString(v, t)})
			}
		}
	}
	route := routeOf(ev.message, h.prefixes)

	ev.level = h.st.levelIndex(level)
	ev.client = h.intern.get(client)
	ev.route = h.intern.get(route)
	return ev, "", true
}

// routeOf computes the Top Routes key at ingest: when the message starts
// with one of the prefixes, the route is the message up to its second space.
func routeOf(message string, prefixes []string) string {
	for _, p := range prefixes {
		if strings.HasPrefix(message, p) {
			first := strings.IndexByte(message, ' ')
			if first < 0 {
				return message
			}
			second := strings.IndexByte(message[first+1:], ' ')
			if second < 0 {
				return message
			}
			return message[:first+1+second]
		}
	}
	return ""
}
