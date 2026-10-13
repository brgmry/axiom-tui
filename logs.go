package main

import (
	"fmt"
	"sort"
	"strings"
)

// LogBuffer is a bounded ring of LogEvents. New events push onto the tail;
// once the buffer is full, the oldest events drop silently. Size is set
// from config (default 2000), big enough to scroll through a few minutes
// of high-volume streams, small enough to keep the TUI responsive.
type LogBuffer struct {
	items []LogEvent
	max   int
}

func NewLogBuffer(max int) *LogBuffer {
	return &LogBuffer{items: make([]LogEvent, 0, max), max: max}
}

// Append adds new events, skipping duplicates of recently buffered ones.
// Polling can return the same event twice at a poll boundary.
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

// isDup checks the last 8 entries. Events with an ID compare by ID; events
// without one compare by (time, message), which is good enough to suppress
// poll-boundary duplicates without scanning the whole buffer.
func (b *LogBuffer) isDup(ev LogEvent) bool {
	start := len(b.items) - 8
	if start < 0 {
		start = 0
	}
	for i := start; i < len(b.items); i++ {
		if ev.ID != "" {
			if b.items[i].ID == ev.ID {
				return true
			}
			continue
		}
		if b.items[i].ID == "" && b.items[i].Time.Equal(ev.Time) && b.items[i].Message == ev.Message {
			return true
		}
	}
	return false
}

// Len returns the number of buffered events.
func (b *LogBuffer) Len() int { return len(b.items) }

// All returns the internal slice, callers must treat it as read-only.
func (b *LogBuffer) All() []LogEvent { return b.items }

// ─── Filtering ───────────────────────────────────────────────────────────────

// LogFilter captures the active filter state for the log stream. A filter
// built by NewLogFilter hides the source's default levels and knows which
// event fields the provider, operation and trace keys read.
//
// HideLevels is a map not a slice so toggling is O(1). Search and Client are
// plain substring matches (case-insensitive); regex is a pit we don't need
// to fall into yet.
type LogFilter struct {
	HideLevels map[string]bool
	Search     string
	Client     string
	Provider   string
	Operation  string
	// Trace pins the stream to a single trace. Exact match on the trace
	// field, set via the `t` key on a focused row.
	Trace string

	fields FilterFields
	seed   []string // levels hidden at startup; Reset restores them
}

// NewLogFilter builds the startup filter for a source: its hidden levels and
// filter field keys.
func NewLogFilter(info SourceInfo) LogFilter {
	f := LogFilter{fields: info.Filters, seed: info.HideLevels}
	f.HideLevels = seedLevels(info.HideLevels)
	return f
}

func seedLevels(levels []string) map[string]bool {
	m := map[string]bool{}
	for _, lv := range levels {
		m[lv] = true
	}
	return m
}

// Reset clears every filter back to the source's startup state.
func (f LogFilter) Reset() LogFilter {
	return LogFilter{HideLevels: seedLevels(f.seed), fields: f.fields, seed: f.seed}
}

// Fields returns the keys the provider, operation and trace filters read.
func (f LogFilter) Fields() FilterFields { return f.fields }

// Active reports whether any filter is in effect beyond the startup seed.
func (f LogFilter) Active() bool {
	seed := seedLevels(f.seed)
	for lv, v := range f.HideLevels {
		if v != seed[lv] {
			return true
		}
	}
	return f.Search != "" || f.Client != "" || f.Provider != "" || f.Operation != "" || f.Trace != ""
}

// Match returns true when ev passes every active filter. Provider, operation
// and trace are no-ops when their field key is empty.
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
	if f.Provider != "" && f.fields.Provider != "" {
		if !strings.EqualFold(asFieldString(ev.Fields[f.fields.Provider]), f.Provider) {
			return false
		}
	}
	if f.Operation != "" && f.fields.Operation != "" {
		if !strings.EqualFold(asFieldString(ev.Fields[f.fields.Operation]), f.Operation) {
			return false
		}
	}
	if f.Trace != "" && f.fields.Trace != "" {
		// Exact match, trace IDs are identity, not fuzzy.
		if asFieldString(ev.Fields[f.fields.Trace]) != f.Trace {
			return false
		}
	}
	return true
}

// asFieldString coerces an any-typed field value to string without pulling in
// util.go's asString (same shape, local copy keeps the hot Match path free of
// the fmt.Stringer check).
func asFieldString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
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
// Never returns nil, callers can safely iterate over the result.
func (b *LogBuffer) Filtered(f LogFilter) []LogEvent {
	if !f.Active() && len(f.HideLevels) == 0 {
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

// StatusDescription renders active filters into a short string for the
// footer. Provider, operation and trace are labelled with their field name
// (display prefix stripped). Empty when no filters are active.
func (f LogFilter) StatusDescription(info SourceInfo) string {
	parts := []string{}
	hidden := []string{}
	for lv, h := range f.HideLevels {
		if h {
			hidden = append(hidden, lv)
		}
	}
	sort.Strings(hidden)
	for _, lv := range hidden {
		parts = append(parts, "¬"+strings.ToUpper(lv))
	}
	if f.Search != "" {
		parts = append(parts, "/"+f.Search)
	}
	if f.Client != "" {
		parts = append(parts, "@"+f.Client)
	}
	if f.Provider != "" && f.fields.Provider != "" {
		parts = append(parts, info.Label(f.fields.Provider)+":"+f.Provider)
	}
	if f.Operation != "" && f.fields.Operation != "" {
		parts = append(parts, info.Label(f.fields.Operation)+":"+f.Operation)
	}
	if f.Trace != "" && f.fields.Trace != "" {
		// Render only the first 8 chars, full UUIDs blow out the footer.
		short := f.Trace
		if len(short) > 8 {
			short = short[:8]
		}
		parts = append(parts, info.Label(f.fields.Trace)+":"+short)
	}
	return strings.Join(parts, " ")
}
