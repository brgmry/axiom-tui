package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"
)

// View renders the whole screen. Layout is a 3-row vertical stack:
//
//	header (1)            : per-group health bar
//	top    (height - 11)  : main grid [logs | stats/throughput/errors]
//	bottom (8 to 12)      : [top errors | recent | top routes] or [top errors | agents]
//	footer (2)            : status bar
//
// Width is split 5:7 on the top row (logs:right-column). Every panel knows
// its own dimensions, renders independently and is clipped to them, so the
// rows add up to the terminal height.
func (m Model) View() string {
	if !m.ready {
		return "initializing…"
	}
	if m.mode == ModeHelp {
		return m.renderHelp()
	}
	if m.mode == ModeExpand && m.expandedLog != nil {
		return m.renderExpand()
	}
	if m.mode == ModeSourceSwitcher {
		return m.renderSourceSwitcher()
	}

	topH, bottomH := m.rowHeights()

	leftW := m.width * 5 / 12
	rightW := m.width - leftW

	headerBar := m.renderTopBar()

	// Top row: logs on the left, stats/throughput/errors stacked on the right.
	logsPanel := m.renderLogsPanel(leftW, topH)
	rightPanel := m.renderRightStack(rightW, topH)
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, logsPanel, rightPanel)

	// Bottom row: three equal tables, or Top Errors beside the Agents panel.
	colW := m.width / 3
	var bottomRow string
	if m.agents != nil {
		bottomRow = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderTopErrors(colW, bottomH),
			m.renderAgents(m.width-colW, bottomH),
		)
	} else {
		bottomRow = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderTopErrors(colW, bottomH),
			m.renderRecentIssues(colW, bottomH),
			m.renderTopRoutes(m.width-colW*2, bottomH),
		)
	}

	full := lipgloss.JoinVertical(lipgloss.Left, headerBar, topRow, bottomRow, m.renderFooter())
	// Every panel is sized exactly, so this only bites on terminals too
	// small for the layout; a frame taller than the terminal scrolls the
	// header off the top, which is worse than losing the footer.
	full = clipRows(full, m.height)
	// Bubblezone scans the rendered output for marker sequences and resolves
	// them to bounding boxes the mouse handler can hit-test against.
	return zone.Scan(full)
}

// rowHeights splits the terminal height between the top row (stream and
// right stack) and the bottom row (tables or the agents panel), after the
// one-row header and the two-row footer.
func (m Model) rowHeights() (topH, bottomH int) {
	headerH := 1 // top-bar per-group health
	footerH := 2
	bottomH = 8
	if m.agents != nil {
		// Two borders, the title and the column header, then one row per
		// agent.
		bottomH = clampInt(len(m.agentStats)+agentsPanelOverhead, 8, 12)
	}
	// The top row needs at least minTopH rows; the bottom row gives way
	// first, down to its minimum. Below 21 rows the frame is clipped.
	const minTopH = 10
	if spare := m.height - headerH - footerH - minTopH; bottomH > spare {
		bottomH = clampInt(spare, 8, 12)
	}
	topH = m.height - headerH - footerH - bottomH
	if topH < minTopH {
		topH = minTopH
	}
	return topH, bottomH
}

// logBodyRows is the number of event rows the logs panel shows at the
// given panel height: inside the border, below the title, above any open
// prompt.
func (m Model) logBodyRows(height int) int {
	innerH := height - 2
	if innerH < 3 {
		innerH = 3
	}
	bodyH := innerH - 1
	switch m.mode {
	case ModeSearch, ModeClient, ModeProvider, ModeOperation:
		bodyH--
	}
	if bodyH < 1 {
		bodyH = 1
	}
	return bodyH
}

// maxScroll is the largest scroll offset that still fills the logs panel
// with rows from a list of n events: the oldest page. Past it the panel
// would go blank.
func (m Model) maxScroll(n int) int {
	topH, _ := m.rowHeights()
	if max := n - m.logBodyRows(topH); max > 0 {
		return max
	}
	return 0
}

// scrollPos is scrollOff clamped to the filtered list. The stored offset
// can outrun the list when a filter shrinks it or new events arrive, so
// every reader goes through here and keys re-clamp the stored value.
func (m Model) scrollPos(filtered []LogEvent) int {
	return clampInt(m.scrollOff, 0, m.maxScroll(len(filtered)))
}

// clipRows keeps the first h rows of s.
func clipRows(s string, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// placeBox sizes a panel body to exactly innerW x innerH: rows beyond
// innerH are dropped, wider rows are cut (ANSI aware), shorter content is
// padded. lipgloss.Place only pads, so every panel goes through here to
// keep the frame within the terminal.
func placeBox(innerW, innerH int, body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for i, l := range lines {
		if displayWidth(l) > innerW {
			lines[i] = ansi.Truncate(l, innerW, "")
		}
	}
	return lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, strings.Join(lines, "\n"))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ─── Logs panel ──────────────────────────────────────────────────────────────

func (m Model) renderLogsPanel(width, height int) string {
	// innerW/innerH are the dimensions INSIDE the border (border uses 1px each side).
	innerW := width - 2
	innerH := height - 2
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 3 {
		innerH = 3
	}

	statusHint := ""
	if m.paused {
		statusHint = m.theme.StatusFilter.Render(" ⏸ PAUSED")
	} else {
		statusHint = m.theme.TimeDim.Render(" ● tailing")
	}
	titleLine := fmt.Sprintf(" %s Live [%s]",
		m.theme.PanelTitle.Render(strings.ToUpper(m.info.Kind)),
		m.name,
	) + statusHint + "  " + m.filterSummary()

	// Reserve rows: 1 for title, 0-1 for prompt input.
	bodyH := m.logBodyRows(height)
	var promptLine string
	switch m.mode {
	case ModeSearch:
		promptLine = m.searchInput.View()
	case ModeClient:
		promptLine = m.clientInput.View()
	case ModeProvider:
		promptLine = m.providerInput.View()
	case ModeOperation:
		promptLine = m.operationInput.View()
	}

	filtered := m.logs.Filtered(m.filter)
	off := m.scrollPos(filtered)
	end := len(filtered) - off
	start := end - bodyH
	if start < 0 {
		start = 0
	}
	slice := filtered[start:end]

	bodyLines := make([]string, 0, bodyH)
	selectedIdx := len(slice) - 1
	// Absolute index into the filtered slice (start..end) for click-to-event mapping.
	for i, ev := range slice {
		line := m.formatLogLine(ev, innerW)
		if m.focus == FocusLogs && i == selectedIdx && off > 0 {
			line = m.theme.Selected.Render(padRight(line, innerW))
		}
		// Mark each row with a unique zone so right-click copy can resolve it.
		bodyLines = append(bodyLines, zone.Mark(fmt.Sprintf("logrow-%d", start+i), line))
	}
	// Empty-state hint when nothing's showing.
	if len(slice) == 0 && bodyH > 2 {
		var hint string
		if m.filter.Active() {
			hint = m.theme.TimeDim.Render("  no logs match the active filter, esc to reset")
		} else if m.logs.Len() == 0 {
			hint = m.theme.TimeDim.Render("  waiting for events…")
		} else {
			hint = m.theme.TimeDim.Render("  no logs in view")
		}
		bodyLines = append(bodyLines, "", hint)
	}

	// Compose full inner block. Each line will be padded to innerW by Place below.
	parts := []string{titleLine}
	parts = append(parts, bodyLines...)
	if promptLine != "" {
		parts = append(parts, promptLine)
	}
	inner := strings.Join(parts, "\n")

	// Force exact innerW x innerH dimensions, lipgloss pads with spaces. This
	// is what makes the border render reliably regardless of content length.
	innerSized := placeBox(innerW, innerH, inner)
	rendered := m.borderFor(FocusLogs).Render(innerSized)
	return zone.Mark(focusZone(FocusLogs), rendered)
}

func (m Model) filterSummary() string {
	desc := m.filter.StatusDescription(m.info)
	if desc == "" {
		return ""
	}
	return m.theme.StatusFilter.Render("[" + desc + "]")
}

// formatLogLine renders one log event to a single line, sized to width.
// Order: time, level, client (colored), message, field=value pairs.
// Every segment is truncated/elided so one event never exceeds `width`.
//
// When an active search filter is set, the matching needle in the message
// is bolded + cyan-highlighted so your eye snaps to the hits.
func (m Model) formatLogLine(ev LogEvent, width int) string {
	t := ev.Time.Local().Format("15:04:05")
	level := ev.Level
	if level == "" {
		level = "info"
	}
	tPart := m.theme.TimeDim.Render(t)
	lvPart := m.theme.LevelStyle(level).Render(strings.ToUpper(padTo(level, 5)))

	clientPart := ""
	if ev.Client != "" {
		clientPart = " " + ColorForClient(ev.Client).Render(trunc(ev.Client, 16))
	}

	msg := ev.Message
	fieldsStr := m.formatFields(ev.Fields)

	totalUsed := 8 + 1 + 5 + 1 + displayWidth(clientPart) + 1
	remaining := width - totalUsed
	if remaining < 20 {
		remaining = 20
	}
	msgW := remaining
	if fieldsStr != "" {
		msgW = remaining * 2 / 3
		if msgW < 20 {
			msgW = 20
		}
	}
	msg = trunc(msg, msgW)
	fieldsStr = trunc(fieldsStr, remaining-displayWidth(msg)-1)

	// Apply search highlight LAST so it wraps already-truncated text.
	msgRendered := highlightNeedle(msg, m.filter.Search)

	line := tPart + " " + lvPart + clientPart + " " + msgRendered
	if fieldsStr != "" {
		line += " " + m.theme.Fields.Render(highlightNeedle(fieldsStr, m.filter.Search))
	}
	return line
}

// needleStyle marks search hits in the stream.
var needleStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#00e5ff")).
	Bold(true).
	Underline(true)

// highlightNeedle wraps every case-insensitive occurrence of needle in s
// with a bold/cyan style. No-op when needle is empty.
//
// Matching walks s rune by rune and folds each rune pair on its own, so a
// match always covers exactly as many runes of s as the needle has. Lowering
// the whole string first can change its byte length (U+023A lowers to a
// 3-byte rune), and offsets taken from that copy would slice s wrongly.
func highlightNeedle(s, needle string) string {
	if needle == "" {
		return s
	}
	src := []rune(s)
	pat := []rune(needle)
	var out strings.Builder
	last := 0
	for i := 0; i+len(pat) <= len(src); {
		if !runesFoldEqual(src[i:i+len(pat)], pat) {
			i++
			continue
		}
		out.WriteString(string(src[last:i]))
		out.WriteString(needleStyle.Render(string(src[i : i+len(pat)])))
		i += len(pat)
		last = i
	}
	out.WriteString(string(src[last:]))
	return out.String()
}

// runesFoldEqual reports whether a and b are equal under simple per-rune
// case folding. Both slices must have the same length.
func runesFoldEqual(a, b []rune) bool {
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if unicode.ToLower(a[i]) != unicode.ToLower(b[i]) &&
			unicode.ToUpper(a[i]) != unicode.ToUpper(b[i]) {
			return false
		}
	}
	return true
}

// formatFields renders the interesting fields in config order. When the
// source lists none, every field is shown alphabetically so ad hoc sources
// still get something inline.
func (m Model) formatFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	parts := []string{}
	if len(m.info.InterestingFields) > 0 {
		for _, key := range m.info.InterestingFields {
			if v, ok := fields[key]; ok {
				parts = append(parts, fmt.Sprintf("%s=%v", m.info.Label(key), v))
			}
		}
		return strings.Join(parts, " ")
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", m.info.Label(k), fields[k]))
	}
	return strings.Join(parts, " ")
}

// ─── Right stack (stats, cost, throughput, error-rate) ───────────────────────

// summaryPanelRows is the height of a chart panel drawn as its one-line
// text summary: the border and one row.
const summaryPanelRows = 3

// chartPanelRows is the smallest chart panel that still draws the chart:
// border, title and the rows the line chart needs.
func chartPanelRows(series []Series) int {
	return 2 + 1 + chartMinRows(series)
}

// rightStackRows splits the right stack's height. The stats row gets the
// rows it needs first. The charts share the rest; a chart without room to
// draw becomes a one-line summary, and when even that does not fit the
// error trend goes before throughput. Rows nobody can use go back to the
// stats row. A zero height means the panel is not drawn.
func (m Model) rightStackRows(height int) (statsH, throughH, errH int) {
	statsH = max(min(m.statsRows(false), height-summaryPanelRows), 0)
	rem := height - statsH
	tMin, eMin := chartPanelRows(m.throughput), chartPanelRows(m.errorRate)
	switch {
	case rem >= tMin+eMin:
		// Both charts draw. Spare rows let the cost panel list more
		// groups, then the charts split the rest 55/45.
		if extra := min(m.statsRows(true)-statsH, rem-tMin-eMin); extra > 0 {
			statsH += extra
			rem -= extra
		}
		throughH = max(tMin, rem*55/100)
		if rem-throughH < eMin {
			throughH = rem - eMin
		}
		errH = rem - throughH
	case rem >= tMin+summaryPanelRows:
		throughH, errH = rem-summaryPanelRows, summaryPanelRows
	case rem >= tMin:
		throughH = rem
	case rem >= 2*summaryPanelRows:
		throughH, errH = summaryPanelRows, summaryPanelRows
		statsH += rem - 2*summaryPanelRows
	case rem >= summaryPanelRows:
		throughH = summaryPanelRows
		statsH += rem - summaryPanelRows
	default:
		statsH += rem
	}
	return statsH, throughH, errH
}

func (m Model) renderRightStack(width, height int) string {
	statsH, throughH, errH := m.rightStackRows(height)
	// Stats and Cost sit side by side at the top.
	var topRow string
	if m.info.Caps.Has(CapCost) {
		// Equal widths, split the full right-stack width down the middle.
		statsW := width / 2
		costW := width - statsW
		topRow = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderStats(statsW, statsH),
			m.renderCost(costW, statsH),
		)
	} else {
		topRow = m.renderStats(width, statsH)
	}
	rows := []string{topRow}
	if throughH > 0 {
		rows = append(rows, m.renderThroughput(width, throughH))
	}
	if errH > 0 {
		rows = append(rows, m.renderErrors(width, errH))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// eventRate is the average events per minute over the stats window.
func (m Model) eventRate() float64 {
	if m.stats.Total <= 0 || m.timeRange <= 0 {
		return 0
	}
	return float64(m.stats.Total) / m.timeRange.Minutes()
}

// formatRate renders an events-per-minute figure as "~0.5" or "~12":
// one decimal under 10, so a quiet stream does not read as zero. Callers
// add the unit.
func formatRate(r float64) string {
	if r < 10 {
		return fmt.Sprintf("~%.1f", r)
	}
	return fmt.Sprintf("~%d", int(r))
}

// plural is word as it follows the count n: "event" for 1, "events" else.
func plural(n int64, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// borderFor picks the right border style based on focus state. Keeps the
// "is this focused" check out of every panel's body.
func (m Model) borderFor(f Focus) lipgloss.Style {
	if m.focus == f {
		return m.theme.BorderFocused
	}
	return m.theme.Border
}

func (m Model) renderStats(width, height int) string {
	s := m.stats
	innerW := width - 2
	innerH := height - 2

	// Health indicator dot, an at-a-glance signal in the title row.
	dotColor := "#7bed9f" // green
	switch {
	case levelShare(float64(s.Error), float64(s.Total)) > 0.05:
		dotColor = "#ff4757" // red
	case levelShare(float64(s.Warn), float64(s.Total)) > 0.10:
		dotColor = "#ffcc00" // yellow
	}
	healthDot := lipgloss.NewStyle().Foreground(lipgloss.Color(dotColor)).Render("●")

	// The title row carries the total. On a narrow panel the rate goes
	// first, then the window label, so the count always shows.
	title := m.theme.PanelTitle.Render("Last "+formatRange(m.timeRange)) + " " + healthDot
	count := bold(fmt.Sprintf("%d", s.Total)) + " " + plural(s.Total, "event")
	rate := m.theme.TimeDim.Render("(" + formatRate(m.eventRate()) + "/min)")
	header := title + "  " + count + "  " + rate
	if displayWidth(header) > innerW {
		header = title + " " + count
	}
	if displayWidth(header) > innerW {
		header = healthDot + " " + count
	}

	slices := []PieSlice{
		{Value: float64(s.Info), Color: "#7bed9f", Label: "info"},
		{Value: float64(s.Warn), Color: "#ffcc00", Label: "warn"},
		{Value: float64(s.Error), Color: "#ff4757", Label: "error"},
	}
	bars := renderHorizontalBars(slices, float64(s.Total), innerW-2, m.theme)
	if bars == "" {
		bars = m.levelRows(slices, s.Total)
	}

	parts := []string{header, bars}
	// Per-field duration roll-up, one row per configured field. Fall back to
	// the legacy AvgDur/P95Dur/MaxDur scalars when no Durations are populated.
	if len(s.Durations) > 0 {
		// Align the field labels so avg/p95/max columns stack neatly.
		maxLabel := 0
		for _, d := range s.Durations {
			if n := displayWidth(m.info.Label(d.Field)); n > maxLabel {
				maxLabel = n
			}
		}
		for _, d := range s.Durations {
			line := fmt.Sprintf("%s  avg %s   p95 %s   max %s",
				padTo(m.info.Label(d.Field), maxLabel),
				formatDuration(d.Avg),
				formatDuration(d.P95),
				formatDuration(d.Max),
			)
			parts = append(parts, m.theme.TimeDim.Render(line))
		}
	} else if s.AvgDur > 0 || s.P95Dur > 0 || s.MaxDur > 0 {
		durations := fmt.Sprintf("avg %s   p95 %s   max %s",
			formatDuration(s.AvgDur),
			formatDuration(s.P95Dur),
			formatDuration(s.MaxDur),
		)
		parts = append(parts, m.theme.TimeDim.Render(durations))
	}
	body := strings.Join(parts, "\n")

	innerSized := placeBox(innerW, innerH, body)
	rendered := m.borderFor(FocusStats).Render(innerSized)
	return zone.Mark(focusZone(FocusStats), rendered)
}

// levelRows is the narrow-panel form of the level bars: one row per level
// with its count and share, no bar. Twenty cells wide.
func (m Model) levelRows(slices []PieSlice, total int64) string {
	rows := make([]string, 0, len(slices))
	for _, s := range slices {
		pct := levelShare(s.Value, float64(total))
		label := lipgloss.NewStyle().Foreground(s.Color).Bold(true).Render(padTo(strings.ToUpper(s.Label), 5))
		rows = append(rows, label+"  "+padLeft(fmt.Sprintf("%d", int(s.Value)), 5)+"  "+
			m.theme.TimeDim.Render(padLeft(formatShare(pct), 6)))
	}
	return strings.Join(rows, "\n")
}

// statsRows is the height the stats row needs, borders included: the stats
// panel's title, level rows and duration rows, or the cost panel beside it,
// whichever is taller. With full false the cost panel counts one group and
// its total; with full true up to five groups and the spacer line.
func (m Model) statsRows(full bool) int {
	durRows := len(m.stats.Durations)
	if durRows == 0 && (m.stats.AvgDur > 0 || m.stats.P95Dur > 0 || m.stats.MaxDur > 0) {
		durRows = 1
	}
	need := 2 + 1 + 3 + durRows
	if !m.info.Caps.Has(CapCost) {
		return need
	}
	cost := 2 + 1 + 2 // title, then one group and the total (or the empty hint)
	if full && len(m.cost.ByGroup) > 0 {
		cost += min(len(m.cost.ByGroup), 5) // the other groups and the spacer
	}
	if m.cost.HasSummary {
		cost++
	}
	if len(m.cost.ByModel) > 0 {
		cost++
	}
	return max(need, cost)
}

// renderCost shows spend for the current window, grouped. Top 5 rows + a
// bold total, plus the today/hourly summary and model mix when the source
// reports them. Sits beside the stats panel; empty state still draws the
// title so the panel doesn't collapse when there's no spend.
func (m Model) renderCost(width, height int) string {
	innerW := width - 2
	innerH := height - 2
	label := fmt.Sprintf("Cost (%s)", formatRange(m.timeRange))
	header := m.theme.PanelTitle.Render(label)

	// Row budget: the summary, total and model mix lines are fixed, the
	// group rows take what is left (at most 5). The blank line before the
	// total is the first thing to go when the panel is short.
	groups := m.cost.ByGroup
	avail := innerH - 1
	if m.cost.HasSummary {
		avail--
	}
	if len(m.cost.ByModel) > 0 {
		avail--
	}
	spacer := true
	if len(groups) > 0 {
		avail -= 2
		if avail < 1 {
			spacer = false
			avail++
		}
	}
	if len(groups) > 5 {
		groups = groups[:5]
	}
	if len(groups) > avail {
		groups = groups[:max(avail, 0)]
	}

	parts := []string{header}
	if m.cost.HasSummary {
		parts = append(parts, m.theme.TimeDim.Render(
			fmt.Sprintf("today $%.2f · $%.2f/h", m.cost.Today, m.cost.TrailingHour)))
	}
	if len(m.cost.ByGroup) == 0 {
		parts = append(parts, "", m.theme.TimeDim.Render("no spend yet"))
	} else {
		for _, c := range groups {
			// $XX.XX  group, dollar col fixed-width so names align.
			amt := fmt.Sprintf("$%7.2f", c.Dollars)
			name := trunc(c.Client, innerW-displayWidth(amt)-2)
			line := m.theme.TimeDim.Render(amt) + " " +
				ColorForClient(c.Client).Render(name)
			parts = append(parts, line)
		}
		// Grand total across all groups, not just the top rows, so the
		// number matches intuition ("total spend in this window").
		total := fmt.Sprintf("$%7.2f", m.cost.Total)
		if spacer {
			parts = append(parts, "")
		}
		parts = append(parts, bold(total)+" "+m.theme.TimeDim.Render("total"))
	}
	if len(m.cost.ByModel) > 0 {
		models := make([]string, 0, len(m.cost.ByModel))
		for _, c := range m.cost.ByModel {
			models = append(models, fmt.Sprintf("%s $%.2f", shortModel(c.Client), c.Dollars))
		}
		parts = append(parts, m.theme.TimeDim.Render(trunc("by model: "+strings.Join(models, " · "), innerW)))
	}

	body := strings.Join(parts, "\n")
	innerSized := placeBox(innerW, innerH, body)
	// Use the stats border for consistency: the cost panel isn't
	// independently focusable, it's a sidecar to the stats panel.
	return m.borderFor(FocusStats).Render(innerSized)
}

// shortModel strips the vendor prefix from a model id for compact display.
func shortModel(model string) string {
	return strings.TrimPrefix(model, "claude-")
}

func (m Model) renderThroughput(width, height int) string {
	innerW := width - 2
	innerH := height - 2
	title := m.theme.PanelTitle.Render(" Throughput ")
	var body string
	if chartH := innerH - 1; chartDrawable(m.throughput, innerW, chartH) {
		body = title + "\n" + renderLineChartNative(m.throughput, innerW, chartH)
	} else {
		summary := fmt.Sprintf("%d %s  %s ev/min", m.stats.Total, plural(m.stats.Total, "event"), formatRate(m.eventRate()))
		body = title + " " + m.theme.TimeDim.Render(summary)
	}
	innerSized := placeBox(innerW, innerH, body)
	rendered := m.borderFor(FocusThroughput).Render(innerSized)
	return zone.Mark(focusZone(FocusThroughput), rendered)
}

func (m Model) renderErrors(width, height int) string {
	innerW := width - 2
	innerH := height - 2
	title := m.theme.PanelTitle.Render(fmt.Sprintf(" Errors & Warns (%s) ", formatRange(m.trendRange())))
	var body string
	if chartH := innerH - 1; chartDrawable(m.errorRate, innerW, chartH) {
		body = title + "\n" + renderLineChartNative(m.errorRate, innerW, chartH)
	} else {
		body = title + " " + m.theme.TimeDim.Render(seriesTotals(m.errorRate))
	}
	innerSized := placeBox(innerW, innerH, body)
	rendered := m.borderFor(FocusErrorTrend).Render(innerSized)
	return zone.Mark(focusZone(FocusErrorTrend), rendered)
}

// seriesTotals sums each series over its window, "Errors 12  Warns 40",
// for a chart panel too short to draw.
func seriesTotals(series []Series) string {
	parts := make([]string, 0, len(series))
	for _, s := range series {
		sum := 0.0
		for _, p := range s.Points {
			sum += p.Value
		}
		parts = append(parts, fmt.Sprintf("%s %d", s.Name, int64(math.Round(sum))))
	}
	if len(parts) == 0 {
		return "no data"
	}
	return strings.Join(parts, "  ")
}

// ─── Bottom row ──────────────────────────────────────────────────────────────

func (m Model) renderTopErrors(width, height int) string {
	title := m.theme.PanelTitle.Render(fmt.Sprintf(" Top Errors & Warns (%s) ", formatRange(m.timeRange)))
	innerW := width - 4
	innerH := height - 3

	rows := make([]string, 0, innerH)
	for i, r := range m.topErrors {
		if i >= innerH {
			break
		}
		countCol := fmt.Sprintf("%4d", r.Count)
		lvlCol := m.theme.LevelStyle(r.Level).Render(padTo(strings.ToUpper(r.Level), 5))
		msg := trunc(r.Message, innerW-12)
		line := fmt.Sprintf("%s  %s  %s", countCol, lvlCol, msg)
		if m.focus == FocusTopErrors && i == m.errorCursor {
			line = m.theme.Selected.Render(padRight(line, innerW))
		}
		// Each row gets its own zone, click drills into the stream filter.
		rows = append(rows, zone.Mark(fmt.Sprintf("toperror-%d", i), line))
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  all clear"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	innerSized := placeBox(width-2, height-2, body)
	rendered := m.borderFor(FocusTopErrors).Render(innerSized)
	return zone.Mark(focusZone(FocusTopErrors), rendered)
}

func (m Model) renderRecentIssues(width, height int) string {
	title := m.theme.PanelTitle.Render(" Recent Issues ")
	innerW := width - 4
	innerH := height - 3
	events := m.recentIssues
	if len(events) > innerH {
		events = events[:innerH]
	}
	rows := make([]string, 0, innerH)
	for _, ev := range events {
		t := ev.Time.Local().Format("15:04:05")
		lv := m.theme.LevelStyle(ev.Level).Render(padTo(strings.ToUpper(ev.Level), 5))
		msg := trunc(ev.Message, innerW-16)
		rows = append(rows, fmt.Sprintf("%s %s %s", m.theme.TimeDim.Render(t), lv, msg))
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  no recent issues"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	innerSized := placeBox(width-2, height-2, body)
	rendered := m.borderFor(FocusRecent).Render(innerSized)
	return zone.Mark(focusZone(FocusRecent), rendered)
}

func (m Model) renderTopRoutes(width, height int) string {
	title := m.theme.PanelTitle.Render(fmt.Sprintf(" Top Routes (%s) ", formatRange(m.timeRange)))
	innerW := width - 4
	innerH := height - 3
	rows := make([]string, 0, innerH)
	if !m.info.Caps.Has(CapRoutes) {
		rows = append(rows, m.theme.TimeDim.Render("  no route_prefixes configured"))
	}
	for i, r := range m.topRoutes {
		if i >= innerH {
			break
		}
		rows = append(rows, fmt.Sprintf("%5d  %s", r.Count, trunc(r.Message, innerW-8)))
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  no data"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	innerSized := placeBox(width-2, height-2, body)
	rendered := m.borderFor(FocusTopRoutes).Render(innerSized)
	return zone.Mark(focusZone(FocusTopRoutes), rendered)
}

// agentColumn is one column of the Agents panel. Columns are dropped from
// the right of the priority list until the row fits the panel width.
type agentColumn struct {
	name  string
	width int
	right bool // right-align numbers
	cell  func(a AgentStat) string
}

func agentColumns() []agentColumn {
	return []agentColumn{
		{name: "agent", width: 14, cell: func(a AgentStat) string { return trunc(a.Name, 14) }},
		{name: "$today", width: 7, right: true, cell: func(a AgentStat) string { return fmt.Sprintf("%.2f", a.SpendToday) }},
		{name: "$/h", width: 6, right: true, cell: func(a AgentStat) string { return fmt.Sprintf("%.2f", a.SpendHour) }},
		{name: "ctx", width: 5, right: true, cell: func(a AgentStat) string { return formatTokens(a.ContextTokens) }},
		{name: "cmp", width: 3, right: true, cell: func(a AgentStat) string { return fmt.Sprintf("%d", a.Compactions) }},
		{name: "err", width: 3, right: true, cell: func(a AgentStat) string { return fmt.Sprintf("%d", a.ToolErrors) }},
		{name: "orphan", width: 6, right: true, cell: func(a AgentStat) string { return fmt.Sprintf("%d", a.OrphanCalls) }},
		{name: "model", width: 12, cell: func(a AgentStat) string { return trunc(shortModel(a.TopModel), 12) }},
		{name: "fallback", width: 8, cell: func(a AgentStat) string {
			if a.FallbackD == 0 {
				return "-"
			}
			return fmt.Sprintf("%d/%d", a.FallbackN, a.FallbackD)
		}},
		{name: "last", width: 5, right: true, cell: func(a AgentStat) string { return relativeAge(a.LastSeen) }},
	}
}

// formatTokens renders a token count compactly: 68224 becomes "68k".
func formatTokens(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// relativeAge renders how long ago t was, coarsely: "3m", "2h", "5d".
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// agentsPanelOverhead is the number of rows the Agents panel spends on
// borders, title and column header before the first agent row.
const agentsPanelOverhead = 4

// renderAgents draws one row per agent in the order the source returns
// them, which is by today's spend. Rows idle
// for more than 15 minutes render dim. When the panel is too short for
// every agent the last row says how many are missing. The panel is
// read-only.
func (m Model) renderAgents(width, height int) string {
	title := m.theme.PanelTitle.Render(" Agents ")
	innerW := width - 4
	innerH := height - 3

	// Fit columns: drop from the right until the header fits.
	cols := agentColumns()
	fits := func(cs []agentColumn) int {
		w := 0
		for i, c := range cs {
			if i > 0 {
				w += 2
			}
			w += c.width
		}
		return w
	}
	for len(cols) > 1 && fits(cols) > innerW {
		cols = cols[:len(cols)-1]
	}
	cell := func(c agentColumn, s string) string {
		if c.right {
			return padLeft(trunc(s, c.width), c.width)
		}
		return padTo(s, c.width)
	}

	headerCells := make([]string, 0, len(cols))
	for _, c := range cols {
		headerCells = append(headerCells, cell(c, c.name))
	}
	rows := []string{m.theme.TimeDim.Render(strings.Join(headerCells, "  "))}

	agents := m.agentStats // AgentSource.Agents returns them sorted by spend
	capacity := innerH - 1 // rows left after the header
	shown := len(agents)
	if shown > capacity {
		shown = capacity - 1 // leave one row for the overflow line
		if shown < 0 {
			shown = 0
		}
	}
	for _, a := range agents[:shown] {
		cells := make([]string, 0, len(cols))
		for _, c := range cols {
			cells = append(cells, cell(c, c.cell(a)))
		}
		line := strings.Join(cells, "  ")
		if time.Since(a.LastSeen) > 15*time.Minute {
			line = m.theme.TimeDim.Render(line)
		} else {
			line = ColorForClient(a.Name).Render(cells[0]) + line[len(cells[0]):]
		}
		rows = append(rows, line)
	}
	if shown < len(agents) {
		rows = append(rows, m.theme.TimeDim.Render(fmt.Sprintf("  … and %d more", len(agents)-shown)))
	}
	if len(agents) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  no agents in window"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	innerSized := placeBox(width-2, height-2, body)
	rendered := m.borderFor(FocusTopRoutes).Render(innerSized)
	return zone.Mark(focusZone(FocusTopRoutes), rendered)
}

// ─── Top bar: per-group health summary ───────────────────────────────────────

// renderTopBar shows a one-row strip listing the noisiest groups in the last
// 15min by error+warn count, so "client X is on fire" is visible without
// touching any panel. Sources without a group field only get the range and
// preset slots.
func (m Model) renderTopBar() string {
	parts := []string{}
	if m.info.Caps.Has(CapGroup) {
		label := m.info.GroupLabel + " health (15m): "
		if len(m.clientHealth) == 0 {
			parts = append(parts, m.theme.TimeDim.Render(" "+label+"no errors or warns"))
		} else {
			parts = append(parts, m.theme.PanelTitle.Render(" "+label))
			for _, h := range m.clientHealth {
				dot := "●"
				dotStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff4757"))
				if h.Errors == 0 {
					dotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffcc00"))
				}
				clientStyle := ColorForClient(h.Client)
				parts = append(parts, fmt.Sprintf("%s %s %d/%d",
					dotStyle.Render(dot),
					clientStyle.Render(trunc(h.Client, 20)),
					h.Errors, h.Warns,
				))
			}
		}
	}
	parts = append(parts, m.theme.TimeDim.Render(fmt.Sprintf("  range: %s", formatRange(m.timeRange))))

	// Preset slots, a compact list so users remember what's saved.
	if len(m.presets.Items) > 0 {
		slotParts := []string{}
		for i := 1; i <= 9; i++ {
			key := fmt.Sprintf("%d", i)
			if p, ok := m.presets.Items[key]; ok {
				slotParts = append(slotParts,
					m.theme.StatusKey.Render(key)+" "+m.theme.TimeDim.Render(trunc(p.Name, 14)))
			}
		}
		if len(slotParts) > 0 {
			parts = append(parts, m.theme.TimeDim.Render(" presets: ")+strings.Join(slotParts, "  "))
		}
	}
	line := strings.Join(parts, "  ")
	return clipRows(m.theme.StatusBar.Width(m.width).Render(ansi.Truncate(line, m.width, "")), 1)
}

// ─── Footer + Help + Expand ──────────────────────────────────────────────────

// footerSeg is one piece of a footer row. Lower rank is kept longer.
type footerSeg struct {
	text string
	rank int
}

// fitSegments joins segs with two spaces, dropping the highest-ranked
// segment (the rightmost among equals) until the row fits width, and
// returns the dropped segments in their original order. The row is cut as
// a guard, so it never exceeds width.
func fitSegments(segs []footerSeg, width int) (string, []footerSeg) {
	keep := make([]bool, len(segs))
	for i := range keep {
		keep[i] = true
	}
	join := func() string {
		parts := make([]string, 0, len(segs))
		for i, s := range segs {
			if keep[i] {
				parts = append(parts, s.text)
			}
		}
		return strings.Join(parts, "  ")
	}
	line := join()
	for n := len(segs); n > 1 && displayWidth(line) > width; n-- {
		drop := -1
		for i, s := range segs {
			if keep[i] && (drop < 0 || s.rank >= segs[drop].rank) {
				drop = i
			}
		}
		keep[drop] = false
		line = join()
	}
	if width <= 0 {
		line = ""
	} else if displayWidth(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	var dropped []footerSeg
	for i, s := range segs {
		if !keep[i] {
			dropped = append(dropped, s)
		}
	}
	return line, dropped
}

// renderFooter is the two-row status bar: keybind hints on the first row,
// source and counters right-aligned on the second. Every row is fitted to
// the terminal width, so the bar never wraps. Hints are the cheapest to
// lose and drop from the right; status that overflows its row takes their
// place, the error, toast and source last to go.
func (m Model) renderFooter() string {
	keys := []string{
		keyLabel(m.theme, "?", "help"),
		keyLabel(m.theme, "space", "pause"),
		keyLabel(m.theme, "/", "search"),
		keyLabel(m.theme, "T", "range"),
		keyLabel(m.theme, "D", "source"),
		keyLabel(m.theme, "esc", "reset"),
		keyLabel(m.theme, "tab", "focus"),
		keyLabel(m.theme, "enter", "expand"),
		keyLabel(m.theme, "q", "quit"),
	}
	keySegs := make([]footerSeg, len(keys))
	for i, k := range keys {
		keySegs[i] = footerSeg{text: k, rank: i}
	}

	refreshAgo := "-"
	if !m.lastRefresh.IsZero() {
		refreshAgo = fmt.Sprintf("%ds ago", int(time.Since(m.lastRefresh).Seconds()))
	}
	// Inline sparkline of the Errors series, an always-visible signal even
	// when scrolled away.
	errSpark := ""
	if len(m.errorSpark) > 0 {
		errSpark = renderSparkline(m.errorSpark, 14, m.theme.PanelTitle) + " "
	}
	// Ranks: 0 error, 1 toast, 2 source, 3 loading, 4 focus, 5 refresh,
	// 6 untimed, 7 sparkline.
	status := []footerSeg{
		{errSpark + m.theme.TimeDim.Render("errs"), 7},
		{m.theme.TimeDim.Render("focus: ") + m.theme.StatusKey.Render(m.focusName(m.focus)), 4},
		{m.theme.TimeDim.Render("source: ") + m.name, 2},
		{m.theme.TimeDim.Render("refresh: ") + refreshAgo, 5},
	}
	if ps, ok := m.src.(ProgressSource); ok {
		p := ps.Progress()
		if p.Loading {
			status = append(status, footerSeg{m.theme.StatusFilter.Render(
				fmt.Sprintf("loading %d/%d files, %s", p.FilesDone, p.FilesTotal, formatBytes(p.Bytes))), 3})
		}
		if p.Untimed > 0 {
			status = append(status, footerSeg{m.theme.TimeDim.Render(fmt.Sprintf("untimed %d", p.Untimed)), 6})
		}
	}
	if m.queryErr != nil {
		status = append(status, footerSeg{m.theme.StatusError.Render("⚠ " + trunc(m.queryErr.Error(), 40)), 0})
	}
	if m.toast != "" && time.Now().Before(m.toastExpires) {
		toast := m.theme.StatusFilter.Render("✓ " + m.toast)
		if m.toastLevel == toastError {
			toast = m.theme.StatusError.Render("✗ " + m.toast)
		}
		status = append(status, footerSeg{toast, 1})
	}

	// Status that does not fit its own row moves up to the right end of the
	// hint row, and the hints make room for it.
	statusRow, overflow := fitSegments(status, m.width)
	spill, _ := fitSegments(overflow, m.width)
	keyRoom := m.width
	if spill != "" {
		keyRoom -= displayWidth(spill) + 2
	}
	keyRow, _ := fitSegments(keySegs, keyRoom)
	if keyRoom < displayWidth("? help") {
		keyRow = ""
	}
	keyRow = padRight(keyRow, m.width-displayWidth(spill)) + spill

	bar := m.theme.StatusBar.Width(m.width)
	return bar.Render(keyRow) + "\n" + bar.Render(padLeft(statusRow, m.width))
}

// formatBytes renders a byte count as "18.2 MB" style.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func keyLabel(t Theme, k, desc string) string {
	return t.StatusKey.Render(k) + " " + t.TimeDim.Render(desc)
}

// helpEntry is one row of the help overlay: one or more bindings whose
// help keys are joined, and a description. An empty desc takes the first
// binding's own help text.
type helpEntry struct {
	desc  string
	binds []key.Binding
}

// helpSection is a titled block of the help overlay.
type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSections lays out the help overlay from the KeyMap bindings, so the
// overlay lists exactly the keys the handlers match. Filter keys whose
// field the source does not map are omitted.
func (m Model) helpSections() []helpSection {
	k := m.keys
	filters := []helpEntry{
		{"toggle error / warn / info / debug", []key.Binding{k.ToggleError, k.ToggleWarn, k.ToggleInfo, k.ToggleDebug}},
		{"", []key.Binding{k.Search}},
		{"filter by " + m.info.GroupLabel, []key.Binding{k.ClientFilter}},
	}
	if f := m.info.Filters.Provider; f != "" {
		filters = append(filters, helpEntry{"filter by " + m.info.Label(f) + " (substring)", []key.Binding{k.ProviderFilter}})
	}
	if f := m.info.Filters.Operation; f != "" {
		filters = append(filters, helpEntry{"filter by " + m.info.Label(f) + " (substring)", []key.Binding{k.OperationFilter}})
	}
	if f := m.info.Filters.Trace; f != "" {
		filters = append(filters, helpEntry{"trace by " + m.info.Label(f) + " (on focused row)", []key.Binding{k.TraceRequest}})
	}
	filters = append(filters, helpEntry{"", []key.Binding{k.ClearFilter}}, helpEntry{"", []key.Binding{k.Escape}})

	return []helpSection{
		{"navigation", []helpEntry{
			{"scroll log stream", []key.Binding{k.Down, k.Up}},
			{"page down / up", []key.Binding{k.PageDown, k.PageUp}},
			{"", []key.Binding{k.Top}},
			{"", []key.Binding{k.Bottom}},
			{"", []key.Binding{k.Tab}},
			{"focus previous / next panel", []key.Binding{k.FocusLeft, k.FocusRight}},
			{"", []key.Binding{k.Pause}},
		}},
		{"filters", filters},
		{"presets + scope", []helpEntry{
			{"", []key.Binding{k.TimeRange}},
			{"", []key.Binding{k.SourceSwitcher}},
			{"", []key.Binding{k.SavePreset}},
			{"", []key.Binding{k.LoadPreset}},
		}},
		{"actions", []helpEntry{
			{"", []key.Binding{k.Enter}},
			{"", []key.Binding{k.Yank}},
			{"", []key.Binding{k.Refresh}},
			{"", []key.Binding{k.Help}},
			{"", []key.Binding{k.Quit}},
		}},
	}
}

// renderHelp is the ? overlay, a centered modal that lists every keybind.
func (m Model) renderHelp() string {
	type line struct{ keys, desc string }
	sections := m.helpSections()
	lines := make([][]line, len(sections))
	keyW := displayWidth("click")
	for i, sec := range sections {
		for _, e := range sec.entries {
			parts := make([]string, len(e.binds))
			for j, b := range e.binds {
				parts[j] = b.Help().Key
			}
			desc := e.desc
			if desc == "" {
				desc = e.binds[0].Help().Desc
			}
			l := line{strings.Join(parts, " / "), desc}
			keyW = max(keyW, displayWidth(l.keys))
			lines[i] = append(lines[i], l)
		}
	}

	rows := []string{m.theme.PanelTitle.Render("tailboard keybinds")}
	for i, sec := range sections {
		rows = append(rows, "", sec.title)
		for _, l := range lines[i] {
			rows = append(rows, helpRow(m.theme, l.keys, l.desc, keyW+2))
		}
		if i == 0 {
			rows = append(rows, helpRow(m.theme, "click", "focus any panel by clicking", keyW+2))
		}
	}
	body := strings.Join(rows, "\n")
	modal := m.theme.Modal.Render(body)
	return clipRows(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal), m.height)
}

func helpRow(t Theme, k, desc string, w int) string {
	return t.ModalKey.Render(padTo(k, w)) + t.TimeDim.Render(desc)
}

// renderSourceSwitcher is the D-key modal: pick a configured source.
// Active source is checkmarked; cursor row is highlighted.
func (m Model) renderSourceSwitcher() string {
	rows := []string{m.theme.PanelTitle.Render("switch source"), ""}
	if len(m.sources) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("no sources configured, add some to ~/.config/tailboard/config.toml"))
	}
	for i, name := range m.sources {
		marker := "  "
		if name == m.name {
			marker = "✓ "
		}
		line := marker + name
		if i == m.sourceCursor {
			line = m.theme.Selected.Render(padRight(line, 40))
		}
		rows = append(rows, line)
	}
	rows = append(rows, "",
		m.theme.TimeDim.Render("↑/↓ pick   enter switch   esc cancel"))
	body := strings.Join(rows, "\n")
	modal := m.theme.Modal.Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
}

// renderExpand is the Enter overlay: full event JSON, y to copy.
func (m Model) renderExpand() string {
	if m.expandedLog == nil {
		return ""
	}
	header := m.theme.PanelTitle.Render("event ") +
		m.theme.TimeDim.Render(m.expandedLog.Time.Local().Format(time.RFC3339))
	content := prettyJSON(m.expandedLog.Raw)
	if m.expandLoading {
		content = m.theme.TimeDim.Render("loading...")
	}
	// Cap modal height so huge events scroll virtually, a simple top-only view.
	lines := splitLines(content, m.height-6)
	footer := m.theme.TimeDim.Render("y") +
		"  copy JSON    " +
		m.theme.TimeDim.Render("esc") +
		"  close"
	body := strings.Join([]string{
		header,
		"",
		strings.Join(lines, "\n"),
		"",
		footer,
	}, "\n")
	modal := m.theme.Modal.Width(m.width - 4).Render(body)
	return clipRows(lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal), m.height)
}

func bold(s string) string {
	return lipgloss.NewStyle().Bold(true).Render(s)
}
