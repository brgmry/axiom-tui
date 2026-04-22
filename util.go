package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lookupAny returns the first non-empty env var from the provided names.
// Makes it easy to accept multiple credential env var spellings.
func lookupAny(names ...string) (string, bool) {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v, true
		}
	}
	return "", false
}

// ─── Loose-typed conversions for Axiom's any-typed column values ─────────────
//
// Axiom returns column values as []any — numbers come through as float64 or
// json.Number depending on the SDK path, timestamps as RFC3339 strings, and
// all strings are strings. These helpers coerce without panicking.

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	}
	return fmt.Sprint(v)
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func asInt(v any) int64 {
	return int64(asFloat(v))
}

func asTime(v any) time.Time {
	switch t := v.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return t
	case string:
		if tt, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return tt
		}
		if tt, err := time.Parse(time.RFC3339, t); err == nil {
			return tt
		}
	}
	return time.Time{}
}

// trunc shortens a string to at most n runes, appending an ellipsis when cut.
// Rune-safe — byte truncation would mangle multibyte input. Tolerates n<=0
// by returning "" (caller may pass a negative width after subtracting other
// columns — we'd rather render empty than panic).
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// prettyJSON returns indented JSON for a flat map, used in the expand modal.
// Keys are stably sorted so eyeballing diffs across events is easier.
func prettyJSON(m map[string]any) string {
	// Sort keys for stability.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]any, len(m))
	for _, k := range keys {
		ordered[k] = m[k]
	}
	b, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unmarshallable event: %v>", err)
	}
	return string(b)
}

// splitLines trims + splits text into lines, bounded by maxLines.
// Appends "…" as a sentinel when truncated.
func splitLines(s string, maxLines int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "…")
	}
	return lines
}
