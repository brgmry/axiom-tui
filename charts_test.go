package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// brokenEsc matches an SGR escape sequence cut before its final byte: the
// introducer is followed by something other than parameters and "m".
var brokenEsc = regexp.MustCompile("\x1b\\[[0-9;]*(?:[^0-9;m]|$)")

// TestChartLegendTruncation: the legend under a segmented chart is built
// from styled segments, so cutting it by rune count lands inside an escape
// sequence and corrupts the row. The cut must be ANSI aware.
func TestChartLegendTruncation(t *testing.T) {
	series := make([]Series, 0, 4)
	for _, name := range []string{"business", "pi-os", "crm", "admin"} {
		series = append(series, Series{Name: name, Points: fakePoints(time.Hour)})
	}
	const width = 24
	out := renderLineChartNative(series, width, 8)
	lines := strings.Split(out, "\n")
	legend := lines[len(lines)-1]
	if brokenEsc.MatchString(legend) {
		t.Errorf("legend was cut inside an escape sequence: %q", legend)
	}
	if w := ansi.StringWidth(legend); w > width {
		t.Errorf("legend is %d columns wide, panel is %d", w, width)
	}
	plain := ansi.Strip(legend)
	if !strings.Contains(plain, "business") {
		t.Errorf("legend lost its first series: %q", plain)
	}
}

// TestLineChartSize: the live renderer returns exactly the rows it was
// given and no row wider than the panel, with and without room to draw.
func TestLineChartSize(t *testing.T) {
	series := []Series{
		{Name: "Errors", Points: fakePoints(time.Hour)},
		{Name: "Warns", Points: fakePoints(time.Hour)},
	}
	for _, size := range [][2]int{{40, 10}, {60, 6}, {30, 4}, {80, 20}} {
		width, height := size[0], size[1]
		out := renderLineChartNative(series, width, height)
		lines := strings.Split(out, "\n")
		if len(lines) != height {
			t.Errorf("%dx%d: %d rows, want %d", width, height, len(lines), height)
		}
		for i, l := range lines {
			if w := displayWidth(l); w > width {
				t.Errorf("%dx%d: row %d is %d cells wide", width, height, i, w)
			}
		}
	}
}
