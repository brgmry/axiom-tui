package main

import (
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
