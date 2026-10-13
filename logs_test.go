package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDedupByID(t *testing.T) {
	b := NewLogBuffer(100)
	ts := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	// Equal time and message, different non-empty IDs: both kept.
	b.Append([]LogEvent{
		{ID: "1:a", Time: ts, Message: "same"},
		{ID: "1:b", Time: ts, Message: "same"},
	})
	if b.Len() != 2 {
		t.Fatalf("distinct IDs should both be kept, got %d", b.Len())
	}

	// Equal IDs: deduped.
	b.Append([]LogEvent{{ID: "1:b", Time: ts.Add(time.Second), Message: "other"}})
	if b.Len() != 2 {
		t.Fatalf("equal IDs should dedup, got %d", b.Len())
	}

	// Empty IDs with equal (time, message): deduped.
	b.Append([]LogEvent{
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
	ev := func(i int) LogEvent {
		return LogEvent{ID: strconv.Itoa(i), Time: ts.Add(time.Duration(i) * time.Second), Level: "info", Message: "m" + strconv.Itoa(i)}
	}
	// Append one at a time and in batches, well past several compactions.
	next := 0
	for round := 0; round < 40; round++ {
		batch := []LogEvent{}
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
	b.Append([]LogEvent{ev(next - 1)})
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
	info := SourceInfo{Filters: FilterFields{Provider: "model"}}
	f := NewLogFilter(info)
	f.Provider = "opus"
	ev := LogEvent{Level: "info", Fields: map[string]any{"model": "opus"}}
	if !f.Match(ev) {
		t.Error("provider filter should match on Fields[model]")
	}
	ev.Fields["model"] = "sonnet"
	if f.Match(ev) {
		t.Error("provider filter should reject a different model")
	}

	// With no provider field the filter is a no-op even when set.
	none := NewLogFilter(SourceInfo{})
	none.Provider = "opus"
	if !none.Match(ev) {
		t.Error("provider filter without a field must be ignored")
	}

	// The help overlay omits p when the field is empty.
	m := newTestModel(t, &fakeSource{info: SourceInfo{Kind: "jsonl", Name: "x"}})
	m.width, m.height, m.ready = 120, 40, true
	m.mode = ModeHelp
	help := m.View()
	if strings.Contains(help, "filter by provider") || strings.Contains(stripANSI(help), "\n p ") {
		t.Error("help should omit the p row when no provider field is configured")
	}
	withP := newTestModel(t, &fakeSource{info: SourceInfo{Kind: "jsonl", Name: "x", Filters: FilterFields{Provider: "model"}}})
	withP.width, withP.height, withP.ready = 120, 40, true
	withP.mode = ModeHelp
	if !strings.Contains(stripANSI(withP.View()), "filter by model") {
		t.Error("help should list p with the mapped field name")
	}
}

func TestFilterSeedAndReset(t *testing.T) {
	f := NewLogFilter(SourceInfo{HideLevels: []string{"debug"}})
	if f.Active() {
		t.Error("seeded filter is not active")
	}
	if f.Match(LogEvent{Level: "debug"}) {
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

func TestStatusDescriptionLabels(t *testing.T) {
	info := SourceInfo{DisplayPrefix: "fields.", Filters: FilterFields{Provider: "fields.provider", Operation: "fields.operation", Trace: "fields.requestId"}}
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
