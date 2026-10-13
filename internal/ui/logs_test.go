package ui

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brgmry/tailboard/internal/source"
)

func TestDedupByID(t *testing.T) {
	b := NewLogBuffer(100)
	ts := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	// Equal time and message, different non-empty IDs: both kept.
	b.Append([]source.LogEvent{
		{ID: "1:a", Time: ts, Message: "same"},
		{ID: "1:b", Time: ts, Message: "same"},
	})
	if b.Len() != 2 {
		t.Fatalf("distinct IDs should both be kept, got %d", b.Len())
	}

	// Equal IDs: deduped.
	b.Append([]source.LogEvent{{ID: "1:b", Time: ts.Add(time.Second), Message: "other"}})
	if b.Len() != 2 {
		t.Fatalf("equal IDs should dedup, got %d", b.Len())
	}

	// Empty IDs with equal (time, message): deduped.
	b.Append([]source.LogEvent{
		{Time: ts, Message: "noid"},
		{Time: ts, Message: "noid"},
	})
	if b.Len() != 3 {
		t.Fatalf("empty-ID duplicates should dedup, got %d", b.Len())
	}
}

func TestLogBufferRing(t *testing.T) {
	const max = 5
	b := NewLogBuffer(max)
	ts := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ev := func(i int) source.LogEvent {
		return source.LogEvent{ID: strconv.Itoa(i), Time: ts.Add(time.Duration(i) * time.Second), Level: "info", Message: "m" + strconv.Itoa(i)}
	}
	// Append one at a time and in batches, well past several compactions.
	next := 0
	for round := 0; round < 40; round++ {
		batch := []source.LogEvent{}
		for k := 0; k <= round%3; k++ {
			batch = append(batch, ev(next))
			next++
		}
		b.Append(batch)
		want := next
		if want > max {
			want = max
		}
		if b.Len() != want {
			t.Fatalf("round %d: Len = %d, want %d", round, b.Len(), want)
		}
		got := b.Filtered(LogFilter{})
		if len(got) != want {
			t.Fatalf("round %d: Filtered has %d, want %d", round, len(got), want)
		}
		for i, e := range got {
			if e.ID != strconv.Itoa(next-want+i) {
				t.Fatalf("round %d: order broken at %d: %q", round, i, e.ID)
			}
		}
	}
	// Dedup still sees the newest entries after wrapping.
	b.Append([]source.LogEvent{ev(next - 1)})
	if b.Len() != max || b.Filtered(LogFilter{})[max-1].ID != strconv.Itoa(next-1) {
		t.Error("a duplicate of the newest event should be dropped after wrap")
	}
	// A filtered view keeps oldest to newest too.
	f := LogFilter{Search: "m" + strconv.Itoa(next-2)}
	if got := b.Filtered(f); len(got) != 1 || got[0].ID != strconv.Itoa(next-2) {
		t.Errorf("filtered view = %+v", got)
	}
}

func TestFilterFields(t *testing.T) {
	info := source.Info{Filters: source.FilterFields{Provider: "model"}}
	f := NewLogFilter(info)
	f.Provider = "opus"
	ev := source.LogEvent{Level: "info", Fields: map[string]any{"model": "opus"}}
	if !f.Match(ev) {
		t.Error("provider filter should match on Fields[model]")
	}
	ev.Fields["model"] = "sonnet"
	if f.Match(ev) {
		t.Error("provider filter should reject a different model")
	}

	// Provider and operation match substrings, case-insensitively; pi's
	// provider field is the full model id. Trace stays exact.
	sub := NewLogFilter(source.Info{Filters: source.FilterFields{Provider: "model", Operation: "tool", Trace: "rid"}})
	full := source.LogEvent{Level: "info", Fields: map[string]any{"model": "claude-Sonnet-5-5", "tool": "bash_exec", "rid": "abc123"}}
	for _, tc := range []struct {
		provider, operation, trace string
		want                       bool
	}{
		{"sonnet", "", "", true},
		{"SONNET-5", "", "", true},
		{"opus", "", "", false},
		{"", "BASH", "", true},
		{"", "read", "", false},
		{"sonnet", "exec", "", true},
		{"", "", "abc123", true},
		{"", "", "abc", false},
		{"", "", "ABC123", false},
	} {
		sub.Provider, sub.Operation, sub.Trace = tc.provider, tc.operation, tc.trace
		if got := sub.Match(full); got != tc.want {
			t.Errorf("provider %q operation %q trace %q: match = %v, want %v", tc.provider, tc.operation, tc.trace, got, tc.want)
		}
	}

	// With no provider field the filter is a no-op even when set.
	none := NewLogFilter(source.Info{})
	none.Provider = "opus"
	if !none.Match(ev) {
		t.Error("provider filter without a field must be ignored")
	}

	// The help overlay omits p when the field is empty.
	m := newTestModel(t, &fakeSource{info: source.Info{Kind: "jsonl", Name: "x"}})
	m.width, m.height, m.ready = 120, 40, true
	m.mode = ModeHelp
	help := m.View()
	if strings.Contains(help, "filter by provider") || strings.Contains(stripANSI(help), "\n p ") {
		t.Error("help should omit the p row when no provider field is configured")
	}
	withP := newTestModel(t, &fakeSource{info: source.Info{Kind: "jsonl", Name: "x", Filters: source.FilterFields{Provider: "model"}}})
	withP.width, withP.height, withP.ready = 120, 40, true
	withP.mode = ModeHelp
	if !strings.Contains(stripANSI(withP.View()), "filter by model") {
		t.Error("help should list p with the mapped field name")
	}
}

func TestFilterSeedAndReset(t *testing.T) {
	f := NewLogFilter(source.Info{HideLevels: []string{"debug"}})
	if f.Active() {
		t.Error("seeded filter is not active")
	}
	if f.Match(source.LogEvent{Level: "debug"}) {
		t.Error("seeded debug should be hidden")
	}
	f.HideLevels["debug"] = false
	f.Search = "x"
	if !f.Active() {
		t.Error("changed filter should be active")
	}
	r := f.Reset()
	if r.Active() || !r.HideLevels["debug"] || r.Search != "" {
		t.Errorf("reset should restore the seed: %+v", r)
	}
}

func TestFilterActive(t *testing.T) {
	info := source.Info{HideLevels: []string{"debug"}, Filters: source.FilterFields{Provider: "model", Operation: "op", Trace: "trace"}}
	if f := NewLogFilter(info); f.Active() {
		t.Fatal("seed state should not be active")
	}
	if f := NewLogFilter(info).Reset(); f.Active() {
		t.Fatal("reset state should not be active")
	}
	changes := map[string]func(f *LogFilter){
		"reveal seeded level": func(f *LogFilter) { f.HideLevels["debug"] = false },
		"hide another level":  func(f *LogFilter) { f.HideLevels["info"] = true },
		"search":              func(f *LogFilter) { f.Search = "x" },
		"client":              func(f *LogFilter) { f.Client = "acme" },
		"provider":            func(f *LogFilter) { f.Provider = "opus" },
		"operation":           func(f *LogFilter) { f.Operation = "chat" },
		"trace":               func(f *LogFilter) { f.Trace = "abc" },
	}
	for name, change := range changes {
		f := NewLogFilter(info)
		change(&f)
		if !f.Active() {
			t.Errorf("%s: filter should be active", name)
		}
	}
	f := NewLogFilter(info)
	f.HideLevels["info"] = false
	if f.Active() {
		t.Error("an explicit false for an unseeded level is not a change")
	}
	if n := testing.AllocsPerRun(100, func() { _ = f.Active() }); n != 0 {
		t.Errorf("Active allocates %v times per call", n)
	}
}

func TestFieldText(t *testing.T) {
	vals := []any{"Mixed", 3.0, 1e6, 1e21, 0.000012, -2.5, math.NaN(), math.Inf(1), float32(0.1),
		42, int64(-7), int32(9), uint64(1 << 63), uint32(5), uint(6), true, nil, []any{1, "a"}}
	for _, v := range vals {
		if got, want := fieldText(v), fmt.Sprint(v); got != want {
			t.Errorf("fieldText(%#v) = %q, want %q", v, got, want)
		}
	}
	ev := source.LogEvent{Level: "info", Message: "nothing here", Fields: map[string]any{"status": 503.0, "user": "Alice"}}
	for _, search := range []string{"503", "ALICE"} {
		if !(LogFilter{Search: search}).Match(ev) {
			t.Errorf("search %q should match a field value", search)
		}
	}
}

func TestStatusDescriptionLabels(t *testing.T) {
	info := source.Info{DisplayPrefix: "fields.", Filters: source.FilterFields{Provider: "fields.provider", Operation: "fields.operation", Trace: "fields.requestId"}}
	f := NewLogFilter(info)
	f.Provider = "openai"
	f.Operation = "chat"
	f.Trace = "0123456789abcdef"
	f.HideLevels["info"] = true
	got := f.StatusDescription(info)
	for _, want := range []string{"¬INFO", "provider:openai", "operation:chat", "requestId:01234567"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %q", got, want)
		}
	}
}

func TestDebugToggleKey(t *testing.T) {
	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 120, 40, true
	if m.filter.HideLevels["debug"] {
		t.Fatal("debug visible by default for a source without hide_levels")
	}
	next, _ := m.handleKey(keyPress("d"))
	m = next.(Model)
	if !m.filter.HideLevels["debug"] {
		t.Error("d should hide debug")
	}
	next, _ = m.handleKey(keyPress("d"))
	m = next.(Model)
	if m.filter.HideLevels["debug"] {
		t.Error("d again should show debug")
	}
	m.mode = ModeHelp
	if !strings.Contains(stripANSI(m.View()), "e / w / i / d") {
		t.Error("help should list the d key")
	}

	// A source that hides debug at startup: esc resets to that seed.
	info := fullInfo()
	info.HideLevels = []string{"debug"}
	m = newTestModel(t, &fakeSource{info: info})
	next, _ = m.handleKey(keyPress("d"))
	m = next.(Model)
	if m.filter.HideLevels["debug"] {
		t.Error("d should reveal seeded-hidden debug")
	}
	next, _ = m.handleKey(keyPress("esc"))
	m = next.(Model)
	if !m.filter.HideLevels["debug"] {
		t.Error("esc should restore the hide_levels seed")
	}
}

func BenchmarkFiltered(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	clients := []string{"acme", "globex", "initech", "umbrella"}
	levels := []string{"info", "info", "info", "warn", "error", "debug"}
	buf := NewLogBuffer(2000)
	now := time.Now()
	evs := make([]source.LogEvent, 2000)
	for i := range evs {
		evs[i] = source.LogEvent{
			ID:      strconv.Itoa(i),
			Time:    now.Add(time.Duration(i) * time.Millisecond),
			Level:   levels[rng.Intn(len(levels))],
			Message: fmt.Sprintf("GET /api/orders %d %dms", 200+rng.Intn(3)*100, rng.Intn(200)),
			Client:  clients[rng.Intn(len(clients))],
			Fields:  map[string]any{"userId": fmt.Sprintf("u%05d", rng.Intn(50000)), "region": "eu-west-1", "durationMs": rng.ExpFloat64() * 40},
		}
	}
	buf.Append(evs)
	f := NewLogFilter(source.Info{HideLevels: []string{"debug"}})
	f.Search = "U01"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Filtered(f)
	}
}
