package main

import (
	"fmt"
	"strings"
)

// LogBuffer is a bounded ring of LogEvents. New events push onto the tail;
// once the buffer is full, the oldest events drop silently. Size is set
// from config (default 2000) — big enough to scroll through a few minutes
// of high-volume streams, small enough to keep the TUI responsive.
type LogBuffer struct {
	items []LogEvent
	max   int
}

func NewLogBuffer(max int) *LogBuffer {
	return &LogBuffer{items: make([]LogEvent, 0, max), max: max}
}

// Append adds new events. Dedups by (_time, message) — Axiom polling can
// return duplicates when events arrive at the exact moment of a poll.
func (b *LogBuffer) Append(events []LogEvent) {
	for _, ev := range events {
		if b.isDup(ev) {
			continue
		}
		if len(b.items) >= b.max {
			// Drop the oldest in-place without reslicing the backing array.
			copy(b.items, b.items[1:])
			b.items[len(b.items)-1] = ev
		} else {
			b.items = append(b.items, ev)
		}
	}
}

// isDup checks the last ~8 entries for the same (time, message) — good enough
// to suppress poll-boundary duplicates without scanning the whole buffer.
func (b *LogBuffer) isDup(ev LogEvent) bool {
	start := len(b.items) - 8
	if start < 0 {
		start = 0
	}
	for i := start; i < len(b.items); i++ {
		if b.items[i].Time.Equal(ev.Time) && b.items[i].Message == ev.Message {
			return true
		}
	}
	return false
}

// Len returns the number of buffered events.
func (b *LogBuffer) Len() int { return len(b.items) }

// All returns the internal slice — callers must treat it as read-only.
func (b *LogBuffer) All() []LogEvent { return b.items }

// ─── Filtering ───────────────────────────────────────────────────────────────

// LogFilter captures the active filter state for the log stream. Zero value
// means "show everything" — filters are opt-in.
//
// HideLevels is a map not a slice so toggling is O(1). Search and Client are
// plain substring matches (case-insensitive) — regex is a pit we don't need
// to fall into yet.
type LogFilter struct {
	HideLevels map[string]bool
	Search     string
	Client     string
}

// Active reports whether any filter is in effect.
func (f LogFilter) Active() bool {
	for _, v := range f.HideLevels {
		if v {
			return true
		}
	}
	return f.Search != "" || f.Client != ""
}

// Match returns true when ev passes every active filter.
func (f LogFilter) Match(ev LogEvent) bool {
	if f.HideLevels[ev.Level] {
		return false
	}
	if f.Search != "" {
		needle := strings.ToLower(f.Search)
		if !strings.Contains(strings.ToLower(ev.Message), needle) {
			// Also match against fields so `/clientX` works even when the
			// client name isn't in the message.
			if !fieldsContain(ev.Fields, needle) {
				return false
			}
		}
	}
	if f.Client != "" {
		if !strings.EqualFold(ev.Client, f.Client) {
			return false
		}
	}
	return true
}

func fieldsContain(fields map[string]any, needle string) bool {
	for _, v := range fields {
		if strings.Contains(strings.ToLower(fmt.Sprint(v)), needle) {
			return true
		}
	}
	return false
}

// Filtered returns the subset of events that pass f. Preserves order.
// Never returns nil — callers can safely iterate over the result.
func (b *LogBuffer) Filtered(f LogFilter) []LogEvent {
	if !f.Active() {
		return b.items
	}
	out := make([]LogEvent, 0, len(b.items))
	for _, ev := range b.items {
		if f.Match(ev) {
			out = append(out, ev)
		}
	}
	return out
}

// StatusDescription renders active filters into a short string for the footer.
// Empty when no filters are active.
func (f LogFilter) StatusDescription() string {
	parts := []string{}
	for lv, hidden := range f.HideLevels {
		if hidden {
			parts = append(parts, "¬"+strings.ToUpper(lv))
		}
	}
	if f.Search != "" {
		parts = append(parts, "/"+f.Search)
	}
	if f.Client != "" {
		parts = append(parts, "@"+f.Client)
	}
	return strings.Join(parts, " ")
}
