package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// fuzzMaxLine keeps the over-limit rule inside reach of the fuzzer.
const fuzzMaxLine = 4096

// FuzzProcessLine feeds one line through the tailer's line rules into the
// generic and the pi handler, then into a store, and checks that nothing
// panics and the event is shaped as the rest of the code expects. Seeds
// are the generic sample and the pi fixture sessions.
func FuzzProcessLine(f *testing.F) {
	sample, err := os.ReadFile("testdata/generic/sample.jsonl")
	if err != nil {
		f.Fatal(err)
	}
	for _, line := range bytes.Split(sample, []byte("\n")) {
		f.Add(line)
	}
	base := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for _, session := range [][]piEntry{normalSession(base), abortedSession(base), headerOnlySession(base)} {
		for _, e := range session {
			f.Add([]byte(piLine(e)))
		}
	}
	for _, s := range []string{
		"", " ", "{}", "[]", "null", `{"ts":0}`, `{"timestamp":-1}`, `{"timestamp":1e300}`,
		`{"message":"` + strings.Repeat("é", 400) + `"}`, `{"type":"message","message":{"role":"assistant","content":"x"}}`,
		`{"type":"message","message":{"role":"assistant","usage":{"totalTokens":1e40}}}`,
		`{"level":{"nested":true},"message":[1,2]}`, "{\"message\":\"x\"}\r",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, line []byte) {
		fuzzGeneric(t, line)
		fuzzPi(t, line)
	})
}

func fuzzGeneric(t *testing.T, line []byte) {
	sc := SourceConfig{Type: "jsonl", Paths: StringList{"x"}, MaxLineBytes: fuzzMaxLine, RoutePrefixes: StringList{"GET ", "POST "}}
	sc.Fields.Group = "fields.clientName"
	sc.Fields.Cost = "fields.costDollars"
	sc.Fields.Duration = StringList{"fields.durationMs"}
	sc.Fields.Interesting = StringList{"fields.requestId"}
	sc.applyTypeDefaults("x")
	fm, err := newFieldMap(sc.Fields)
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(16)
	c := &counters{}
	tl := newTailer(sc, st, newGenericHandler(fm, st, c, sc.RoutePrefixes), c, 0)

	ev, id, ok := fuzzLine(t, tl, st, 7, line)
	trimmed := bytes.TrimSpace(line)
	wellFormed := len(line) <= fuzzMaxLine && len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
	if ok != wellFormed {
		t.Fatalf("generic: ok = %v for a line that is well formed = %v", ok, wellFormed)
	}
	if !ok {
		rejected := c.skippedLines.Load() + c.parseErrors.Load()
		if len(trimmed) > 0 && rejected != 1 {
			t.Fatalf("generic: a rejected line should count once, counted %d", rejected)
		}
		return
	}
	if id != "" {
		t.Fatalf("generic: id = %q, want none", id)
	}
	if len(ev.duration) != 1 {
		t.Fatalf("generic: %d duration slots, want 1", len(ev.duration))
	}
}

func fuzzPi(t *testing.T, line []byte) {
	sc := SourceConfig{Type: "pi", MaxLineBytes: fuzzMaxLine}
	st := newStore(16)
	st.durFields = piDurationFields
	st.costDimRole = costDimModel
	c := &counters{}
	h := newPiHandler(st, c, time.Minute)
	tl := newTailer(sc, st, h, c, 0)
	h.tailer = tl

	// As the first line of a file the input is judged as a header; after
	// a real header it is judged as an entry.
	fuzzLine(t, tl, st, 0, line)
	tl.handler.reset(0)
	header := []byte(piLine(piHeader("s", "/x/agent", time.Now())))
	if _, _, ok := fuzzLine(t, tl, st, 0, header); !ok {
		t.Fatal("pi: the fixture header should be ingested")
	}
	ev, _, ok := fuzzLine(t, tl, st, int64(len(header)+1), line)
	if !ok {
		return
	}
	if ev.client != "agent" {
		t.Fatalf("pi: client = %q, want the header's agent", ev.client)
	}
	if len(ev.duration) != len(piDurationFields) {
		t.Fatalf("pi: %d duration slots, want %d", len(ev.duration), len(piDurationFields))
	}
	_, _ = (&PiSource{FileSource: &FileSource{store: st, tailer: tl, counters: c}, h: h}).Agents(context.Background(), time.Hour)
}

// fuzzLine runs one line through processLine and, when it is accepted,
// checks the common invariants and appends it to the store and reads it
// back.
func fuzzLine(t *testing.T, tl *tailer, st *store, offset int64, line []byte) (event, string, bool) {
	f := &tailFile{path: "fuzz.jsonl", idx: 0, gen: 3}
	ev, id, ok := tl.processLine(f, offset, line)
	if !ok {
		return ev, id, ok
	}
	if len(line) > fuzzMaxLine {
		t.Fatalf("accepted a %d-byte line over the %d limit", len(line), fuzzMaxLine)
	}
	if want := (eventRef{file: 0, generation: 3, offset: offset, length: int32(len(line))}); ev.ref != want {
		t.Fatalf("ref = %+v, want %+v", ev.ref, want)
	}
	if len(ev.message) > maxMessageBytes {
		t.Fatalf("message of %d bytes over the %d cap", len(ev.message), maxMessageBytes)
	}
	before := st.len()
	st.appendBatch([]event{ev}, []string{id})
	if st.len() != before+1 {
		t.Fatalf("store len %d after append, want %d", st.len(), before+1)
	}
	now := time.Now()
	_ = st.stats(now, time.Hour)
	_ = st.cost(now, time.Hour)
	if evs, _ := st.stream(Cursor{Seq: 1}, 10, true); len(evs) != st.len() {
		t.Fatalf("stream returned %d events, store holds %d", len(evs), st.len())
	}
	return ev, id, ok
}
