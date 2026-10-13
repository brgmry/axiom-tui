package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestPiSource builds a pi source over dir without the goroutine.
func newTestPiSource(t *testing.T, dir string, orphanAfter time.Duration) *PiSource {
	t.Helper()
	sc := SourceConfig{Type: "pi", Paths: StringList{dir}}
	if orphanAfter > 0 {
		sc.OrphanAfter = Duration(orphanAfter)
	}
	ps, err := newPiSource("pi", sc, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ps.Close() })
	return ps
}

func (p *PiSource) pass() {
	p.tailer.discoverOnce()
	p.tailer.pollOnce()
}

func allEvents(t *testing.T, src Source) []LogEvent {
	t.Helper()
	evs, _, err := src.Stream(context.Background(), Cursor{Seq: 1}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func findEvent(evs []LogEvent, prefix string) (LogEvent, bool) {
	for _, e := range evs {
		if strings.HasPrefix(e.Message, prefix) {
			return e, true
		}
	}
	return LogEvent{}, false
}

func TestPiNormalise(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-2 * time.Minute)
	writePiFile(t, dir, "normal.jsonl", normalSession(base))
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	if p := ps.Progress(); p.ParseErrors != 0 || p.UnreadableFiles != 0 || p.Untimed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	// Entry kinds live in event.kind, never in the route.
	if rows := ps.store.topRoutes(time.Now(), 10, time.Hour); len(rows) != 0 {
		t.Errorf("pi store should have no routes, got %v", rows)
	}
	evs := allEvents(t, ps)
	if len(evs) != len(normalSession(base)) {
		t.Fatalf("one line, one event: got %d events for %d lines", len(evs), len(normalSession(base)))
	}
	for _, e := range evs {
		if e.Client != "crm" {
			t.Errorf("agent should be the cwd basename, got %q on %q", e.Client, e.Message)
		}
		if e.Fields["session"] != "sess-normal" {
			t.Errorf("every event carries the session: %v", e.Fields)
		}
	}

	type row struct {
		prefix string
		level  string
		fields []string
	}
	rows := []row{
		{"session sess-normal cwd /Users/me/dev/coreOS/life/crm", "debug", nil},
		{"session named crm", "info", []string{"name"}},
		{"model pi-os/auto", "info", []string{"provider", "modelId"}},
		{"thinking_level_change", "debug", nil},
		{"system prompt", "debug", nil},
		{"please list the files", "info", []string{"role"}},
		{"router deep by haiku", "info", []string{"tier", "by", "router"}},
		{"-> bash, read", "info", []string{"model", "provider", "stopReason", "tokens", "in", "out", "cacheRead", "cacheWrite", "toolCalls", "responseId", "cost"}},
		{"bash ok: file-a", "info", []string{"tool", "toolCallId", "isError"}},
		{"read error: permission denied", "error", []string{"tool", "toolCallId", "isError"}},
		{"Here are the files.", "info", []string{"model", "tokens"}},
		{"compaction, 180000 tokens before", "warn", []string{"tokensBefore", "fromHook", "cost"}},
		{"cache_warm claude-opus-5-5 $0.0200", "info", []string{"model", "provider", "kind", "cost"}},
		{"om.observations.recorded", "debug", []string{"customType"}},
		{"shell-function", "debug", nil},
		{"label", "debug", nil},
	}
	for _, r := range rows {
		ev, ok := findEvent(evs, r.prefix)
		if !ok {
			t.Errorf("no event with message prefix %q", r.prefix)
			continue
		}
		if ev.Level != r.level {
			t.Errorf("%q level = %s, want %s", r.prefix, ev.Level, r.level)
		}
		for _, f := range r.fields {
			if _, ok := ev.Fields[f]; !ok {
				t.Errorf("%q missing field %s: %v", r.prefix, f, ev.Fields)
			}
		}
	}
	// Personal content of om.* entries is not retained.
	om, _ := findEvent(evs, "om.observations.recorded")
	if len(om.Fields) != 2 {
		t.Errorf("om entry should keep only customType and session: %v", om.Fields)
	}

	// Cost and duration values land in the right slots.
	st := ps.store
	var llmDur, toolDur []float64
	var costs []float64
	st.scan(func(e *event) {
		if !math.IsNaN(e.duration[0]) {
			llmDur = append(llmDur, e.duration[0])
		}
		if !math.IsNaN(e.duration[1]) {
			toolDur = append(toolDur, e.duration[1])
		}
		if !math.IsNaN(e.cost) {
			costs = append(costs, e.cost)
		}
	})
	if len(llmDur) != 3 || llmDur[0] != 4829 {
		t.Errorf("llm durations = %v", llmDur)
	}
	if len(toolDur) != 2 || toolDur[0] != 16 || toolDur[1] != 62 {
		t.Errorf("tool durations = %v", toolDur)
	}
	if len(costs) != 5 {
		t.Errorf("costs = %v (assistant x3, compaction, usage)", costs)
	}
	stats, _ := ps.Stats(context.Background(), time.Hour)
	if len(stats.Durations) != 2 || stats.Durations[0].Field != "llm" || stats.Durations[1].Field != "tool" {
		t.Errorf("stats durations = %+v", stats.Durations)
	}
	if stats.Durations[0].Max != 4829 || stats.Durations[1].Max != 62 {
		t.Errorf("llm and tool land in different slots: %+v", stats.Durations)
	}
	info := ps.Info()
	if info.Kind != "pi" || info.GroupLabel != "agent" || info.Filters.Trace != "session" || info.Filters.Provider != "model" || info.Filters.Operation != "tool" {
		t.Errorf("info = %+v", info)
	}
	if len(info.HideLevels) != 1 || info.HideLevels[0] != "debug" {
		t.Errorf("hide_levels default = %v", info.HideLevels)
	}
}

func TestPiCostSources(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-5 * time.Minute)
	t0 := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	writePiFile(t, dir, "s.jsonl", []piEntry{
		piHeader("s", "/x/agent-a", t0(0)),
		piAssistant("a1", "claude-opus-5-5", "hi", nil, piUsage{total: 100, cost: 0.10}, "stop", 10, t0(1)),
		piUsageEntry("u1", "cache_warm", "claude-sonnet-5-5", 0.02, t0(2)),
		piCompaction("c1", 1000, true, 0.03, t0(3)),
		piCompaction("c2", 1000, false, 0, t0(4)),
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	rep, _ := ps.Cost(context.Background(), 24*time.Hour)
	if !approx(rep.Total, 0.15) {
		t.Errorf("total = %v, want 0.15", rep.Total)
	}
	if !rep.HasSummary || !approx(rep.Today, 0.15) || !approx(rep.TrailingHour, 0.15) {
		t.Errorf("summary = %+v", rep)
	}
	if len(rep.ByModel) != 2 || rep.ByModel[0].Client != "claude-opus-5-5" || !approx(rep.ByModel[0].Dollars, 0.10) || !approx(rep.ByModel[1].Dollars, 0.02) {
		t.Errorf("by model = %v", rep.ByModel)
	}
	if len(rep.ByGroup) != 1 || rep.ByGroup[0].Client != "agent-a" {
		t.Errorf("by group = %v", rep.ByGroup)
	}
}

func TestPiOrphans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	call := func(age time.Duration) []piEntry {
		at := time.Now().Add(-age)
		return []piEntry{
			piHeader("s", "/x/agent-a", at.Add(-time.Second)),
			piAssistant("a1", "claude-opus-5-5", "", []piToolCall{{"call-1", "bash"}}, piUsage{cost: 0.01}, "toolUse", 10, at),
		}
	}
	orphans := func(ps *PiSource) int {
		agents, _ := ps.Agents(context.Background(), 24*time.Hour)
		for _, a := range agents {
			if a.Name == "agent-a" {
				return a.OrphanCalls
			}
		}
		return -1
	}

	writePiFile(t, dir, "s.jsonl", call(10*time.Minute))
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	if n := orphans(ps); n != 1 {
		t.Errorf("10 min old call with no result: orphans = %d, want 1", n)
	}

	os.WriteFile(path, []byte(""), 0o644)
	ps.pass() // truncation clears the side table
	writePiFile(t, dir, "s.jsonl", call(30*time.Second))
	ps.pass()
	if n := orphans(ps); n != 0 {
		t.Errorf("30 s old call: orphans = %d, want 0", n)
	}

	os.WriteFile(path, []byte(""), 0o644)
	ps.pass()
	writePiFile(t, dir, "s.jsonl", call(10*time.Minute))
	ps.pass()
	appendPiEntries(t, path, []piEntry{piToolResult("t1", "call-1", "bash", "ok", false, 5, time.Now())})
	ps.pass()
	if n := orphans(ps); n != 0 {
		t.Errorf("matched call: orphans = %d, want 0", n)
	}

	// Truncate and rewrite without the call: the side table forgets it.
	os.WriteFile(path, []byte(""), 0o644)
	ps.pass()
	writePiFile(t, dir, "s.jsonl", call(10*time.Minute))
	ps.pass()
	if n := orphans(ps); n != 1 {
		t.Fatalf("precondition: orphans = %d, want 1", n)
	}
	os.WriteFile(path, []byte(piLine(piHeader("s", "/x/agent-a", time.Now()))+"\n"), 0o644)
	ps.pass()
	if n := orphans(ps); n != 0 {
		t.Errorf("after truncation without the call: orphans = %d, want 0", n)
	}
}

func TestPiContext(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Minute)
	t0 := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	older := writePiFile(t, dir, "old.jsonl", []piEntry{
		piHeader("s-old", "/x/agent-a", t0(0)),
		piAssistant("a1", "claude-opus-5-5", "hi", nil, piUsage{total: 90000, cost: 0.1}, "stop", 10, t0(1)),
	})
	newer := writePiFile(t, dir, "new.jsonl", []piEntry{
		piHeader("s-new", "/x/agent-a", t0(100)),
		piAssistant("a1", "claude-opus-5-5", "hi", nil, piUsage{total: 68224, cost: 0.1}, "stop", 10, t0(101)),
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	ctxTokens := func() int64 {
		agents, _ := ps.Agents(context.Background(), 24*time.Hour)
		if len(agents) != 1 {
			t.Fatalf("agents = %v", agents)
		}
		return agents[0].ContextTokens
	}
	if got := ctxTokens(); got != 68224 {
		t.Errorf("context tokens = %d, want 68224 from the newest file", got)
	}
	agents, _ := ps.Agents(context.Background(), 24*time.Hour)
	if agents[0].SessionFile != filepath.Base(newer) {
		t.Errorf("session file = %s, want %s", agents[0].SessionFile, filepath.Base(newer))
	}
	// An older file getting a newer assistant message does not win unless
	// its newest event is newer.
	appendPiEntries(t, older, []piEntry{piAssistant("a2", "claude-opus-5-5", "x", nil, piUsage{total: 1, cost: 0}, "stop", 1, t0(50))})
	ps.pass()
	if got := ctxTokens(); got != 68224 {
		t.Errorf("context tokens = %d, want 68224", got)
	}
	// Compaction then a new assistant message in the newest file.
	appendPiEntries(t, newer, []piEntry{
		piCompaction("c1", 68224, false, 0, t0(200)),
		piAssistant("a2", "claude-opus-5-5", "after", nil, piUsage{total: 12000, cost: 0.1}, "stop", 10, t0(201)),
	})
	ps.pass()
	if got := ctxTokens(); got != 12000 {
		t.Errorf("context tokens after compaction = %d, want 12000", got)
	}
}

func TestPiFallback(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-5 * time.Minute)
	t0 := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	writePiFile(t, dir, "s.jsonl", []piEntry{
		piHeader("s", "/x/agent-a", t0(0)),
		piRouter("r1", "deep", "haiku", t0(1)),
		piRouter("r2", "deep", "fallback", t0(2)),
		piRouter("r3", "fast", "haiku", t0(3)),
		piRouter("r4", "deep", "haiku", t0(4)),
		piAssistant("a1", "claude-sonnet-5-5", "x", nil, piUsage{cost: 0.01}, "stop", 1, t0(5)),
		piAssistant("a2", "claude-opus-5-5", "y", nil, piUsage{cost: 0.30}, "stop", 1, t0(6)),
		piAssistant("a3", "claude-sonnet-5-5", "z", nil, piUsage{cost: 0.02}, "stop", 1, t0(7)),
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	agents, _ := ps.Agents(context.Background(), time.Hour)
	if len(agents) != 1 {
		t.Fatalf("agents = %v", agents)
	}
	a := agents[0]
	if a.FallbackN != 1 || a.FallbackD != 4 {
		t.Errorf("fallback = %d/%d, want 1/4", a.FallbackN, a.FallbackD)
	}
	if a.TopModel != "claude-opus-5-5" || len(a.ModelMix) != 2 || !approx(a.ModelMix[1].Dollars, 0.03) {
		t.Errorf("model mix = %s %v", a.TopModel, a.ModelMix)
	}
	if !approx(a.SpendToday, 0.33) || !approx(a.SpendHour, 0.33) {
		t.Errorf("spend = %v / %v", a.SpendToday, a.SpendHour)
	}
	if a.LastSeen.IsZero() {
		t.Error("last seen should be set")
	}
	// Router events are warn when by fallback, info otherwise.
	evs := allEvents(t, ps)
	fb, _ := findEvent(evs, "router deep by fallback")
	ok, _ := findEvent(evs, "router fast by haiku")
	if fb.Level != "warn" || ok.Level != "info" {
		t.Errorf("router levels = %s / %s", fb.Level, ok.Level)
	}
	// The fallback count reads the typed flag, not the "by" field.
	var flagged, routers int
	ps.store.scan(func(e *event) {
		if e.kind == piKindRouter {
			routers++
			if e.flags&piFlagFallback != 0 {
				flagged++
			}
		}
	})
	if flagged != 1 || routers != 4 {
		t.Errorf("router flags = %d/%d, want 1/4", flagged, routers)
	}
}

func TestPiIdentity(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-5 * time.Minute)
	writePiFile(t, dir, "a.jsonl", []piEntry{
		piHeader("sess-a", "/Users/me/dev/coreOS/life/crm", base),
		piUser("u1", "hi", base.Add(time.Second)),
	})
	writePiFile(t, dir, "b.jsonl", []piEntry{
		piHeader("sess-b", "/tmp/other/crm", base.Add(2*time.Second)),
		piUser("u1", "hello", base.Add(3*time.Second)),
	})
	// Not a header first: skipped and counted.
	writePiFile(t, dir, "c.jsonl", []piEntry{
		piUser("u1", "orphan line", base.Add(4*time.Second)),
		piHeader("sess-c", "/x/y", base.Add(5*time.Second)),
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	agents, _ := ps.Agents(context.Background(), time.Hour)
	if len(agents) != 1 || agents[0].Name != "crm" {
		t.Errorf("two cwds with the same basename roll up: %v", agents)
	}
	evs := allEvents(t, ps)
	sessions := map[string]bool{}
	for _, e := range evs {
		sessions[e.Fields["session"].(string)] = true
	}
	if !sessions["sess-a"] || !sessions["sess-b"] || len(sessions) != 2 {
		t.Errorf("sessions = %v", sessions)
	}
	if p := ps.Progress(); p.UnreadableFiles != 1 {
		t.Errorf("unreadable_files = %d, want 1", p.UnreadableFiles)
	}
	if len(evs) != 4 {
		t.Errorf("events from the bad file must be skipped: %d", len(evs))
	}
}

func TestPiBigLine(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-5 * time.Minute)
	writePiFile(t, dir, "s.jsonl", []piEntry{
		piHeader("s", "/x/agent-a", base),
		piAssistant("a1", "claude-opus-5-5", "", []piToolCall{{"call-big", "bash"}}, piUsage{cost: 0.01}, "toolUse", 10, base.Add(time.Second)),
		bigToolResult("call-big", base.Add(2*time.Second)),
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	if p := ps.Progress(); p.SkippedLines != 0 || p.ParseErrors != 0 {
		t.Fatalf("progress = %+v", p)
	}
	evs := allEvents(t, ps)
	big, ok := findEvent(evs, "bash error: xxx")
	if !ok {
		t.Fatalf("big tool result not found: %v", messages(evs))
	}
	if len(big.Message) > 512 {
		t.Errorf("message should be capped: %d bytes", len(big.Message))
	}
	if big.Level != "error" || big.Fields["isError"] != "true" {
		t.Errorf("isError after content should be read: %s %v", big.Level, big.Fields)
	}
	var toolDur float64
	ps.store.scan(func(e *event) {
		if e.kind == piKindToolResult {
			toolDur = e.duration[1]
		}
	})
	if toolDur != 77 {
		t.Errorf("durationMs after content = %v, want 77", toolDur)
	}
	raw, err := ps.Hydrate(context.Background(), big)
	if err != nil {
		t.Fatal(err)
	}
	content := raw["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if len(content) != 1500*1024 {
		t.Errorf("hydrate should return the full content: %d bytes", len(content))
	}
	agents, _ := ps.Agents(context.Background(), time.Hour)
	if agents[0].ToolErrors != 1 || agents[0].ToolCalls != 1 || agents[0].OrphanCalls != 0 {
		t.Errorf("agent stats = %v", agents[0])
	}
}

func TestPiHeaderOnlyAndUnknown(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Minute)
	writePiFile(t, dir, "empty.jsonl", headerOnlySession(base))
	writePiFile(t, dir, "weird.jsonl", []piEntry{
		piHeader("w", "/x/weird", base),
		piUnknown("z1", "branch_summary", base.Add(time.Second)),
		piUnknown("z2", "something_new", base.Add(2*time.Second)),
		{"type": "message", "id": "m9", "timestamp": piTS(base.Add(3 * time.Second)), "message": piEntry{"role": "mystery"}},
	})
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	if p := ps.Progress(); p.UnreadableFiles != 0 || p.ParseErrors != 0 || p.Events != 5 {
		t.Errorf("progress = %+v", p)
	}
	agents, _ := ps.Agents(context.Background(), time.Hour)
	names := []string{}
	for _, a := range agents {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "Documents,weird" {
		t.Errorf("agents = %v", names)
	}
	evs := allEvents(t, ps)
	for _, want := range []string{"branch_summary", "something_new", "message mystery"} {
		if ev, ok := findEvent(evs, want); !ok || ev.Level != "debug" {
			t.Errorf("unknown entry %q should be a debug event", want)
		}
	}
}

func TestPiAbortedSession(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Minute)
	writePiFile(t, dir, "ab.jsonl", abortedSession(base))
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	evs := allEvents(t, ps)
	ab, ok := findEvent(evs, "This operation was aborted")
	if !ok || ab.Level != "error" || ab.Fields["cost"] != "$0.0700" {
		t.Errorf("aborted turn should be an error event with its cost: %+v", ab)
	}
	agents, _ := ps.Agents(context.Background(), time.Hour)
	if len(agents) != 1 || agents[0].Name != "pi-os" || agents[0].OrphanCalls != 1 || agents[0].FallbackN != 1 || !approx(agents[0].SpendToday, 0.07) {
		t.Errorf("agent = %v", agents)
	}
}

// TestPiConcurrentAgents drives the tailer goroutine against a growing
// session while Agents, Stream and Hydrate read the side table, as the
// UI does on every refresh. Run with -race.
func TestPiConcurrentAgents(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Minute)
	path := writePiFile(t, dir, "s1.jsonl", normalSession(base))
	writePiFile(t, dir, "s2.jsonl", abortedSession(base))
	sc := SourceConfig{Type: "pi", Paths: StringList{dir}}
	sc.pollEvery = 5 * time.Millisecond
	ps, err := NewPiSource("pi", sc)
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ts := time.Now()
			id := fmt.Sprintf("x%d", i)
			appendPiEntries(t, path, []piEntry{
				piAssistant("a"+id, "claude-opus-5-5", "", []piToolCall{{"call-" + id, "bash"}}, piUsage{total: 30000 + i, cost: 0.01}, "toolUse", 100, ts),
				piToolResult("t"+id, "call-"+id, "bash", "ok", i%3 == 0, 5, ts),
			})
			time.Sleep(2 * time.Millisecond)
		}
	}()

	ctx := context.Background()
	deadline := time.Now().Add(400 * time.Millisecond)
	var cur Cursor
	var last []LogEvent
	for time.Now().Before(deadline) {
		if _, err := ps.Agents(ctx, time.Hour); err != nil {
			t.Fatal(err)
		}
		_ = ps.Progress()
		evs, next, _ := ps.Stream(ctx, cur, 50)
		if !next.IsZero() {
			cur = next
		}
		if len(evs) > 0 {
			last = evs
		}
		for _, ev := range last {
			if _, err := ps.Hydrate(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = ps.Cost(ctx, time.Hour)
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-writerDone
	ps.Close()
	<-ps.tailer.done
	agents, _ := ps.Agents(ctx, time.Hour)
	if len(agents) != 2 {
		t.Errorf("want 2 agents, got %v", agents)
	}
}

// TestPiEventIDs: the event ID is "<file index>:<entry id>", the header
// line is "<file index>:session", and ids survive a truncation rewrite.
func TestPiEventIDs(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Minute)
	path := writePiFile(t, dir, "s1.jsonl", normalSession(base))
	ps := newTestPiSource(t, dir, 0)
	ps.pass()
	evs := allEvents(t, ps)
	ids := map[string]bool{}
	for _, e := range evs {
		if ids[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		ids[e.ID] = true
	}
	if !ids["0:session"] {
		t.Errorf("header id should be 0:session: %v", ids)
	}
	for _, want := range []string{"0:a1", "0:t2", "0:c1", "0:us1", "0:om1", "0:lb1"} {
		if !ids[want] {
			t.Errorf("missing id %s", want)
		}
	}
	hdr, _ := findEvent(evs, "session sess-normal")
	if hdr.ID != "0:session" {
		t.Errorf("header event id = %s", hdr.ID)
	}

	// Truncate and rewrite a shorter session: the store drops the file's
	// events and the ids re-derive from the new entries.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	appendPiEntries(t, path, abortedSession(base))
	ps.pass()
	evs = allEvents(t, ps)
	if len(evs) != len(abortedSession(base)) {
		t.Fatalf("after rewrite got %d events", len(evs))
	}
	got := map[string]bool{}
	for _, e := range evs {
		got[e.ID] = true
	}
	for _, want := range []string{"0:session", "0:u1", "0:r1", "0:a1"} {
		if !got[want] {
			t.Errorf("after rewrite missing id %s: %v", want, got)
		}
	}
	if ps.store.len() != len(ps.store.ids) {
		t.Errorf("id side table out of step: %d events, %d ids", ps.store.len(), len(ps.store.ids))
	}

	// Generic sources keep the ref-derived id.
	gen := newTestFileSource(t, filepath.Join(dir, "g.jsonl"))
	writeLines(t, filepath.Join(dir, "g.jsonl"), 0, 2)
	gen.pass()
	for _, e := range allEvents(t, gen) {
		if !strings.HasPrefix(e.ID, "0:0:") {
			t.Errorf("generic id = %s", e.ID)
		}
	}
	if gen.store.ids != nil {
		t.Error("generic store should not keep ids")
	}
}

// gatedHandler wraps a lineHandler so a test can hold reset open at the
// moment it is entered, then let it continue.
type gatedHandler struct {
	lineHandler
	entered chan struct{}
	release chan struct{}
}

func (g *gatedHandler) reset(file int32) {
	g.entered <- struct{}{}
	<-g.release
	g.lineHandler.reset(file)
}

// TestPiNoLockInversion reproduces the interleaving that used to deadlock:
// discovery re-activates a file that went away and came back with a new
// inode (the tailer calls the handler's reset) while an Agents refresh is
// in flight (the handler calls back into the tailer for a file path). If
// either side holds its own lock while taking the other's, the two block
// each other for good. The race detector cannot see lock ordering, so
// this test drives it.
func TestPiNoLockInversion(t *testing.T) {
	dir := t.TempDir()
	path := writePiFile(t, dir, "a.jsonl", normalSession(time.Now().Add(-time.Minute)))
	ps := newTestPiSource(t, dir, 0)
	ps.pass()

	tl := ps.tailer
	tl.mu.Lock()
	f := tl.files[0]
	tl.mu.Unlock()
	if !f.idOK {
		t.Skip("no file identity on this platform")
	}

	g := &gatedHandler{lineHandler: tl.handler, entered: make(chan struct{}), release: make(chan struct{})}
	tl.handler = g
	tl.update(func() {
		f.gone = true
		f.ino++ // the path now names a different file
	})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	considerDone := make(chan struct{})
	go func() {
		tl.consider(path, fi, time.Time{})
		close(considerDone)
	}()
	<-g.entered // the tailer is inside reset

	agentsDone := make(chan error, 1)
	go func() {
		_, err := ps.Agents(context.Background(), 24*time.Hour)
		agentsDone <- err
	}()
	// Let Agents reach its own lock before the tailer is released.
	time.Sleep(50 * time.Millisecond)
	close(g.release)

	deadline := time.After(3 * time.Second)
	for _, wait := range []struct {
		name string
		fn   func() bool
	}{
		{"Agents", func() bool {
			select {
			case err := <-agentsDone:
				if err != nil {
					t.Error(err)
				}
				return true
			case <-deadline:
				return false
			}
		}},
		{"consider", func() bool {
			select {
			case <-considerDone:
				return true
			case <-deadline:
				return false
			}
		}},
	} {
		if !wait.fn() {
			t.Fatalf("deadlock: %s did not return", wait.name)
		}
	}
}
