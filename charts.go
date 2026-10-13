package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/canvas/runes"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// sparkChars is the 8-step unicode block ramp. Height encodes value; a value
// of 0 still renders as a space so the baseline is readable.
var sparkChars = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// renderSparkline turns a value slice into a 1-row sparkline of the given
// width. Smooths by averaging values that fall into the same column when
// len(values) > width; otherwise left-pads with spaces.
func renderSparkline(values []float64, width int, style lipgloss.Style) string {
	if width <= 0 || len(values) == 0 {
		return ""
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return style.Render(strings.Repeat(" ", width))
	}

	// Downsample to `width` buckets.
	buckets := make([]float64, width)
	for i, v := range values {
		idx := i * width / len(values)
		if idx >= width {
			idx = width - 1
		}
		if v > buckets[idx] {
			buckets[idx] = v
		}
	}
	var b strings.Builder
	for _, v := range buckets {
		step := int(v / max * float64(len(sparkChars)-1))
		if step < 0 {
			step = 0
		}
		if step >= len(sparkChars) {
			step = len(sparkChars) - 1
		}
		b.WriteRune(sparkChars[step])
	}
	return style.Render(b.String())
}

// ─── Line chart (multi-series, braille) ──────────────────────────────────────

var chartPalette = []lipgloss.Color{
	lipgloss.Color("#00e5ff"), // cyan
	lipgloss.Color("#ffcc00"), // yellow
	lipgloss.Color("#7bed9f"), // green
	lipgloss.Color("#a29bfe"), // lavender
	lipgloss.Color("#ff6b9d"), // pink
	lipgloss.Color("#fab1a0"), // peach
}

// renderLineChartNative draws multi-series time-series data as actual
// connected line strokes using ntcharts' braille-based renderer. Each series
// becomes a dataset with its own palette color; lines auto-scale to the
// shared Y range; X axis is time-formatted as HH:MM.
func renderLineChartNative(series []Series, width, height int) string {
	// The legend takes one of the rows, so the chart itself is drawn one
	// shorter when there is more than one series. The result is always
	// exactly height rows.
	chartH := height
	if len(series) > 1 {
		chartH--
	}
	if !chartDrawable(series, width, height) {
		return blankRows(height)
	}

	// Bounds across all series.
	var minT, maxT time.Time
	var minY, maxY float64
	first := true
	for _, s := range series {
		for _, p := range s.Points {
			if first {
				minT = p.Time
				maxT = p.Time
				minY = p.Value
				maxY = p.Value
				first = false
				continue
			}
			if p.Time.Before(minT) {
				minT = p.Time
			}
			if p.Time.After(maxT) {
				maxT = p.Time
			}
			if p.Value < minY {
				minY = p.Value
			}
			if p.Value > maxY {
				maxY = p.Value
			}
		}
	}
	if first || maxT.Equal(minT) {
		return blankRows(height)
	}
	if maxY == minY {
		maxY = minY + 1 // avoid zero range
	}
	// Pad top 10% headroom so the tallest spike doesn't hug the ceiling.
	maxY = maxY + (maxY-minY)*0.1

	ts := timeserieslinechart.New(width, chartH,
		timeserieslinechart.WithTimeRange(minT, maxT),
		timeserieslinechart.WithYRange(0, maxY),
		timeserieslinechart.WithXLabelFormatter(localTimeLabelFormatter),
	)
	// Use arc-style line rendering, smoother curves than the default.
	ts.SetLineStyle(runes.ArcLineStyle)

	// Push each series as its own dataset with a palette color.
	names := make([]string, 0, len(series))
	for si, s := range series {
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("series-%d", si)
		}
		color := chartPalette[si%len(chartPalette)]
		style := lipgloss.NewStyle().Foreground(color)
		ts.SetDataSetStyle(name, style)
		ts.SetDataSetLineStyle(name, runes.ArcLineStyle)
		for _, p := range s.Points {
			ts.PushDataSet(name, timeserieslinechart.TimePoint{Time: p.Time, Value: p.Value})
		}
		names = append(names, name)
	}
	ts.DrawBrailleDataSets(names)

	out := ts.View()

	// Append a compact legend below the chart; ntcharts doesn't render one
	// itself because it assumes you already know which series is which.
	if len(series) > 1 {
		legendParts := []string{}
		for si, s := range series {
			if s.Name == "" {
				continue
			}
			color := chartPalette[si%len(chartPalette)]
			style := lipgloss.NewStyle().Foreground(color)
			legendParts = append(legendParts,
				style.Render("━━ ")+trunc(s.Name, 14))
		}
		if len(legendParts) > 0 {
			legend := strings.Join(legendParts, "  ")
			// The segments are styled: cut by display width, never by rune.
			out += "\n" + ansi.Truncate(legend, width, "…")
		}
	}
	return out
}

// Below these sizes renderLineChartNative has no room for its axes and
// returns blank rows.
const (
	chartMinWidth = 20
	chartMinPlot  = 5
)

// chartMinRows is the fewest rows renderLineChartNative needs to draw
// series: the plot, plus the legend row when there is more than one.
func chartMinRows(series []Series) int {
	if len(series) > 1 {
		return chartMinPlot + 1
	}
	return chartMinPlot
}

// chartDrawable reports whether renderLineChartNative draws anything for
// series at this size: there is room, and the points span some time.
func chartDrawable(series []Series, width, height int) bool {
	if width < chartMinWidth || height < chartMinRows(series) || len(series) == 0 {
		return false
	}
	var lo, hi time.Time
	for _, s := range series {
		for _, p := range s.Points {
			if lo.IsZero() || p.Time.Before(lo) {
				lo = p.Time
			}
			if hi.IsZero() || p.Time.After(hi) {
				hi = p.Time
			}
		}
	}
	return hi.After(lo)
}

// localTimeLabelFormatter labels the x-axis in local time, like every stream
// row. The library's HourTimeLabelFormatter has the same shape but formats
// in UTC. The value is a Unix timestamp in seconds.
func localTimeLabelFormatter(_ int, v float64) string {
	return time.Unix(int64(v), 0).Local().Format("15:04:05")
}

// blankRows is an empty block of exactly n rows.
func blankRows(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" \n", n-1) + " "
}

// PieSlice is one labelled share of a whole, drawn as a horizontal bar.
type PieSlice struct {
	Value float64
	Color lipgloss.Color
	Label string
}

// levelShare is n as a fraction of total, 0 when total is 0. Every share
// in the stats panel goes through it so the wide bars, the narrow rows and
// the health dot agree. total is Stats.Total, which counts debug events too.
func levelShare(n, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return n / total
}

// formatShare renders a share from levelShare as "12.3%".
func formatShare(share float64) string {
	return fmt.Sprintf("%.1f%%", share*100)
}

// renderHorizontalBars draws each PieSlice as a labeled horizontal bar with
// proportional fill, so "errors are X% of traffic" reads at a glance. Shares
// are taken over total, not the sum of the slices, so levels without a bar
// (debug) still count.
//
// Layout per row:  LABEL  ████████████████░░░░░  COUNT  PERCENT
// Bar width is sized to fill the panel; counts/percents trail the bar.
func renderHorizontalBars(slices []PieSlice, total float64, width int, theme Theme) string {
	if width < 30 || len(slices) == 0 {
		return ""
	}

	// Layout columns: label(6) + space(2) + bar(barW) + space(2) + count(6) + space(1) + pct(6)
	const (
		labelW = 6
		countW = 6
		pctW   = 6
		gap    = 2
	)
	barW := width - labelW - countW - pctW - gap*2 - 1
	if barW < 5 {
		barW = 5
	}

	var out strings.Builder
	for _, s := range slices {
		labelStyle := lipgloss.NewStyle().Foreground(s.Color).Bold(true)
		pct := levelShare(s.Value, total)
		filled := min(int(pct*float64(barW)), barW)
		if pct > 0 && filled == 0 {
			filled = 1 // always show something for non-zero
		}
		empty := barW - filled
		bar := lipgloss.NewStyle().Foreground(s.Color).Render(strings.Repeat("█", filled)) +
			theme.TimeDim.Render(strings.Repeat("░", empty))

		out.WriteString(fmt.Sprintf("%s  %s  %s  %s\n",
			labelStyle.Render(padTo(strings.ToUpper(s.Label), labelW)),
			bar,
			padLeft(fmt.Sprintf("%d", int(s.Value)), countW),
			theme.TimeDim.Render(padLeft(formatShare(pct), pctW)),
		))
	}
	return strings.TrimRight(out.String(), "\n")
}

// formatDuration formats a ms value as "12ms", "1.4s", "3m12s", scale picked
// for readability, not precision.
func formatDuration(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	return fmt.Sprintf("%dm%ds", int(sec/60), int(sec)%60)
}
