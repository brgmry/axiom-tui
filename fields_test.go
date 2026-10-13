package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buger/jsonparser"
)

func TestFieldPath(t *testing.T) {
	line := []byte(`{"a.b":"literal","a":{"b":"nested","c":{"d":5}},"n":null}`)

	v, _, ok := newFieldPath("a.b").get(line)
	if !ok || string(v) != "literal" {
		t.Errorf("literal key should win: %q %v", v, ok)
	}
	v, _, ok = newFieldPath("a.c.d").get(line)
	if !ok || string(v) != "5" {
		t.Errorf("nested path should resolve: %q %v", v, ok)
	}
	if _, _, ok := newFieldPath("a.x").get(line); ok {
		t.Error("missing path should be not-found")
	}
	if _, _, ok := newFieldPath("n").get(line); ok {
		t.Error("null should be not-found")
	}
	if _, _, ok := newFieldPath("").get(line); ok {
		t.Error("empty path should be not-found")
	}

	paths := []fieldPath{newFieldPath("missing"), newFieldPath("a.c.d"), newFieldPath("a.b")}
	v, _, ok = firstOf(line, paths)
	if !ok || string(v) != "5" {
		t.Errorf("first present should win: %q", v)
	}
}

func TestLevelNormalise(t *testing.T) {
	cases := map[string]string{
		"error": "error", "ERR": "error", "fatal": "error", "Critical": "error", "crit": "error",
		"panic": "error", "emerg": "error", "alert": "error", "50": "error", "60": "error",
		"warn": "warn", "WARNING": "warn", "40": "warn",
		"info": "info", "notice": "info", "30": "info",
		"debug": "debug", "trace": "debug", "verbose": "debug", "10": "debug", "20": "debug",
		"Custom": "custom",
	}
	for raw, want := range cases {
		if got := normaliseLevel(raw, nil); got != want {
			t.Errorf("normaliseLevel(%q) = %q, want %q", raw, got, want)
		}
	}
	lm := map[string]string{"WARNING": "error", "custom": "warn", "debug": "info"}
	if got := normaliseLevel("WARNING", lm); got != "error" {
		t.Errorf("level_map on raw value: got %q", got)
	}
	if got := normaliseLevel("Custom", lm); got != "warn" {
		t.Errorf("level_map on normalised value: got %q", got)
	}
	if got := normaliseLevel("trace", lm); got != "info" {
		t.Errorf("level_map after built-in: got %q", got)
	}
}

func TestTimeParse(t *testing.T) {
	want := time.Date(2026, 10, 11, 15, 5, 27, 0, time.UTC)
	cases := []struct {
		raw    string
		typ    jsonparser.ValueType
		layout string
		want   time.Time
	}{
		{"2026-10-11T15:05:27Z", jsonparser.String, "", want},
		{"2026-10-11T15:05:27.558Z", jsonparser.String, "", want.Add(558 * time.Millisecond)},
		{"2026-10-11T17:05:27+02:00", jsonparser.String, "", want},
		{"1791731127", jsonparser.Number, "", want},
		{"1791731127558", jsonparser.Number, "", want.Add(558 * time.Millisecond)},
		{"1791731127.5", jsonparser.Number, "", want.Add(500 * time.Millisecond)},
		{"1791731127", jsonparser.String, "", want},
		{"11/10/2026 15:05:27", jsonparser.String, "02/01/2006 15:04:05", want},
	}
	for _, c := range cases {
		got, ok := parseTime([]byte(c.raw), c.typ, c.layout)
		if !ok || !got.Equal(c.want) {
			t.Errorf("parseTime(%q) = %v %v, want %v", c.raw, got, ok, c.want)
		}
	}
	if _, ok := parseTime([]byte("yesterday-ish"), jsonparser.String, ""); ok {
		t.Error("garbage should not parse")
	}

	// Garbage through the generic handler falls back to ingestion time and
	// counts as untimed.
	fm, err := newFieldMap(defaultJSONLFields())
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(10)
	c := &counters{}
	h := newGenericHandler(fm, st, c, nil)
	now := time.Now()
	ev, _, ok := h.handle(0, 1, 0, []byte(`{"timestamp":"garbage","message":"x"}`), now)
	if !ok {
		t.Fatal("line should still be ingested")
	}
	if ev.ts != now.UnixNano() {
		t.Error("untimed event should use ingestion time")
	}
	if c.untimed.Load() != 1 {
		t.Errorf("untimed = %d", c.untimed.Load())
	}
}

func TestDurationUnit(t *testing.T) {
	f := defaultJSONLFields()
	f.Duration = StringList{"took"}
	f.DurationUnit = "s"
	fm, err := newFieldMap(f)
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(10)
	h := newGenericHandler(fm, st, &counters{}, nil)
	ev, _, _ := h.handle(0, 1, 0, []byte(`{"message":"x","took":1.5}`), time.Now())
	if len(ev.duration) != 1 || ev.duration[0] != 1500 {
		t.Errorf("seconds should convert to ms: %v", ev.duration)
	}

	// An unknown unit is an error, not a silent 1x scale.
	for unit, want := range map[string]float64{"": 1, "ms": 1, "s": 1000, "us": 0.001, "ns": 1e-6} {
		if got, ok := durationScale(unit); !ok || got != want {
			t.Errorf("durationScale(%q) = %v, %v", unit, got, ok)
		}
	}
	if _, ok := durationScale("minutes"); ok {
		t.Error("unknown unit should not be accepted")
	}

	// A source built without config validation gets an error, not a panic.
	f.DurationUnit = "minutes"
	if fm, err := newFieldMap(f); err == nil || fm != nil || !strings.Contains(err.Error(), `"minutes"`) {
		t.Errorf("newFieldMap with an unknown unit = %v, %v", fm, err)
	}
	sc := SourceConfig{Type: "jsonl", Paths: StringList{filepath.Join(t.TempDir(), "a.jsonl")}, Fields: f}
	if _, err := newFileSource("m", sc, OpenOptions{}, false); err == nil || !strings.Contains(err.Error(), `source "m"`) {
		t.Errorf("newFileSource with an unknown unit: %v", err)
	}
}

// defaultJSONLFields returns the field defaults a jsonl source gets.
func defaultJSONLFields() FieldsConfig {
	sc := SourceConfig{Type: "jsonl", Paths: StringList{"x"}}
	sc.applyTypeDefaults("x")
	return sc.Fields
}

// TestEpochZeroIsTimed checks that a timestamp of exactly the Unix epoch is
// kept as is and not mistaken for a missing one.
func TestEpochZeroIsTimed(t *testing.T) {
	fm, err := newFieldMap(defaultJSONLFields())
	if err != nil {
		t.Fatal(err)
	}
	c := &counters{}
	h := newGenericHandler(fm, newStore(10), c, nil)
	for _, line := range []string{`{"ts":0,"message":"x"}`, `{"timestamp":"1970-01-01T00:00:00Z","message":"x"}`} {
		ev, _, ok := h.handle(0, 1, 0, []byte(line), time.Now())
		if !ok || ev.ts != 0 {
			t.Errorf("%s: ts = %d, want 0", line, ev.ts)
		}
	}
	if n := c.untimed.Load(); n != 0 {
		t.Errorf("untimed = %d, want 0", n)
	}
}
