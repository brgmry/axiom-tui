package main

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/buger/jsonparser"
)

// fieldPath is a pre-split dot path. Lookup tries the literal key first
// (flattened loggers emit "fields.x" as one key), then nested navigation.
type fieldPath struct {
	raw   string
	parts []string
}

func newFieldPath(raw string) fieldPath {
	p := fieldPath{raw: raw}
	if strings.Contains(raw, ".") {
		p.parts = strings.Split(raw, ".")
	}
	return p
}

// get returns the raw value bytes, its JSON type and whether it was found.
// Null counts as not found. The returned slice aliases line.
func (p fieldPath) get(line []byte) ([]byte, jsonparser.ValueType, bool) {
	if p.raw == "" {
		return nil, jsonparser.NotExist, false
	}
	v, t, _, err := jsonparser.Get(line, p.raw)
	if err == nil && t != jsonparser.Null {
		return v, t, true
	}
	if p.parts == nil {
		return nil, jsonparser.NotExist, false
	}
	v, t, _, err = jsonparser.Get(line, p.parts...)
	if err == nil && t != jsonparser.Null {
		return v, t, true
	}
	return nil, jsonparser.NotExist, false
}

// firstOf returns the first path present in line.
func firstOf(line []byte, paths []fieldPath) ([]byte, jsonparser.ValueType, bool) {
	for _, p := range paths {
		if v, t, ok := p.get(line); ok {
			return v, t, true
		}
	}
	return nil, jsonparser.NotExist, false
}

// valueString renders a raw JSON value as display text: strings are
// unescaped, everything else is returned verbatim.
func valueString(v []byte, t jsonparser.ValueType) string {
	if t == jsonparser.String {
		s, err := jsonparser.ParseString(v)
		if err == nil {
			return s
		}
	}
	return string(v)
}

// valueFloat parses a number or a numeric string.
func valueFloat(v []byte, t jsonparser.ValueType) (float64, bool) {
	switch t {
	case jsonparser.Number:
		f, err := jsonparser.ParseFloat(v)
		return f, err == nil
	case jsonparser.String:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	}
	return 0, false
}

// builtinLevels maps every spelling the built-in table knows, lowercased.
var builtinLevels = map[string]string{
	"error": "error", "err": "error", "fatal": "error", "critical": "error", "crit": "error",
	"panic": "error", "emerg": "error", "alert": "error", "50": "error", "60": "error",
	"warn": "warn", "warning": "warn", "40": "warn",
	"info": "info", "notice": "info", "30": "info",
	"debug": "debug", "trace": "debug", "verbose": "debug", "10": "debug", "20": "debug",
}

// normaliseLevel applies the built-in table, then level_map on the raw and
// on the normalised value. Unknown values are kept lowercased.
func normaliseLevel(raw string, levelMap map[string]string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	out, ok := builtinLevels[lower]
	if !ok {
		out = lower
	}
	if len(levelMap) > 0 {
		if m, ok := levelMap[raw]; ok {
			return m
		}
		if m, ok := levelMap[out]; ok {
			return m
		}
	}
	return out
}

// parseTime decodes a timestamp value. Without a layout it accepts
// RFC3339Nano, RFC3339, then a number: >= 1e12 is epoch milliseconds, else
// epoch seconds (fractions allowed).
func parseTime(v []byte, t jsonparser.ValueType, layout string) (time.Time, bool) {
	if t == jsonparser.String {
		s := string(v)
		if layout != "" {
			ts, err := time.Parse(layout, s)
			return ts, err == nil
		}
		if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return ts, true
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return epochTime(f), true
		}
		return time.Time{}, false
	}
	if t == jsonparser.Number {
		f, err := jsonparser.ParseFloat(v)
		if err != nil {
			return time.Time{}, false
		}
		return epochTime(f), true
	}
	return time.Time{}, false
}

func epochTime(f float64) time.Time {
	if f >= 1e12 {
		return time.UnixMilli(int64(f)).UTC()
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

// durationScale returns the multiplier that converts duration_unit to ms,
// and false for a unit it does not know. Empty means ms. Config validation
// rejects an unknown unit at load, so none reaches newFieldMap.
func durationScale(unit string) (float64, bool) {
	switch strings.ToLower(unit) {
	case "", "ms":
		return 1, true
	case "s":
		return 1000, true
	case "us", "µs":
		return 0.001, true
	case "ns":
		return 1e-6, true
	}
	return 0, false
}

// capMessage truncates s to at most n bytes on a rune boundary.
func capMessage(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// FieldMap is the compiled form of FieldsConfig for the generic jsonl
// source. Every lookup is a pre-split path.
type FieldMap struct {
	time        []fieldPath
	timeFormat  string
	level       []fieldPath
	levelMap    map[string]string
	message     []fieldPath
	group       fieldPath
	cost        fieldPath
	costGroup   fieldPath
	duration    []fieldPath
	durScale    float64
	provider    fieldPath
	operation   fieldPath
	trace       fieldPath
	interesting []fieldPath
	// projected is interesting plus the filter fields, deduplicated, in the
	// order they land in LogEvent.Fields.
	projected []fieldPath
}

// newFieldMap compiles a validated FieldsConfig. An unknown duration_unit is
// a caller bug (SourceConfig.validate rejects it) and panics rather than
// scaling durations by a guess.
func newFieldMap(f FieldsConfig) *FieldMap {
	scale, ok := durationScale(f.DurationUnit)
	if !ok {
		panic("newFieldMap: unvalidated duration_unit " + strconv.Quote(f.DurationUnit))
	}
	paths := func(keys []string) []fieldPath {
		out := make([]fieldPath, 0, len(keys))
		for _, k := range keys {
			if k != "" {
				out = append(out, newFieldPath(k))
			}
		}
		return out
	}
	fm := &FieldMap{
		time:        paths(f.Time),
		timeFormat:  f.TimeFormat,
		level:       paths(f.Level),
		levelMap:    f.LevelMap,
		message:     paths(f.Message),
		group:       newFieldPath(f.Group),
		cost:        newFieldPath(f.Cost),
		costGroup:   newFieldPath(f.CostGroup),
		duration:    paths(f.Duration),
		durScale:    scale,
		provider:    newFieldPath(f.Provider),
		operation:   newFieldPath(f.Operation),
		trace:       newFieldPath(f.Trace),
		interesting: paths(f.Interesting),
	}
	if fm.costGroup.raw == "" {
		fm.costGroup = fm.group
	}
	seen := map[string]bool{}
	for _, p := range fm.interesting {
		if !seen[p.raw] {
			seen[p.raw] = true
			fm.projected = append(fm.projected, p)
		}
	}
	for _, p := range []fieldPath{fm.provider, fm.operation, fm.trace} {
		if p.raw != "" && !seen[p.raw] {
			seen[p.raw] = true
			fm.projected = append(fm.projected, p)
		}
	}
	return fm
}

// durationFields returns the configured duration keys in slot order.
func (fm *FieldMap) durationFields() []string {
	out := make([]string, len(fm.duration))
	for i, p := range fm.duration {
		out[i] = p.raw
	}
	return out
}
