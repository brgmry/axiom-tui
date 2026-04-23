package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/canvas/runes"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/charmbracelet/lipgloss"
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
	if width < 20 || height < 6 || len(series) == 0 {
		return strings.Repeat(" \n", height)
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
		return strings.Repeat(" \n", height)
	}
	if maxY == minY {
		maxY = minY + 1 // avoid zero range
	}
	// Pad top 10% headroom so the tallest spike doesn't hug the ceiling.
	maxY = maxY + (maxY-minY)*0.1

	ts := timeserieslinechart.New(width, height,
		timeserieslinechart.WithTimeRange(minT, maxT),
		timeserieslinechart.WithYRange(0, maxY),
		timeserieslinechart.WithXLabelFormatter(timeserieslinechart.HourTimeLabelFormatter()),
	)
	// Use arc-style line rendering — smoother curves than the default.
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
			out += "\n" + trunc(legend, width)
		}
	}
	return out
}

// renderLineChart stays as the legacy bar-chart renderer (kept for tiny
// panels where ntcharts' axes don't fit).
func renderLineChart(series []Series, width, height int) string {
	if width < 10 || height < 4 || len(series) == 0 {
		return strings.Repeat(" \n", height)
	}

	// Reserve 1 row for x-axis labels + 1 row for legend.
	chartH := height - 2
	if chartH < 2 {
		chartH = 2
	}

	xs := longestX(series)
	if len(xs) == 0 {
		return strings.Repeat(" \n", height)
	}
	nPoints := len(xs)

	// Downsample / bucket: one column per x. Use max in each column.
	// Each series keeps its own bucket slice so we can render overlays.
	bucketCount := width
	if nPoints < bucketCount {
		bucketCount = nPoints
	}
	if bucketCount < 1 {
		bucketCount = 1
	}

	maxY := 0.0
	buckets := make([][]float64, len(series)) // series → bucketIdx → value
	for si, s := range series {
		buckets[si] = make([]float64, bucketCount)
		for i, p := range s.Points {
			if i >= nPoints {
				break
			}
			idx := i * bucketCount / nPoints
			if idx >= bucketCount {
				idx = bucketCount - 1
			}
			if p.Value > buckets[si][idx] {
				buckets[si][idx] = p.Value
			}
			if p.Value > maxY {
				maxY = p.Value
			}
		}
	}
	if maxY == 0 {
		maxY = 1
	}

	// barChars is the 1/8-step vertical ramp (bottom → full height).
	barChars := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	var out strings.Builder
	// Render top to bottom. For each row r (0 = top), check each column:
	// if any series's fullHeight(col) reaches into this row, draw.
	for r := 0; r < chartH; r++ {
		for c := 0; c < bucketCount; c++ {
			// Find the "tallest" series at this column — it'll own the cell.
			// This renders as overlaid (not stacked) because event streams
			// share x-axis but are independent.
			var bestStyle lipgloss.Style
			var bestChar rune = ' '
			for si, b := range buckets {
				if c >= len(b) {
					continue
				}
				// fullHeight in (0..chartH*8) sub-cells.
				subCells := int((b[c] / maxY) * float64(chartH*8))
				// This row occupies sub-cells [chartH-1-r, chartH-r)*8.
				bottomSub := (chartH - 1 - r) * 8
				topSub := bottomSub + 8
				if subCells <= bottomSub {
					continue // bar doesn't reach this row
				}
				var ch rune
				if subCells >= topSub {
					ch = barChars[8]
				} else {
					ch = barChars[subCells-bottomSub]
				}
				// Prefer the rune with more fill — whichever series spikes
				// hardest in this cell "wins" the visual.
				if ch > bestChar {
					bestChar = ch
					bestStyle = lipgloss.NewStyle().Foreground(chartPalette[si%len(chartPalette)])
				}
			}
			if bestChar == ' ' {
				out.WriteByte(' ')
			} else {
				out.WriteString(bestStyle.Render(string(bestChar)))
			}
		}
		// Pad remainder to full width.
		if bucketCount < width {
			out.WriteString(strings.Repeat(" ", width-bucketCount))
		}
		out.WriteByte('\n')
	}

	// X-axis labels: first, middle, last time.
	if len(xs) >= 2 && width >= 20 {
		labels := make([]byte, width)
		for i := range labels {
			labels[i] = ' '
		}
		place := func(pos int, t time.Time) {
			s := t.Local().Format("15:04")
			if pos < 0 {
				pos = 0
			}
			if pos+len(s) > width {
				pos = width - len(s)
			}
			copy(labels[pos:], s)
		}
		place(0, xs[0])
		place(width/2-2, xs[len(xs)/2])
		place(width-5, xs[len(xs)-1])
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#747d8c"))
		out.WriteString(muted.Render(string(labels)))
		out.WriteByte('\n')
	}

	// Legend on the last row.
	legendParts := []string{}
	for si, s := range series {
		if s.Name == "" {
			continue
		}
		color := chartPalette[si%len(chartPalette)]
		name := trunc(s.Name, 14)
		legendParts = append(legendParts,
			lipgloss.NewStyle().Foreground(color).Render("█ ")+name)
	}
	if len(legendParts) > 0 {
		legend := strings.Join(legendParts, "  ")
		legend = trunc(legend, width)
		out.WriteString(legend)
	}
	return out.String()
}

func longestX(series []Series) []time.Time {
	var best []ChartPoint
	for _, s := range series {
		if len(s.Points) > len(best) {
			best = s.Points
		}
	}
	out := make([]time.Time, len(best))
	for i, p := range best {
		out[i] = p.Time
	}
	return out
}

// ─── Donut (pie chart) ───────────────────────────────────────────────────────
//
// Renders a filled donut using half-block unicode (▀ ▄) so each character
// cell holds two vertically-stacked "pixels". Doubles vertical resolution vs
// full-block rendering — a circle at 8 rows becomes 16 virtual rows of
// fidelity, which reads as actually round instead of stair-stepped.

// PieSlice is one wedge of the donut.
type PieSlice struct {
	Value float64
	Color lipgloss.Color
	Label string
}

// renderDonut draws a donut of the given diameter (in character rows). Each
// row holds two pixel-rows, so effective vertical resolution is 2*diameter.
// Horizontal resolution is 2x diameter to compensate for terminal cells
// being ~2:1 tall:wide — the result is a round-looking donut.
func renderDonut(slices []PieSlice, diameter int) string {
	if diameter < 3 {
		return ""
	}
	total := 0.0
	for _, s := range slices {
		total += s.Value
	}
	if total <= 0 {
		return strings.Repeat("\n", diameter)
	}

	// Cumulative fraction boundaries. Force the last boundary to a touch >1
	// so floating-point near-1.0 values always land in the last slice.
	cum := make([]float64, len(slices))
	running := 0.0
	for i, s := range slices {
		running += s.Value / total
		cum[i] = running
	}
	cum[len(cum)-1] = 1.001

	pxRows := diameter * 2      // pixel rows (half-block doubles vertical)
	pxCols := diameter * 2      // pixel cols (2x horizontal for cell aspect)
	radius := float64(pxRows) / 2
	innerR := radius * 0.55     // hollow centre

	// Sample one logical pixel → slice index (or -1 for empty).
	sample := func(px, py float64) int {
		dx := px - radius + 0.5
		dy := py - radius + 0.5
		d2 := dx*dx + dy*dy
		if d2 > radius*radius || d2 < innerR*innerR {
			return -1
		}
		angle := math.Atan2(dx, -dy)
		if angle < 0 {
			angle += 2 * math.Pi
		}
		pct := angle / (2 * math.Pi)
		for i, b := range cum {
			if pct <= b {
				return i
			}
		}
		return len(slices) - 1
	}

	var out strings.Builder
	for y := 0; y < diameter; y++ {
		for x := 0; x < pxCols; x++ {
			top := sample(float64(x), float64(y*2))
			bot := sample(float64(x), float64(y*2+1))

			switch {
			case top == -1 && bot == -1:
				out.WriteByte(' ')
			case top == -1:
				out.WriteString(lipgloss.NewStyle().Foreground(slices[bot].Color).Render("▄"))
			case bot == -1:
				out.WriteString(lipgloss.NewStyle().Foreground(slices[top].Color).Render("▀"))
			case top == bot:
				out.WriteString(lipgloss.NewStyle().Foreground(slices[top].Color).Render("█"))
			default:
				// Two different colors in one cell — top foreground, bottom background.
				out.WriteString(lipgloss.NewStyle().
					Foreground(slices[top].Color).
					Background(slices[bot].Color).
					Render("▀"))
			}
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// renderDonutWithLegend composes a donut + text legend side-by-side. Returns
// a 2D block ready to drop into a panel.
func renderDonutWithLegend(slices []PieSlice, diameter int, theme Theme) string {
	donut := renderDonut(slices, diameter)
	var legend strings.Builder
	total := 0.0
	for _, s := range slices {
		total += s.Value
	}
	for _, s := range slices {
		pct := 0.0
		if total > 0 {
			pct = s.Value / total * 100
		}
		style := lipgloss.NewStyle().Foreground(s.Color)
		legend.WriteString(fmt.Sprintf("%s %s  %d  %s\n",
			style.Render("●"),
			padTo(s.Label, 6),
			int(s.Value),
			theme.TimeDim.Render(fmt.Sprintf("%.1f%%", pct)),
		))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		donut,
		"  ",
		legend.String(),
	)
}

// (padTo is defined in view.go and shared across files.)

// formatDuration formats a ms value as "12ms", "1.4s", "3m12s" — scale picked
// for readability, not precision.
func formatDuration(ms float64) string {
	if ms <= 0 {
		return "—"
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
