package main

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// benchDir holds generated fixtures for the lifetime of the test binary.
// TestMain removes it.
var (
	benchDir     string
	benchDirOnce sync.Once
)

func benchFixtureDir() string {
	benchDirOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tailboard-bench-")
		if err != nil {
			panic(err)
		}
		benchDir = dir
	})
	return benchDir
}

func cleanupBenchFixtures() {
	if benchDir != "" {
		os.RemoveAll(benchDir)
	}
}

// benchLines writes n generic lines (~250 bytes, same shape as
// internal/gen) to a file and returns its path and size.
func benchLines(b testing.TB, name string, n int) (string, int64) {
	path := filepath.Join(benchFixtureDir(), name)
	if fi, err := os.Stat(path); err == nil {
		return path, fi.Size()
	}
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	rng := rand.New(rand.NewSource(42))
	now := time.Now().UTC()
	routes := []string{"/api/users", "/api/orders", "/api/search", "/health"}
	clients := []string{"acme", "globex", "initech", "umbrella", "hooli", "stark", "wayne"}
	for i := 0; i < n; i++ {
		ts := now.Add(-time.Duration(rng.Int63n(int64(24 * time.Hour))))
		level, msg := "info", fmt.Sprintf("GET %s %d %dms", routes[rng.Intn(len(routes))], 200+rng.Intn(3)*100, rng.Intn(200))
		switch r := rng.Float64(); {
		case r < 0.03:
			level, msg = "error", "upstream timeout"
		case r < 0.10:
			level, msg = "warn", "slow query"
		}
		fmt.Fprintf(w, `{"timestamp":"%s","level":"%s","message":"%s","service":"api","requestId":"%08x-%04x","userId":"u%05d","durationMs":%.2f,"client":"%s","fields":{"region":"eu-west-%d","pod":"api-%d"}}`+"\n",
			ts.Format(time.RFC3339Nano), level, msg, rng.Uint32(), rng.Intn(1<<16), rng.Intn(50000), rng.ExpFloat64()*40, clients[rng.Intn(len(clients))], rng.Intn(3), rng.Intn(8))
	}
	w.Flush()
	f.Close()
	fi, _ := os.Stat(path)
	return path, fi.Size()
}

func benchSourceConfig(path string) SourceConfig {
	sc := SourceConfig{Type: "jsonl", Paths: StringList{path}, RoutePrefixes: StringList{"GET", "POST"}}
	sc.Fields = FieldsConfig{Group: "client", Duration: StringList{"durationMs"}, Trace: "requestId",
		Interesting: StringList{"userId", "fields.region"}}
	return sc
}

func BenchmarkIngestGeneric(b *testing.B) {
	path, size := benchLines(b, "ingest.jsonl", 100000)
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fs, err := newFileSource("bench", benchSourceConfig(path), OpenOptions{}, false)
		if err != nil {
			b.Fatal(err)
		}
		fs.pass()
		if fs.store.len() != 100000 {
			b.Fatalf("ingested %d", fs.store.len())
		}
	}
	b.StopTimer()
	perOp := b.Elapsed().Seconds() / float64(b.N)
	b.ReportMetric(float64(size)/perOp/1e6, "MB/s")
}

func BenchmarkStoreMemory(b *testing.B) {
	path, _ := benchLines(b, "memory.jsonl", 500000)
	for i := 0; i < b.N; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		fs, err := newFileSource("bench", benchSourceConfig(path), OpenOptions{}, false)
		if err != nil {
			b.Fatal(err)
		}
		fs.pass()
		runtime.GC()
		runtime.ReadMemStats(&after)
		n := fs.store.len()
		if n != 500000 {
			b.Fatalf("ingested %d", n)
		}
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "bytes/event")
		runtime.KeepAlive(fs)
	}
}

func BenchmarkQueries(b *testing.B) {
	path, _ := benchLines(b, "memory.jsonl", 500000)
	fs, err := newFileSource("bench", benchSourceConfig(path), OpenOptions{}, false)
	if err != nil {
		b.Fatal(err)
	}
	fs.pass()
	st := fs.store
	now := time.Now()
	window := 24 * time.Hour
	cases := []struct {
		name string
		fn   func()
	}{
		{"Stats", func() { st.stats(now, window) }},
		{"Throughput", func() { st.throughput(now, window) }},
		{"ThroughputSegmented", func() { st.throughputSegmented(now, 5, window) }},
		{"ErrorRate", func() { st.errorRate(now, window) }},
		{"TopErrors", func() { st.topErrors(now, 10, window) }},
		{"TopRoutes", func() { st.topRoutes(now, 10, window) }},
		{"RecentIssues", func() { st.recentIssues(now, 20, window) }},
		{"TopClientHealth", func() { st.topClientHealth(now, 5, 15*time.Minute) }},
		{"Cost", func() { st.cost(now, window) }},
		{"StreamZero", func() { st.stream(Cursor{}, streamLimit, true) }},
		{"StreamCursor", func() { st.stream(Cursor{Seq: 250000}, streamLimit, true) }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				c.fn()
			}
			b.ReportMetric(float64(b.Elapsed().Milliseconds())/float64(b.N), "ms/op")
		})
	}
}

func BenchmarkFiltered(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	clients := []string{"acme", "globex", "initech", "umbrella"}
	levels := []string{"info", "info", "info", "warn", "error", "debug"}
	buf := NewLogBuffer(2000)
	now := time.Now()
	evs := make([]LogEvent, 2000)
	for i := range evs {
		evs[i] = LogEvent{
			ID:      strconv.Itoa(i),
			Time:    now.Add(time.Duration(i) * time.Millisecond),
			Level:   levels[rng.Intn(len(levels))],
			Message: fmt.Sprintf("GET /api/orders %d %dms", 200+rng.Intn(3)*100, rng.Intn(200)),
			Client:  clients[rng.Intn(len(clients))],
			Fields:  map[string]any{"userId": fmt.Sprintf("u%05d", rng.Intn(50000)), "region": "eu-west-1", "durationMs": rng.ExpFloat64() * 40},
		}
	}
	buf.Append(evs)
	f := NewLogFilter(SourceInfo{HideLevels: []string{"debug"}})
	f.Search = "U01"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Filtered(f)
	}
}
