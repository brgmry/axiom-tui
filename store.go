package main

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Level indices in the store's level table. Anything else is appended to
// the table as it is first seen.
const (
	levelError uint8 = iota
	levelWarn
	levelInfo
	levelDebug
)

// kv is one projected field on an event. Values are kept as display text.
type kv struct {
	k, v string
}

// event is the store's compact record. The parsed JSON is never retained:
// a 1.5 MB line contributes a ref and a short message, Hydrate re-reads it.
type event struct {
	seq      uint64
	ts       int64     // unix nanoseconds
	cost     float64   // NaN when absent
	message  string    // capped at maxMessageBytes at ingest
	client   string    // interned per store
	route    string    // message prefix match result, "" when none
	dim      string    // second cost dimension: the model for pi, the cost_group value for a generic source whose cost_group differs from group, else ""
	fields   []kv      // interesting and filter fields only
	duration []float64 // one per configured duration field, NaN when absent, in ms
	ref      eventRef
	level    uint8
}

// maxMessageBytes caps the stored message. Hydrate shows the full line.
const maxMessageBytes = 512

// store is the in-memory event store of one file source, guarded by one
// RWMutex. The tailer goroutine writes; Source methods read. Events are
// kept in sequence order, which is ingestion order, not time order.
type store struct {
	mu        sync.RWMutex
	events    []event
	nextSeq   uint64
	maxEvents int
	levels    []string
	levelIdx  map[string]uint8
	durFields []string // duration slot labels
	costByDim bool     // Cost groups by dim instead of client (cost_group != group)
	modelMix  bool     // Cost fills ByModel from dim (pi)

	// ids is a side table parallel to events holding the source's own entry
	// id, kept only when keepIDs is set (pi). The event ID is then
	// "<file index>:<entry id>"; otherwise it derives from the ref.
	ids     []string
	keepIDs bool
}

func newStore(maxEvents int) *store {
	if maxEvents <= 0 {
		maxEvents = defaultMaxEvents
	}
	s := &store{
		nextSeq:   1,
		maxEvents: maxEvents,
		levels:    []string{"error", "warn", "info", "debug"},
		levelIdx:  map[string]uint8{},
	}
	for i, l := range s.levels {
		s.levelIdx[l] = uint8(i)
	}
	return s
}

// levelIndex maps a normalised level string to its table index, appending
// unknown levels under the write lock.
func (s *store) levelIndex(level string) uint8 {
	s.mu.RLock()
	i, ok := s.levelIdx[level]
	s.mu.RUnlock()
	if ok {
		return i
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.levelIdx[level]; ok {
		return i
	}
	if len(s.levels) >= 250 {
		return levelInfo
	}
	s.levels = append(s.levels, level)
	i = uint8(len(s.levels) - 1)
	s.levelIdx[level] = i
	return i
}

// interner shares low-cardinality strings (clients, routes, models) so
// 500,000 events do not carry 500,000 copies. The table never shrinks, so
// only intern values with a bounded set.
type interner map[string]string

func (in interner) get(v string) string {
	if v == "" {
		return ""
	}
	if got, ok := in[v]; ok {
		return got
	}
	if len(in) < 100000 {
		in[v] = v
	}
	return v
}

// appendBatch assigns sequence numbers and appends, enforcing max_events
// by cutting to 90% in one copy when exceeded. ids runs parallel to evs
// and is kept only when the store keeps entry ids.
func (s *store) appendBatch(evs []event, ids []string) {
	if len(evs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range evs {
		evs[i].seq = s.nextSeq
		s.nextSeq++
	}
	s.events = append(s.events, evs...)
	if s.keepIDs {
		s.ids = append(s.ids, ids...)
	}
	if len(s.events) > s.maxEvents {
		keep := s.maxEvents * 9 / 10
		if keep < 1 {
			keep = 1
		}
		cut := len(s.events) - keep
		n := copy(s.events, s.events[cut:])
		clear(s.events[n:])
		s.events = s.events[:n]
		if s.keepIDs {
			n := copy(s.ids, s.ids[cut:])
			clear(s.ids[n:])
			s.ids = s.ids[:n]
		}
	}
}

// dropFile removes every event with the given file index, keeping order.
func (s *store) dropFile(file int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.events[:0]
	var ids []string
	if s.keepIDs {
		ids = s.ids[:0]
	}
	for i, e := range s.events {
		if e.ref.file != file {
			out = append(out, e)
			if s.keepIDs {
				ids = append(ids, s.ids[i])
			}
		}
	}
	clear(s.events[len(out):])
	s.events = out
	if s.keepIDs {
		clear(s.ids[len(ids):])
		s.ids = ids
	}
}

// scan calls fn for every event under the read lock, in sequence order.
func (s *store) scan(fn func(e *event)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.events {
		fn(&s.events[i])
	}
}

// len returns the number of stored events.
func (s *store) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}

// window is the half-open interval (after, now] of a lookback query.
func windowStart(now time.Time, lookback time.Duration) int64 {
	return now.Add(-lookback).UnixNano()
}

// inWindow mirrors the APL `_time > ago(lookback)`.
func inWindow(ts, after int64) bool { return ts > after }

// ─── Aggregates ──────────────────────────────────────────────────────────────

func (s *store) stats(now time.Time, lookback time.Duration) Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	var st Stats
	nDur := len(s.durFields)
	sums := make([]float64, nDur)
	maxs := make([]float64, nDur)
	vals := make([][]float64, nDur)
	for i := range maxs {
		maxs[i] = math.Inf(-1)
	}
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) {
			continue
		}
		st.Total++
		switch e.level {
		case levelError:
			st.Error++
		case levelWarn:
			st.Warn++
		case levelInfo:
			st.Info++
		}
		for d := 0; d < nDur && d < len(e.duration); d++ {
			v := e.duration[d]
			if math.IsNaN(v) {
				continue
			}
			sums[d] += v
			if v > maxs[d] {
				maxs[d] = v
			}
			vals[d] = append(vals[d], v)
		}
	}
	for d := 0; d < nDur; d++ {
		ds := DurationStat{Field: s.durFields[d]}
		if n := len(vals[d]); n > 0 {
			ds.Avg = sums[d] / float64(n)
			ds.Max = maxs[d]
			ds.P95 = percentileNearestRank(vals[d], 95)
		}
		st.Durations = append(st.Durations, ds)
		if d == 0 {
			st.AvgDur, st.P95Dur, st.MaxDur = ds.Avg, ds.P95, ds.Max
		}
	}
	return st
}

// percentileNearestRank sorts a copy of vals and returns the nearest-rank
// percentile: the value at ceil(p/100 * n), 1-indexed.
func percentileNearestRank(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// bins returns the aligned bin starts covering (now-lookback, now].
func bins(now time.Time, lookback time.Duration) (start time.Time, bin time.Duration, n int) {
	bin = pickBinDuration(lookback)
	start = now.Add(-lookback).Truncate(bin)
	end := now.Truncate(bin)
	n = int(end.Sub(start)/bin) + 1
	return start, bin, n
}

func binIndex(ts int64, start time.Time, bin time.Duration, n int) int {
	i := int((ts - start.UnixNano()) / int64(bin))
	if i < 0 || i >= n {
		return -1
	}
	return i
}

func binSeries(name string, start time.Time, bin time.Duration, counts []float64) Series {
	pts := make([]ChartPoint, len(counts))
	for i, c := range counts {
		pts[i] = ChartPoint{Time: start.Add(bin * time.Duration(i)), Value: c}
	}
	return Series{Name: name, Points: pts}
}

func (s *store) throughput(now time.Time, lookback time.Duration) Series {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	start, bin, n := bins(now, lookback)
	counts := make([]float64, n)
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) {
			continue
		}
		if b := binIndex(e.ts, start, bin, n); b >= 0 {
			counts[b]++
		}
	}
	return binSeries("Req/min", start, bin, counts)
}

func (s *store) throughputSegmented(now time.Time, topN int, lookback time.Duration) []Series {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	start, bin, n := bins(now, lookback)
	byGroup := map[string][]float64{}
	totals := map[string]float64{}
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) {
			continue
		}
		b := binIndex(e.ts, start, bin, n)
		if b < 0 {
			continue
		}
		g := e.client
		if g == "" {
			g = "(none)"
		}
		counts := byGroup[g]
		if counts == nil {
			counts = make([]float64, n)
			byGroup[g] = counts
		}
		counts[b]++
		totals[g]++
	}
	ranked := make([]string, 0, len(totals))
	for g := range totals {
		ranked = append(ranked, g)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if totals[ranked[i]] != totals[ranked[j]] {
			return totals[ranked[i]] > totals[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	top := ranked
	if len(top) > topN {
		top = ranked[:topN]
	}
	out := make([]Series, 0, len(top)+1)
	for _, g := range top {
		out = append(out, binSeries(g, start, bin, byGroup[g]))
	}
	if len(ranked) > topN {
		other := make([]float64, n)
		for _, g := range ranked[topN:] {
			for i, c := range byGroup[g] {
				other[i] += c
			}
		}
		out = append(out, binSeries("Other", start, bin, other))
	}
	return out
}

func (s *store) errorRate(now time.Time, lookback time.Duration) []Series {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	start, bin, n := bins(now, lookback)
	errs := make([]float64, n)
	warns := make([]float64, n)
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) || (e.level != levelError && e.level != levelWarn) {
			continue
		}
		b := binIndex(e.ts, start, bin, n)
		if b < 0 {
			continue
		}
		if e.level == levelError {
			errs[b]++
		} else {
			warns[b]++
		}
	}
	return []Series{binSeries("Errors", start, bin, errs), binSeries("Warns", start, bin, warns)}
}

type msgKey struct {
	message string
	level   uint8
}

func (s *store) topErrors(now time.Time, n int, lookback time.Duration) []TableRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	counts := map[msgKey]int64{}
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) || (e.level != levelError && e.level != levelWarn) {
			continue
		}
		counts[msgKey{e.message, e.level}]++
	}
	rows := make([]TableRow, 0, len(counts))
	for k, c := range counts {
		rows = append(rows, TableRow{Count: c, Level: s.levels[k.level], Message: k.message})
	}
	sortRows(rows)
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

// sortRows orders by count desc, then message asc, then level asc, so
// ties are deterministic.
func sortRows(rows []TableRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].Message != rows[j].Message {
			return rows[i].Message < rows[j].Message
		}
		return rows[i].Level < rows[j].Level
	})
}

func (s *store) topRoutes(now time.Time, n int, lookback time.Duration) []TableRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	counts := map[string]int64{}
	for i := range s.events {
		e := &s.events[i]
		if e.route == "" || !inWindow(e.ts, after) {
			continue
		}
		counts[e.route]++
	}
	rows := make([]TableRow, 0, len(counts))
	for r, c := range counts {
		rows = append(rows, TableRow{Count: c, Message: r})
	}
	sortRows(rows)
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}

func (s *store) recentIssues(now time.Time, n int, lookback time.Duration) []LogEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	idx := make([]int, 0, 64)
	for i := range s.events {
		e := &s.events[i]
		if !inWindow(e.ts, after) || (e.level != levelError && e.level != levelWarn) {
			continue
		}
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ea, eb := &s.events[idx[a]], &s.events[idx[b]]
		if ea.ts != eb.ts {
			return ea.ts > eb.ts
		}
		return ea.seq > eb.seq
	})
	if len(idx) > n {
		idx = idx[:n]
	}
	out := make([]LogEvent, 0, len(idx))
	for _, i := range idx {
		out = append(out, s.toLogEvent(i))
	}
	return out
}

func (s *store) topClientHealth(now time.Time, n int, lookback time.Duration) []ClientHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	byClient := map[string]*ClientHealth{}
	for i := range s.events {
		e := &s.events[i]
		if e.client == "" || !inWindow(e.ts, after) || (e.level != levelError && e.level != levelWarn) {
			continue
		}
		h := byClient[e.client]
		if h == nil {
			h = &ClientHealth{Client: e.client}
			byClient[e.client] = h
		}
		if e.level == levelError {
			h.Errors++
		} else {
			h.Warns++
		}
	}
	out := make([]ClientHealth, 0, len(byClient))
	for _, h := range byClient {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].Errors+out[i].Warns, out[j].Errors+out[j].Warns
		if ti != tj {
			return ti > tj
		}
		return out[i].Client < out[j].Client
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// cost sums the cost field by cost_group over the window, plus today
// (local midnight) and the trailing hour over every stored event. The
// group key is client unless the source mapped a distinct cost_group, in
// which case its value rides in dim.
func (s *store) cost(now time.Time, lookback time.Duration) CostReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	after := windowStart(now, lookback)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixNano()
	hourAgo := now.Add(-time.Hour).UnixNano()
	byGroup := map[string]float64{}
	byModel := map[string]float64{}
	rep := CostReport{HasSummary: true}
	for i := range s.events {
		e := &s.events[i]
		if math.IsNaN(e.cost) {
			continue
		}
		if e.ts >= midnight && e.ts <= now.UnixNano() {
			rep.Today += e.cost
		}
		if e.ts >= hourAgo && e.ts <= now.UnixNano() {
			rep.TrailingHour += e.cost
		}
		if !inWindow(e.ts, after) {
			continue
		}
		rep.Total += e.cost
		if s.costByDim {
			byGroup[e.dim] += e.cost
		} else {
			byGroup[e.client] += e.cost
		}
		if s.modelMix && e.dim != "" {
			byModel[e.dim] += e.cost
		}
	}
	rep.ByGroup = sortedCosts(byGroup)
	rep.ByModel = sortedCosts(byModel)
	return rep
}

// sortedCosts turns a map into rows sorted desc by dollars, name asc on
// ties, omitting rows at or below zero.
func sortedCosts(m map[string]float64) []ClientCost {
	out := make([]ClientCost, 0, len(m))
	for name, d := range m {
		if d <= 0 {
			continue
		}
		if name == "" {
			name = "(unknown)"
		}
		out = append(out, ClientCost{Client: name, Dollars: d})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dollars != out[j].Dollars {
			return out[i].Dollars > out[j].Dollars
		}
		return out[i].Client < out[j].Client
	})
	return out
}

// ─── Stream ──────────────────────────────────────────────────────────────────

// newestHeap is a min-heap on time used to select the newest N events.
type newestHeap struct {
	s   *store
	idx []int
}

func (h newestHeap) Len() int { return len(h.idx) }
func (h newestHeap) Less(i, j int) bool {
	a, b := &h.s.events[h.idx[i]], &h.s.events[h.idx[j]]
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	return a.seq < b.seq
}
func (h newestHeap) Swap(i, j int)   { h.idx[i], h.idx[j] = h.idx[j], h.idx[i] }
func (h *newestHeap) Push(x any)     { h.idx = append(h.idx, x.(int)) }
func (h *newestHeap) Pop() any       { n := len(h.idx); v := h.idx[n-1]; h.idx = h.idx[:n-1]; return v }
func (h *newestHeap) peek(i int) int { return h.idx[i] }

// stream implements Source.Stream over the store. ready reports whether
// the first backfill pass has completed; before that a zero cursor yields
// nothing and stays zero.
func (s *store) stream(cur Cursor, limit int, ready bool) ([]LogEvent, Cursor) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = streamLimit
	}
	if cur.Seq == 0 {
		if !ready {
			return nil, Cursor{}
		}
		h := &newestHeap{s: s}
		for i := range s.events {
			if len(h.idx) < limit {
				heap.Push(h, i)
				continue
			}
			top := &s.events[h.peek(0)]
			e := &s.events[i]
			if e.ts > top.ts || (e.ts == top.ts && e.seq > top.seq) {
				h.idx[0] = i
				heap.Fix(h, 0)
			}
		}
		idx := append([]int(nil), h.idx...)
		sort.Slice(idx, func(a, b int) bool {
			ea, eb := &s.events[idx[a]], &s.events[idx[b]]
			if ea.ts != eb.ts {
				return ea.ts < eb.ts
			}
			return ea.seq < eb.seq
		})
		out := make([]LogEvent, 0, len(idx))
		for _, i := range idx {
			out = append(out, s.toLogEvent(i))
		}
		return out, Cursor{Seq: s.nextSeq}
	}
	start := sort.Search(len(s.events), func(i int) bool { return s.events[i].seq >= cur.Seq })
	end := start + limit
	if end > len(s.events) {
		end = len(s.events)
	}
	out := make([]LogEvent, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, s.toLogEvent(i))
	}
	next := Cursor{Seq: s.nextSeq}
	if end < len(s.events) {
		next.Seq = s.events[end].seq
	}
	return out, next
}

// toLogEvent expands the stored event at index i for the UI. Raw stays
// nil: Hydrate fills it on demand.
func (s *store) toLogEvent(i int) LogEvent {
	e := &s.events[i]
	ev := LogEvent{
		ID:      s.eventID(i),
		Time:    time.Unix(0, e.ts),
		Level:   s.levels[e.level],
		Message: e.message,
		Client:  e.client,
		Fields:  make(map[string]any, len(e.fields)),
		ref:     e.ref,
	}
	for _, f := range e.fields {
		ev.Fields[f.k] = f.v
	}
	return ev
}

// eventID is "<file index>:<entry id>" when the source keeps entry ids
// (pi; the session header uses "session"), else the ref-derived id.
func (s *store) eventID(i int) string {
	if s.keepIDs && i < len(s.ids) && s.ids[i] != "" {
		return strconv.Itoa(int(s.events[i].ref.file)) + ":" + s.ids[i]
	}
	return refID(s.events[i].ref)
}

// refID derives the stable identity of a file event from its position:
// file index, generation and byte offset.
func refID(r eventRef) string {
	return strconv.Itoa(int(r.file)) + ":" + strconv.Itoa(int(r.generation)) + ":" + strconv.FormatInt(r.offset, 10)
}

// String renders the ref for diagnostics.
func (r eventRef) String() string {
	return fmt.Sprintf("file=%d gen=%d off=%d len=%d", r.file, r.generation, r.offset, r.length)
}
