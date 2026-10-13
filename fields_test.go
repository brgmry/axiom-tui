package main

import (
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
	fm := newFieldMap(defaultJSONLFields())
	st := newStore(10)
	c := &counters{}
	h := newGenericHandler(fm, st, c, nil)
	now := time.Now()
	ev, ok := h.handle(0, 1, 0, []byte(`{"timestamp":"garbage","message":"x"}`), now)
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
	fm := newFieldMap(f)
	st := newStore(10)
	h := newGenericHandler(fm, st, &counters{}, nil)
	ev, _ := h.handle(0, 1, 0, []byte(`{"message":"x","took":1.5}`), time.Now())
	if len(ev.duration) != 1 || ev.duration[0] != 1500 {
		t.Errorf("seconds should convert to ms: %v", ev.duration)
	}
}

// defaultJSONLFields returns the field defaults a jsonl source gets.
func defaultJSONLFields() FieldsConfig {
	sc := SourceConfig{Type: "jsonl", Paths: StringList{"x"}}
	sc.applyTypeDefaults("x")
	return sc.Fields
}
