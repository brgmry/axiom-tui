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

// TestChartAxisLocalTime: stream rows are local, so the chart axis must be
// too. The library formatter printed UTC.
func TestChartAxisLocalTime(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("test", 5*3600+30*60)
	t.Cleanup(func() { time.Local = saved })

	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	want := start.In(time.Local).Format("15:04:05")
	if got := localTimeLabelFormatter(0, float64(start.Unix())); got != want {
		t.Errorf("label = %q, want %q", got, want)
	}

	pts := make([]ChartPoint, 0, 10)
	for i := 0; i < 10; i++ {
		pts = append(pts, ChartPoint{Time: start.Add(time.Duration(i) * 6 * time.Minute), Value: float64(i % 4)})
	}
	out := ansi.Strip(renderLineChartNative([]Series{{Name: "ev/min", Points: pts}}, 60, 10))
	if !strings.Contains(out, want) {
		t.Errorf("axis should start at %s local:\n%s", want, out)
	}
	if utc := start.Format("15:04:05"); strings.Contains(out, utc) {
		t.Errorf("axis shows %s, the UTC time:\n%s", utc, out)
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
