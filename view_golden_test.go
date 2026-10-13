package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// goldenNow is the fake source's clock for the golden frame.
var goldenNow = time.Date(2026, 10, 13, 17, 9, 22, 0, time.UTC)

var refreshAgoRE = regexp.MustCompile(`refresh: \d+s ago`)

// goldenFrame renders the fake all-caps source at 120x40 with fixed data,
// ANSI stripped and trailing spaces trimmed. The refresh age is the only
// wall-clock value in the frame and is pinned to 0s.
func goldenFrame(t *testing.T) string {
	t.Helper()
	saved := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = saved })

	m := newTestModel(t, &fakeSource{info: fullInfo(), now: goldenNow})
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	out := refreshAgoRE.ReplaceAllString(stripANSI(m.View()), "refresh: 0s ago")
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestViewGolden compares the 120x40 frame with testdata/view. Run with
// -update to rewrite it after an intended layout change, then copy it into
// the README.
func TestViewGolden(t *testing.T) {
	got := goldenFrame(t)
	path := filepath.Join("testdata", "view", "frame-120x40.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("frame drifted from golden\n--- want\n%s\n--- got\n%s", want, got)
	}
}
