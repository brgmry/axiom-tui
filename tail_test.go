package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestFileSource builds a jsonl source over paths without starting the
// goroutine, so tests drive discoverOnce and pollOnce explicitly.
func newTestFileSource(t *testing.T, paths ...string) *FileSource {
	t.Helper()
	sc := SourceConfig{Type: "jsonl", Paths: StringList(paths)}
	sc.Fields.Interesting = StringList{"n"}
	fs, err := newFileSource("test", sc, OpenOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	return fs
}

func writeLines(t *testing.T, path string, from, to int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := from; i < to; i++ {
		fmt.Fprintf(f, `{"timestamp":"2026-10-12T09:%02d:00Z","level":"info","message":"line %d","n":%d}`+"\n", i%60, i, i)
	}
}

func (f *FileSource) pass() {
	f.tailer.discoverOnce()
	f.tailer.pollOnce()
}

func messages(evs []LogEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Message)
	}
	return out
}

func TestTailAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 3)
	fs := newTestFileSource(t, path)
	fs.pass()
	if got := fs.store.len(); got != 3 {
		t.Fatalf("after first poll: %d events", got)
	}
	ctx := context.Background()
	evs, cur, err := fs.Stream(ctx, Cursor{}, streamLimit)
	if err != nil || len(evs) != 3 || cur.Seq != 4 {
		t.Fatalf("zero-cursor stream: %d events, cursor %+v, err %v", len(evs), cur, err)
	}

	writeLines(t, path, 3, 5)
	fs.tailer.pollOnce()
	if got := fs.store.len(); got != 5 {
		t.Fatalf("after append: %d events", got)
	}
	evs, next, _ := fs.Stream(ctx, cur, streamLimit)
	if got := messages(evs); strings.Join(got, ",") != "line 3,line 4" {
		t.Errorf("stream from cursor should return exactly the new lines, got %v", got)
	}
	if next.Seq != 6 {
		t.Errorf("advanced cursor = %d, want 6", next.Seq)
	}
	// Order within the file is file order.
	all, _, _ := fs.Stream(ctx, Cursor{Seq: 1}, streamLimit)
	if got := messages(all); strings.Join(got, ",") != "line 0,line 1,line 2,line 3,line 4" {
		t.Errorf("order = %v", got)
	}
	if all[0].Fields["n"] != "0" || all[0].ID == "" {
		t.Errorf("fields or id missing: %+v", all[0])
	}
}

func TestTailPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(path, []byte(`{"message":"partial","level":"info"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newTestFileSource(t, path)
	fs.pass()
	if fs.store.len() != 0 {
		t.Fatalf("partial line must not be emitted, got %d", fs.store.len())
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("\n")
	f.Close()
	fs.tailer.pollOnce()
	evs, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	if len(evs) != 1 || evs[0].Message != "partial" {
		t.Fatalf("after newline: %v", messages(evs))
	}
}

func TestTailTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 10)
	fs := newTestFileSource(t, path)
	fs.pass()
	old, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	if len(old) != 10 {
		t.Fatalf("expected 10, got %d", len(old))
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, 100, 102)
	fs.tailer.pollOnce()
	if got := fs.store.len(); got != 2 {
		t.Fatalf("after truncation the store should hold 2 events, got %d", got)
	}
	raw, err := fs.Hydrate(context.Background(), old[0])
	if err != nil {
		t.Fatal(err)
	}
	if raw["_tailboard"] != "source line no longer on disk" {
		t.Errorf("pre-truncation event should hydrate soft: %v", raw)
	}
	cur, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	raw, _ = fs.Hydrate(context.Background(), cur[0])
	if raw["message"] != "line 100" {
		t.Errorf("post-truncation event should hydrate fully: %v", raw)
	}
}

func TestTailHydrateCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 1)
	fs := newTestFileSource(t, path)
	fs.pass()
	evs, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, err := fs.Hydrate(ctx, evs[0])
	if !errors.Is(err, context.Canceled) || raw != nil {
		t.Errorf("cancelled hydrate = %v, %v; want context.Canceled", raw, err)
	}
}

func TestTailRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	writeLines(t, path, 0, 5)
	fs := newTestFileSource(t, path)
	fs.pass()

	if err := os.Rename(path, filepath.Join(dir, "a.jsonl.1")); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, 10, 13)
	fs.tailer.pollOnce()
	if got := fs.store.len(); got != 8 {
		t.Fatalf("rotation should keep old events: %d", got)
	}
	evs, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	newGen := 0
	for _, e := range evs {
		if e.ref.generation == 1 {
			newGen++
		}
	}
	if newGen != 3 {
		t.Errorf("3 events should carry the new generation, got %d", newGen)
	}
	// Old events hydrate soft, new ones fully.
	raw, _ := fs.Hydrate(context.Background(), evs[0])
	if _, stale := raw["_tailboard"]; !stale {
		t.Error("pre-rotation event should hydrate soft")
	}
	raw, _ = fs.Hydrate(context.Background(), evs[7])
	if raw["message"] != "line 12" {
		t.Errorf("post-rotation hydrate = %v", raw)
	}
}

func TestTailLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	big := strings.Repeat("x", 2*1024*1024)
	f, _ := os.Create(path)
	fmt.Fprintf(f, `{"message":"big","payload":"%s"}`+"\n", big)
	f.Close()
	fs := newTestFileSource(t, path)
	fs.pass()
	evs, _, _ := fs.Stream(context.Background(), Cursor{Seq: 1}, 10)
	if len(evs) != 1 || evs[0].Message != "big" {
		t.Fatalf("2 MB line should ingest with default limits: %v", messages(evs))
	}
	raw, _ := fs.Hydrate(context.Background(), evs[0])
	if len(raw["payload"].(string)) != len(big) {
		t.Error("hydrate should return the full payload")
	}

	// Over the limit: skipped, counted, and the next line still lands.
	sc := SourceConfig{Type: "jsonl", Paths: StringList{path}, MaxLineBytes: 1000}
	small, err := newFileSource("small", sc, OpenOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	f, _ = os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	fmt.Fprintf(f, `{"message":"too long","p":"%s"}`+"\n", strings.Repeat("y", 1001-len(`{"message":"too long","p":""}`)))
	fmt.Fprintln(f, `{"message":"after"}`)
	f.Close()
	small.pass()
	evs, _, _ = small.Stream(context.Background(), Cursor{Seq: 1}, 10)
	if len(evs) != 1 || evs[0].Message != "after" {
		t.Fatalf("line after the long one should ingest: %v", messages(evs))
	}
	if n := small.Progress().SkippedLines; n != 1 {
		t.Errorf("skipped_lines = %d", n)
	}
}

func TestTailDiscovery(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "nested"), 0o755)
	writeLines(t, filepath.Join(dir, "nested", "in.jsonl"), 0, 2)
	writeLines(t, filepath.Join(dir, "old.jsonl"), 0, 3)
	oldTime := time.Now().Add(-72 * time.Hour)
	os.Chtimes(filepath.Join(dir, "old.jsonl"), oldTime, oldTime)
	writeLines(t, filepath.Join(dir, "other.log"), 0, 4)

	fs := newTestFileSource(t, dir)
	fs.pass()
	if got := fs.store.len(); got != 2 {
		t.Fatalf("only the nested jsonl should load, got %d events", got)
	}
	p := fs.Progress()
	if p.FilesTotal != 1 || p.Loading {
		t.Errorf("progress = %+v", p)
	}

	writeLines(t, filepath.Join(dir, "late.jsonl"), 0, 5)
	fs.pass()
	fs.pass()
	if got := fs.store.len(); got != 7 {
		t.Errorf("late file should be picked up within two scans, got %d", got)
	}
}

func TestTailDeletedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	writeLines(t, path, 0, 3)
	fs := newTestFileSource(t, dir)
	fs.pass()
	os.Remove(path)
	fs.pass()
	if got := fs.store.len(); got != 3 {
		t.Errorf("deleted file keeps its events: %d", got)
	}
	if fs.Progress().FilesTotal != 0 {
		t.Errorf("gone file should leave the count: %+v", fs.Progress())
	}
	writeLines(t, path, 0, 2)
	fs.pass()
	if got := fs.store.len(); got != 5 {
		t.Errorf("re-created file should be followed again: %d", got)
	}
}

func TestTailParseErrorsAndUntimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	os.WriteFile(path, []byte("not json\n{\"message\":\"ok\",\"timestamp\":\"nope\"}\n[1,2]\n\n{\"message\":\"two\"}\n"), 0o644)
	fs := newTestFileSource(t, path)
	fs.pass()
	p := fs.Progress()
	if p.ParseErrors != 2 || p.Untimed != 2 || p.Events != 2 {
		t.Errorf("progress = %+v", p)
	}
}

func TestTailStreamBeforeBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 3)
	fs := newTestFileSource(t, path)
	evs, cur, _ := fs.Stream(context.Background(), Cursor{}, 10)
	if len(evs) != 0 || !cur.IsZero() {
		t.Errorf("before backfill a zero cursor yields nothing: %d %+v", len(evs), cur)
	}
	fs.pass()
	_, cur, _ = fs.Stream(context.Background(), Cursor{}, 10)
	if cur.IsZero() {
		t.Error("after backfill the cursor is positioned")
	}
	// Empty store after backfill still positions the cursor at 1.
	empty := newTestFileSource(t, filepath.Join(t.TempDir(), "none.jsonl"))
	empty.pass()
	_, cur, _ = empty.Stream(context.Background(), Cursor{}, 10)
	if cur.Seq != 1 {
		t.Errorf("empty store cursor = %+v", cur)
	}
}

func TestFileSourceCloseIdempotent(t *testing.T) {
	fs, err := NewFileSource("x", SourceConfig{Type: "jsonl", Paths: StringList{filepath.Join(t.TempDir(), "*.jsonl")}}, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fs.Close()
	fs.Close()
	select {
	case <-fs.tailer.done:
	default:
		t.Fatal("Close returned before the goroutine stopped")
	}
	if _, err := fs.Stats(context.Background(), time.Hour); err != nil {
		t.Error("store should stay readable after close")
	}
}

// TestTailCloseWaits runs the goroutine for real: once Close returns,
// appended lines are never ingested.
func TestTailCloseWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 3)
	sc := SourceConfig{Type: "jsonl", Paths: StringList{path}}
	fs, err := NewFileSource("close", sc, OpenOptions{PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fs.store.len() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	writeLines(t, path, 3, 6)
	fs.Close()
	n := fs.store.len()
	writeLines(t, path, 6, 9)
	time.Sleep(20 * time.Millisecond)
	if got := fs.store.len(); got != n {
		t.Errorf("events appended after Close: %d then %d", n, got)
	}
	// A tailer never started closes at once, and start after close is a no-op.
	idle := newTailer(sc, fs.store, nil, &counters{}, 0)
	idle.close()
	idle.start()
	idle.close()
}

// TestTailConcurrentReaders runs the tailer goroutine for real while the
// Source methods the UI calls every tick read its per-file state. Under
// the race detector this catches unguarded writes to the file table.
func TestTailConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	writeLines(t, path, 0, 50)
	sc := SourceConfig{Type: "jsonl", Paths: StringList{dir}}
	fs, err := NewFileSource("race", sc, OpenOptions{PollEvery: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		n := 50
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			writeLines(t, path, n, n+5)
			n += 5
			if i%10 == 9 {
				// Truncate so the generation moves while readers hydrate.
				if err := os.Truncate(path, 0); err != nil {
					t.Error(err)
				}
				n = 0
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	ctx := context.Background()
	deadline := time.Now().Add(400 * time.Millisecond)
	var cur Cursor
	var seen []LogEvent
	for time.Now().Before(deadline) {
		_ = fs.Progress()
		evs, next, _ := fs.Stream(ctx, cur, 100)
		if !next.IsZero() {
			cur = next
		}
		seen = append(seen, evs...)
		if len(seen) > 20 {
			seen = seen[len(seen)-20:]
		}
		for _, ev := range seen {
			if _, err := fs.Hydrate(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = fs.Stats(ctx, time.Hour)
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-writerDone
	fs.Close()
	if fs.Progress().Loading {
		t.Error("backfill should have completed")
	}
}

// TestTailPollEvery checks that the root stream_poll_ms drives the file
// poller: a configured 500 ms lands on the tailer, zero means the default,
// and a fast poller picks up an appended line without a manual pass.
func TestTailPollEvery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 1)
	cfg := Defaults()
	cfg.StreamPollMs = 500
	cfg.AddFileSource("f", []string{path})
	src, err := OpenSource(cfg, "f")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if got := src.(*FileSource).tailer.pollEvery; got != 500*time.Millisecond {
		t.Errorf("pollEvery = %v, want 500ms", got)
	}

	if got := newTailer(SourceConfig{}, nil, nil, nil, 0).pollEvery; got != defaultPollEvery {
		t.Errorf("zero config pollEvery = %v, want %v", got, defaultPollEvery)
	}

	sc := SourceConfig{Type: "jsonl", Paths: StringList{path}}
	fast, err := NewFileSource("fast", sc, OpenOptions{PollEvery: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	deadline := time.Now().Add(2 * time.Second)
	for fast.Progress().Loading && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	writeLines(t, path, 1, 2)
	for time.Now().Before(deadline) {
		if fast.store.len() == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("appended line not ingested within 2s; store has %d events", fast.store.len())
}
