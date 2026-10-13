package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// oracleEvent is the brute-force view of one fixture line, in ingestion order.
type oracleEvent struct {
	seq      uint64
	ts       time.Time
	level    string
	message  string
	client   string
	cost     float64 // NaN when absent
	duration float64 // NaN when absent
	route    string
}

// oracleFixture writes three files spanning the same 30 h with random
// timestamps, so sequence order is not time order, and returns the events
// in ingestion order (file by file, line by line).
func oracleFixture(t *testing.T, dir string, now time.Time, total int) []oracleEvent {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	messages := []string{"GET /users 200", "GET /users 500", "POST /orders 201", "timeout talking to db", "slow query", "cache miss", "token expired", "retrying"}
	levels := []string{"info", "info", "info", "info", "warn", "error", "debug", "notice"}
	clients := []string{"acme", "globex", "initech", "umbrella", "hooli", "stark", "wayne", ""}
	var out []oracleEvent
	perFile := total / 3
	seq := uint64(1)
	for fi := 0; fi < 3; fi++ {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("f%d.jsonl", fi)))
		if err != nil {
			t.Fatal(err)
		}
		w := bufio.NewWriter(f)
		n := perFile
		if fi == 2 {
			n = total - 2*perFile
		}
		for i := 0; i < n; i++ {
			ts := now.Add(-time.Duration(rng.Int63n(int64(30 * time.Hour))))
			lvl := levels[rng.Intn(len(levels))]
			msg := messages[rng.Intn(len(messages))]
			client := clients[rng.Intn(len(clients))]
			ev := oracleEvent{seq: seq, ts: ts, level: normaliseLevel(lvl, nil), message: msg, client: client, cost: math.NaN(), duration: math.NaN()}
			seq++
			line := fmt.Sprintf(`{"ts":"%s","level":"%s","msg":"%s","client":"%s"`, ts.Format(time.RFC3339Nano), lvl, msg, client)
			if rng.Float64() < 0.5 {
				ev.cost = math.Round(rng.Float64()*100) / 100
				line += fmt.Sprintf(`,"cost":%.2f`, ev.cost)
			}
			if rng.Float64() < 0.7 {
				ev.duration = float64(rng.Intn(2000))
				line += fmt.Sprintf(`,"durationMs":%d`, int(ev.duration))
			}
			line += "}\n"
			ev.route = routeOf(msg, []string{"GET", "POST"})
			w.WriteString(line)
			out = append(out, ev)
		}
		w.Flush()
		f.Close()
	}
	return out
}

func oracleSource(t *testing.T, dir string) *FileSource {
	t.Helper()
	sc := SourceConfig{Type: "jsonl", Paths: StringList{dir}, RoutePrefixes: StringList{"GET", "POST"}}
	sc.Fields = FieldsConfig{Time: StringList{"ts"}, Level: StringList{"level"}, Message: StringList{"msg"},
		Group: "client", Cost: "cost", Duration: StringList{"durationMs"}}
	fs, err := newFileSource("oracle", sc, OpenOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	fs.pass()
	return fs
}

func approx(a, b float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	return math.Abs(a-b) <= 1e-9
}

func TestStoreOracle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	events := oracleFixture(t, dir, now, 20000)
	fs := oracleSource(t, dir)
	st := fs.store
	if st.len() != len(events) {
		t.Fatalf("store has %d events, fixture %d", st.len(), len(events))
	}

	for _, window := range timeRangeOptions {
		after := now.Add(-window)
		in := func(e oracleEvent) bool { return e.ts.After(after) }

		// Stats.
		got := st.stats(now, window)
		var want Stats
		var durs []float64
		for _, e := range events {
			if !in(e) {
				continue
			}
			want.Total++
			switch e.level {
			case "error":
				want.Error++
			case "warn":
				want.Warn++
			case "info":
				want.Info++
			}
			if !math.IsNaN(e.duration) {
				durs = append(durs, e.duration)
			}
		}
		if got.Total != want.Total || got.Info != want.Info || got.Warn != want.Warn || got.Error != want.Error {
			t.Errorf("%v stats = %+v, want %+v", window, got, want)
		}
		if len(durs) > 0 {
			sum, max := 0.0, math.Inf(-1)
			for _, d := range durs {
				sum += d
				if d > max {
					max = d
				}
			}
			sorted := append([]float64(nil), durs...)
			sort.Float64s(sorted)
			p95 := sorted[int(math.Ceil(0.95*float64(len(sorted))))-1]
			d := got.Durations[0]
			if !approx(d.Avg, sum/float64(len(durs))) || !approx(d.Max, max) || !approx(d.P95, p95) || d.Field != "durationMs" {
				t.Errorf("%v durations = %+v, want avg %v p95 %v max %v", window, d, sum/float64(len(durs)), p95, max)
			}
		}

		// Throughput, segmented and error rate share the bin grid.
		start, bin, n := bins(now, window)
		binOf := func(e oracleEvent) int {
			i := int(e.ts.Sub(start) / bin)
			if i < 0 || i >= n {
				return -1
			}
			return i
		}
		thr := make([]float64, n)
		errs := make([]float64, n)
		warns := make([]float64, n)
		byGroup := map[string][]float64{}
		totals := map[string]float64{}
		for _, e := range events {
			if !in(e) {
				continue
			}
			b := binOf(e)
			if b < 0 {
				continue
			}
			thr[b]++
			if e.level == "error" {
				errs[b]++
			} else if e.level == "warn" {
				warns[b]++
			}
			g := e.client
			if g == "" {
				g = "(none)"
			}
			if byGroup[g] == nil {
				byGroup[g] = make([]float64, n)
			}
			byGroup[g][b]++
			totals[g]++
		}
		checkSeries := func(name string, got Series, want []float64) {
			if len(got.Points) != len(want) {
				t.Errorf("%v %s: %d points, want %d", window, name, len(got.Points), len(want))
				return
			}
			for i, p := range got.Points {
				if !p.Time.Equal(start.Add(bin*time.Duration(i))) || p.Value != want[i] {
					t.Errorf("%v %s point %d = %v %v, want %v %v", window, name, i, p.Time, p.Value, start.Add(bin*time.Duration(i)), want[i])
					return
				}
			}
		}
		checkSeries("throughput", st.throughput(now, window), thr)
		er := st.errorRate(now, window)
		if len(er) != 2 || er[0].Name != "Errors" || er[1].Name != "Warns" {
			t.Fatalf("errorRate shape: %v", er)
		}
		checkSeries("errors", er[0], errs)
		checkSeries("warns", er[1], warns)

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
		seg := st.throughputSegmented(now, 5, window)
		wantN := len(ranked)
		if wantN > 5 {
			wantN = 6
		}
		if len(seg) != wantN {
			t.Fatalf("%v segmented: %d series, want %d", window, len(seg), wantN)
		}
		for i := 0; i < len(seg) && i < 5 && i < len(ranked); i++ {
			if seg[i].Name != ranked[i] {
				t.Errorf("%v segmented series %d = %s, want %s", window, i, seg[i].Name, ranked[i])
			}
			checkSeries("segment "+ranked[i], seg[i], byGroup[ranked[i]])
		}
		if len(ranked) > 5 {
			other := make([]float64, n)
			for _, g := range ranked[5:] {
				for i, c := range byGroup[g] {
					other[i] += c
				}
			}
			if seg[5].Name != "Other" {
				t.Errorf("last series should be Other, got %s", seg[5].Name)
			}
			checkSeries("Other", seg[5], other)
		}

		// Top errors and routes.
		type mk struct{ msg, lvl string }
		errCounts := map[mk]int64{}
		routeCounts := map[string]int64{}
		for _, e := range events {
			if !in(e) {
				continue
			}
			if e.level == "error" || e.level == "warn" {
				errCounts[mk{e.message, e.level}]++
			}
			if e.route != "" {
				routeCounts[e.route]++
			}
		}
		wantErr := []TableRow{}
		for k, c := range errCounts {
			wantErr = append(wantErr, TableRow{Count: c, Level: k.lvl, Message: k.msg})
		}
		sortRows(wantErr)
		if len(wantErr) > 10 {
			wantErr = wantErr[:10]
		}
		gotErr := st.topErrors(now, 10, window)
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
			t.Errorf("%v topErrors = %v, want %v", window, gotErr, wantErr)
		}
		wantRoutes := []TableRow{}
		for r, c := range routeCounts {
			wantRoutes = append(wantRoutes, TableRow{Count: c, Message: r})
		}
		sortRows(wantRoutes)
		if len(wantRoutes) > 10 {
			wantRoutes = wantRoutes[:10]
		}
		gotRoutes := st.topRoutes(now, 10, window)
		if fmt.Sprint(gotRoutes) != fmt.Sprint(wantRoutes) {
			t.Errorf("%v topRoutes = %v, want %v", window, gotRoutes, wantRoutes)
		}

		// Recent issues: newest 20 error/warn by time, then seq.
		issues := []oracleEvent{}
		for _, e := range events {
			if in(e) && (e.level == "error" || e.level == "warn") {
				issues = append(issues, e)
			}
		}
		sort.Slice(issues, func(i, j int) bool {
			if !issues[i].ts.Equal(issues[j].ts) {
				return issues[i].ts.After(issues[j].ts)
			}
			return issues[i].seq > issues[j].seq
		})
		if len(issues) > 20 {
			issues = issues[:20]
		}
		gotIssues := st.recentIssues(now, 20, window)
		if len(gotIssues) != len(issues) {
			t.Fatalf("%v recentIssues: %d, want %d", window, len(gotIssues), len(issues))
		}
		for i := range issues {
			g := gotIssues[i]
			if !g.Time.Equal(issues[i].ts) || g.Level != issues[i].level || g.Message != issues[i].message || g.Client != issues[i].client {
				t.Errorf("%v recentIssues[%d] = %v %s %q, want %v %s %q", window, i, g.Time, g.Level, g.Message, issues[i].ts, issues[i].level, issues[i].message)
			}
		}

		// Client health.
		health := map[string]*ClientHealth{}
		for _, e := range events {
			if !in(e) || e.client == "" || (e.level != "error" && e.level != "warn") {
				continue
			}
			h := health[e.client]
			if h == nil {
				h = &ClientHealth{Client: e.client}
				health[e.client] = h
			}
			if e.level == "error" {
				h.Errors++
			} else {
				h.Warns++
			}
		}
		wantHealth := []ClientHealth{}
		for _, h := range health {
			wantHealth = append(wantHealth, *h)
		}
		sort.Slice(wantHealth, func(i, j int) bool {
			ti, tj := wantHealth[i].Errors+wantHealth[i].Warns, wantHealth[j].Errors+wantHealth[j].Warns
			if ti != tj {
				return ti > tj
			}
			return wantHealth[i].Client < wantHealth[j].Client
		})
		if len(wantHealth) > 5 {
			wantHealth = wantHealth[:5]
		}
		if gotHealth := st.topClientHealth(now, 5, window); fmt.Sprint(gotHealth) != fmt.Sprint(wantHealth) {
			t.Errorf("%v health = %v, want %v", window, gotHealth, wantHealth)
		}

		// Cost.
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		hourAgo := now.Add(-time.Hour)
		var wantCost CostReport
		groupCost := map[string]float64{}
		for _, e := range events {
			if math.IsNaN(e.cost) {
				continue
			}
			if !e.ts.Before(midnight) && !e.ts.After(now) {
				wantCost.Today += e.cost
			}
			if !e.ts.Before(hourAgo) && !e.ts.After(now) {
				wantCost.TrailingHour += e.cost
			}
			if in(e) {
				wantCost.Total += e.cost
				groupCost[e.client] += e.cost
			}
		}
		wantCost.ByGroup = sortedCosts(groupCost)
		gotCost := st.cost(now, window)
		if !gotCost.HasSummary || !approx(gotCost.Total, wantCost.Total) || !approx(gotCost.Today, wantCost.Today) || !approx(gotCost.TrailingHour, wantCost.TrailingHour) {
			t.Errorf("%v cost = %+v, want %+v", window, gotCost, wantCost)
		}
		if len(gotCost.ByGroup) != len(wantCost.ByGroup) || len(gotCost.ByModel) != 0 {
			t.Fatalf("%v cost groups = %v, want %v", window, gotCost.ByGroup, wantCost.ByGroup)
		}
		for i := range wantCost.ByGroup {
			if gotCost.ByGroup[i].Client != wantCost.ByGroup[i].Client || !approx(gotCost.ByGroup[i].Dollars, wantCost.ByGroup[i].Dollars) {
				t.Errorf("%v cost group %d = %v, want %v", window, i, gotCost.ByGroup[i], wantCost.ByGroup[i])
			}
		}
	}

	// Stream from a zero cursor: newest 500 by time, ascending.
	byTime := append([]oracleEvent(nil), events...)
	sort.SliceStable(byTime, func(i, j int) bool {
		if !byTime[i].ts.Equal(byTime[j].ts) {
			return byTime[i].ts.Before(byTime[j].ts)
		}
		return byTime[i].seq < byTime[j].seq
	})
	newest := byTime[len(byTime)-streamLimit:]
	got, cur, _ := fs.Stream(context.Background(), Cursor{}, streamLimit)
	if len(got) != streamLimit || cur.Seq != uint64(len(events)+1) {
		t.Fatalf("zero cursor stream: %d events, cursor %+v", len(got), cur)
	}
	for i, e := range newest {
		if !got[i].Time.Equal(e.ts) || got[i].Message != e.message {
			t.Fatalf("zero cursor stream[%d] = %v %q, want %v %q", i, got[i].Time, got[i].Message, e.ts, e.message)
		}
	}
	// Five positioned cursors.
	for _, seq := range []uint64{1, 777, 10000, 19600, 20001} {
		got, next, _ := fs.Stream(context.Background(), Cursor{Seq: seq}, streamLimit)
		want := []oracleEvent{}
		for _, e := range events {
			if e.seq >= seq {
				want = append(want, e)
			}
		}
		if len(want) > streamLimit {
			want = want[:streamLimit]
		}
		if len(got) != len(want) {
			t.Fatalf("cursor %d: %d events, want %d", seq, len(got), len(want))
		}
		for i := range want {
			if !got[i].Time.Equal(want[i].ts) || got[i].Message != want[i].message {
				t.Fatalf("cursor %d event %d mismatch", seq, i)
			}
		}
		wantNext := uint64(len(events) + 1)
		if len(want) > 0 && want[len(want)-1].seq+1 <= uint64(len(events)) {
			wantNext = want[len(want)-1].seq + 1
		}
		if next.Seq != wantNext {
			t.Errorf("cursor %d advanced to %d, want %d", seq, next.Seq, wantNext)
		}
	}
}

func TestStoreCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 2500)
	sc := SourceConfig{Type: "jsonl", Paths: StringList{path}, MaxEvents: 1000}
	fs, err := newFileSource("cap", sc, OpenOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	fs.pass()
	if n := fs.store.len(); n > 1000 {
		t.Errorf("store should be capped at 1000, got %d", n)
	}
	evs, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 5000)
	if evs[len(evs)-1].Message != "line 2499" {
		t.Errorf("newest event should survive the cut, last = %q", evs[len(evs)-1].Message)
	}
	if fs.store.nextSeq != 2501 {
		t.Errorf("sequence numbers keep counting: %d", fs.store.nextSeq)
	}
}

// TestStoreIDsStartOnFirstID: the id side table stays nil while no handler
// gives an id, starts aligned when one does, and stays aligned through the
// max_events cut and dropFile.
func TestStoreIDsStartOnFirstID(t *testing.T) {
	st := newStore(10)
	batch := func(file int32, ids ...string) ([]event, []string) {
		evs := make([]event, len(ids))
		for i := range evs {
			evs[i].ref.file = file
			evs[i].ref.offset = int64(i)
		}
		return evs, ids
	}
	st.appendBatch(batch(0, "", ""))
	if st.ids != nil {
		t.Fatal("no ids yet, side table should be nil")
	}
	st.appendBatch(batch(1, "a", "", "c"))
	if len(st.ids) != st.len() || st.ids[2] != "a" || st.ids[3] != "" || st.ids[4] != "c" {
		t.Fatalf("ids = %q", st.ids)
	}
	if got := st.eventID(0); got != "0:0:0" {
		t.Errorf("event without an id = %s, want the ref id", got)
	}
	if got := st.eventID(2); got != "1:a" {
		t.Errorf("event with an id = %s, want 1:a", got)
	}
	// A short ids slice pads; the cut keeps the table in step.
	evs, _ := batch(2, "", "", "", "", "", "", "")
	st.appendBatch(evs, nil)
	if len(st.ids) != st.len() {
		t.Fatalf("after cut: %d events, %d ids", st.len(), len(st.ids))
	}
	st.dropFile(2)
	if len(st.ids) != st.len() || st.len() != 2 || st.ids[0] != "" || st.ids[1] != "c" {
		t.Errorf("after dropFile: %d events, ids %q", st.len(), st.ids)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	vals := []float64{5, 1, 4, 2, 3}
	if got := percentileNearestRank(vals, 95); got != 5 {
		t.Errorf("p95 of 1..5 = %v", got)
	}
	if got := percentileNearestRank(vals, 50); got != 3 {
		t.Errorf("p50 of 1..5 = %v", got)
	}
	hundred := make([]float64, 100)
	for i := range hundred {
		hundred[i] = float64(100 - i)
	}
	if got := percentileNearestRank(hundred, 95); got != 95 {
		t.Errorf("p95 of 1..100 = %v", got)
	}
	if got := percentileNearestRank(nil, 95); got != 0 {
		t.Errorf("empty p95 = %v", got)
	}
}

// TestSelectKth: quickselect agrees with a full sort at every rank, on
// shapes that stress partitioning: random, heavy duplicates, sorted,
// reversed, all equal and organ pipe.
func TestSelectKth(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	shapes := map[string]func(n int) []float64{
		"random": func(n int) []float64 {
			v := make([]float64, n)
			for i := range v {
				v[i] = rng.Float64() * 1000
			}
			return v
		},
		"duplicates": func(n int) []float64 {
			v := make([]float64, n)
			for i := range v {
				v[i] = float64(rng.Intn(4))
			}
			return v
		},
		"sorted": func(n int) []float64 {
			v := make([]float64, n)
			for i := range v {
				v[i] = float64(i)
			}
			return v
		},
		"reversed": func(n int) []float64 {
			v := make([]float64, n)
			for i := range v {
				v[i] = float64(n - i)
			}
			return v
		},
		"equal": func(n int) []float64 { return make([]float64, n) },
		"organ pipe": func(n int) []float64 {
			v := make([]float64, n)
			for i := range v {
				v[i] = float64(min(i, n-i))
			}
			return v
		},
	}
	for name, gen := range shapes {
		for _, n := range []int{1, 2, 3, 7, 64, 1000} {
			base := gen(n)
			sorted := append([]float64(nil), base...)
			sort.Float64s(sorted)
			for k := 0; k < n; k++ {
				v := append([]float64(nil), base...)
				if got := selectKth(v, k); got != sorted[k] {
					t.Fatalf("%s n=%d k=%d: got %v, want %v", name, n, k, got, sorted[k])
				}
			}
		}
	}
}

// TestCostGroup checks that Cost sums by fields.cost_group: by group when
// cost_group is unset, by the mapped field when it differs from group.
func TestCostGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	lines := []string{
		`{"ts":"2026-10-12T09:00:00Z","msg":"a","client":"acme","team":"red","cost":1.5}`,
		`{"ts":"2026-10-12T09:00:01Z","msg":"b","client":"acme","team":"blue","cost":2}`,
		`{"ts":"2026-10-12T09:00:02Z","msg":"c","client":"beta","team":"red","cost":4}`,
		`{"ts":"2026-10-12T09:00:03Z","msg":"d","client":"beta","cost":0.5}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	open := func(costGroup string) *FileSource {
		sc := SourceConfig{Type: "jsonl", Paths: StringList{path}}
		sc.Fields.Group = "client"
		sc.Fields.Cost = "cost"
		sc.Fields.CostGroup = costGroup
		fs, err := newFileSource("t", sc, OpenOptions{}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fs.Close() })
		fs.pass()
		return fs
	}
	now := time.Date(2026, 10, 12, 9, 1, 0, 0, time.UTC)
	byGroup := func(rep CostReport) map[string]float64 {
		out := map[string]float64{}
		for _, c := range rep.ByGroup {
			out[c.Client] = c.Dollars
		}
		return out
	}

	rep := open("").store.cost(now, time.Hour)
	if got := byGroup(rep); got["acme"] != 3.5 || got["beta"] != 4.5 || len(got) != 2 {
		t.Errorf("default cost_group should be group: %v", got)
	}
	if rep.Total != 8 || len(rep.ByModel) != 0 {
		t.Errorf("total %v, by model %v", rep.Total, rep.ByModel)
	}

	rep = open("team").store.cost(now, time.Hour)
	if got := byGroup(rep); got["red"] != 5.5 || got["blue"] != 2 || got["(unknown)"] != 0.5 || len(got) != 3 {
		t.Errorf("cost_group = team should sum by team: %v", got)
	}
	if rep.Total != 8 || len(rep.ByModel) != 0 {
		t.Errorf("total %v, by model %v", rep.Total, rep.ByModel)
	}
}

func TestLevelOverflow(t *testing.T) {
	st := newStore(0)
	if i := st.levelIndex("warn"); i != levelWarn {
		t.Fatalf("warn = %d", i)
	}
	if i := st.levelIndex("other"); i != levelOther || st.levels[i] != "other" {
		t.Fatalf("other = %d (%q)", i, st.levels[i])
	}
	seen := map[uint8]string{}
	for n := 0; len(st.levels) < maxLevels; n++ {
		lv := "custom" + strconv.Itoa(n)
		i := st.levelIndex(lv)
		if st.levels[i] != lv {
			t.Fatalf("%s mapped to %q", lv, st.levels[i])
		}
		seen[i] = lv
	}
	if i := st.levelIndex("one-too-many"); i != levelOther {
		t.Errorf("overflow level = %q, want other", st.levels[i])
	}
	if len(st.levels) != maxLevels {
		t.Errorf("table grew past the cap: %d", len(st.levels))
	}
	// Levels added before the cap keep their own index.
	for i, lv := range seen {
		if got := st.levelIndex(lv); got != i {
			t.Errorf("%s moved from %d to %d", lv, i, got)
		}
	}
}
