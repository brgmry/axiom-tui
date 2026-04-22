package main

import (
	"fmt"
	"math"
	"strings"
	"time"

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

// renderLineChart draws multi-series data as block-based lines, sized to
// width x height cells. Good enough to read at a glance; no axis labels yet.
// Empty series degrade to an empty box rather than a crash.
func renderLineChart(series []Series, width, height int) string {
	if width < 10 || height < 3 || len(series) == 0 {
		return strings.Repeat("\n", height)
	}
	// Collect all points for max-y + time bounds.
	var maxY float64
	for _, s := range series {
		for _, p := range s.Points {
			if p.Value > maxY {
				maxY = p.Value
			}
		}
	}
	if maxY == 0 {
		maxY = 1 // avoid div-by-zero
	}

	// Assume all series share the x-axis bin set (they do for our queries).
	xs := longestX(series)
	if len(xs) == 0 {
		return strings.Repeat("\n", height)
	}

	chartH := height - 2 // reserve 1 for legend, 1 for x-axis labels
	if chartH < 1 {
		chartH = height
	}

	// Build a cell grid.
	grid := make([][]lipgloss.Style, chartH)
	chars := make([][]rune, chartH)
	for r := 0; r < chartH; r++ {
		grid[r] = make([]lipgloss.Style, width)
		chars[r] = make([]rune, width)
		for c := 0; c < width; c++ {
			chars[r][c] = ' '
		}
	}

	for si, s := range series {
		color := chartPalette[si%len(chartPalette)]
		lineStyle := lipgloss.NewStyle().Foreground(color)
		for i, p := range s.Points {
			if i >= width {
				break
			}
			col := i * width / len(s.Points)
			if col >= width {
				col = width - 1
			}
			ratio := p.Value / maxY
			if ratio < 0 {
				ratio = 0
			}
			if ratio > 1 {
				ratio = 1
			}
			row := chartH - 1 - int(ratio*float64(chartH-1))
			if row < 0 {
				row = 0
			}
			// Overlap: prefer denser glyph.
			if chars[row][col] == ' ' || chars[row][col] == '·' {
				chars[row][col] = '●'
			} else {
				chars[row][col] = '◆'
			}
			grid[row][col] = lineStyle
		}
	}

	var out strings.Builder
	for r := 0; r < chartH; r++ {
		for c := 0; c < width; c++ {
			if chars[r][c] == ' ' {
				out.WriteByte(' ')
				continue
			}
			out.WriteString(grid[r][c].Render(string(chars[r][c])))
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
			lipgloss.NewStyle().Foreground(color).Render("● ")+name)
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
// Renders a filled donut by ray-marching each character cell. Angle determines
// the slice; radius determines fill (inner hollow + outer rim). Terminal cells
// are ~2x as tall as wide, so we double the horizontal step to keep the shape
// circular rather than stretched.

// PieSlice is one wedge of the donut.
type PieSlice struct {
	Value float64
	Color lipgloss.Color
	Label string
}

// renderDonut draws a donut chart of the given diameter (in rows). The actual
// cell width is 2*diameter because terminal cells are not square. Total of
// slice values drives percentages — caller doesn't need to normalize.
func renderDonut(slices []PieSlice, diameter int) string {
	if diameter < 3 {
		return ""
	}
	total := 0.0
	for _, s := range slices {
		total += s.Value
	}
	if total <= 0 {
		return strings.Repeat(" ", diameter*2) + "\n" + strings.Repeat("\n", diameter-1)
	}
	// Precompute cumulative fraction boundaries per slice.
	cum := make([]float64, len(slices))
	running := 0.0
	for i, s := range slices {
		running += s.Value / total
		cum[i] = running
	}

	radius := float64(diameter) / 2
	innerR := radius * 0.55 // hollow centre for the "donut" look

	var out strings.Builder
	for y := 0; y < diameter; y++ {
		for x := 0; x < diameter*2; x++ {
			// Half horizontal step to compensate for 2:1 cell aspect ratio.
			dx := float64(x)/2.0 - radius + 0.5
			dy := float64(y) - radius + 0.5
			dist := dx*dx + dy*dy
			if dist > radius*radius || dist < innerR*innerR {
				out.WriteByte(' ')
				continue
			}
			// Angle: 0 at 12 o'clock, increasing clockwise.
			// atan2(dx, -dy) gives -π..π with 0 at 12 o'clock clockwise.
			angle := math.Atan2(dx, -dy)
			if angle < 0 {
				angle += 2 * math.Pi
			}
			pct := angle / (2 * math.Pi)
			for i, boundary := range cum {
				if pct <= boundary {
					out.WriteString(lipgloss.NewStyle().Foreground(slices[i].Color).Render("█"))
					break
				}
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
