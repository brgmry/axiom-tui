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
	for _, render := range []struct {
		name string
		fn   func([]Series, int, int) string
	}{
		{"native", renderLineChartNative},
		{"legacy", renderLineChart},
	} {
		const width = 24
		out := render.fn(series, width, 8)
		lines := strings.Split(out, "\n")
		legend := lines[len(lines)-1]
		if brokenEsc.MatchString(legend) {
			t.Errorf("%s: legend was cut inside an escape sequence: %q", render.name, legend)
		}
		if w := ansi.StringWidth(legend); w > width {
			t.Errorf("%s: legend is %d columns wide, panel is %d", render.name, w, width)
		}
		plain := ansi.Strip(legend)
		if !strings.Contains(plain, "business") {
			t.Errorf("%s: legend lost its first series: %q", render.name, plain)
		}
	}
}
