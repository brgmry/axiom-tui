package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

// View renders the whole screen. Layout is a 3-row vertical stack:
//
//   top    (height - 10)  : main grid [logs | stats/throughput/errors]
//   bottom (8)            : three-panel row [top errors | recent | top routes]
//   footer (2)            : status bar
//
// Width is split 5:7 on the top row (logs:right-column), and 4:4:4 on the bottom.
// Every panel knows its own dimensions and renders independently.
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

	footerH := 2
	bottomH := 8
	topH := m.height - footerH - bottomH
	if topH < 10 {
		topH = 10
	}

	leftW := m.width * 5 / 12
	rightW := m.width - leftW

	// Top row — logs on the left, stats/donut/throughput/errors stacked on the right.
	logsPanel := m.renderLogsPanel(leftW, topH)
	rightPanel := m.renderRightStack(rightW, topH)
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, logsPanel, rightPanel)

	// Bottom row — three equal tables.
	colW := m.width / 3
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderTopErrors(colW, bottomH),
		m.renderRecentIssues(colW, bottomH),
		m.renderTopRoutes(m.width-colW*2, bottomH),
	)

	full := lipgloss.JoinVertical(lipgloss.Left, topRow, bottomRow, m.renderFooter())
	// Bubblezone scans the rendered output for marker sequences and resolves
	// them to bounding boxes the mouse handler can hit-test against.
	return zone.Scan(full)
}

// ─── Logs panel ──────────────────────────────────────────────────────────────

func (m Model) renderLogsPanel(width, height int) string {
	title := fmt.Sprintf(" %s Live [%s] ",
		m.theme.PanelTitle.Render("AXIOM"),
		m.dataset,
	)
	// Stream-status hint — easy to miss if it lives only in the footer.
	statusHint := ""
	if m.paused {
		statusHint = m.theme.StatusFilter.Render(" ⏸ PAUSED")
	} else {
		statusHint = m.theme.TimeDim.Render(" ● tailing")
	}

	filtered := m.logs.Filtered(m.filter)

	// Inner area: subtract 2 for border, 1 for the header hint row.
	innerW := width - 2
	innerH := height - 3
	if innerH < 1 {
		innerH = 1
	}

	// Reserve one extra row for the input prompt when in search/client mode.
	var promptLine string
	switch m.mode {
	case ModeSearch:
		promptLine = m.searchInput.View()
		innerH--
	case ModeClient:
		promptLine = m.clientInput.View()
		innerH--
	}

	// Determine the slice to render: the last `innerH` visible lines ending
	// at (len - scrollOff - 1).
	end := len(filtered) - m.scrollOff
	if end > len(filtered) {
		end = len(filtered)
	}
	if end < 0 {
		end = 0
	}
	start := end - innerH
	if start < 0 {
		start = 0
	}
	slice := filtered[start:end]

	lines := make([]string, 0, innerH)
	selectedIdx := len(slice) - 1 // cursor lands on last visible line
	for i, ev := range slice {
		line := m.formatLogLine(ev, innerW)
		if m.focus == FocusLogs && i == selectedIdx && m.scrollOff > 0 {
			line = m.theme.Selected.Render(padRight(line, innerW))
		}
		lines = append(lines, line)
	}
	// Empty-state hint — covers both "no logs streamed yet" and
	// "filter eliminated everything" so the user knows the panel isn't broken.
	if len(slice) == 0 && innerH > 2 {
		var hint string
		if m.filter.Active() {
			hint = m.theme.TimeDim.Render("  no logs match the active filter — esc to reset")
		} else if m.logs.Len() == 0 {
			hint = m.theme.TimeDim.Render("  waiting for events…")
		} else {
			hint = m.theme.TimeDim.Render("  no logs in view")
		}
		lines = append(lines, "", hint)
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	body := strings.Join(lines, "\n")

	inner := lipgloss.JoinVertical(lipgloss.Left,
		title+statusHint+"  "+m.filterSummary(),
		body,
	)
	if promptLine != "" {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, promptLine)
	}

	rendered := m.borderFor(FocusLogs).Width(width - 2).Height(height - 2).Render(inner)
	return zone.Mark(focusZone(FocusLogs), rendered)
}

func (m Model) filterSummary() string {
	desc := m.filter.StatusDescription()
	if desc == "" {
		return ""
	}
	return m.theme.StatusFilter.Render("[" + desc + "]")
}

// formatLogLine renders one log event to a single line, sized to width.
// Order: time · level · client(colored) · message · field=value pairs.
// Every segment is truncated/elided so one event never exceeds `width`.
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

	// Estimated fixed prefix width so we don't blow the line.
	// 8 (time) + 1 + 5 (level) + 1 + 16 (client cap) + 1 = 32; be generous.
	msg := ev.Message
	fieldsStr := m.formatFields(ev.Fields)

	totalUsed := 8 + 1 + 5 + 1 + visualLen(clientPart) + 1
	remaining := width - totalUsed
	if remaining < 20 {
		remaining = 20
	}
	// Split remaining between message and fields — prefer message.
	msgW := remaining
	if fieldsStr != "" {
		msgW = remaining * 2 / 3
		if msgW < 20 {
			msgW = 20
		}
	}
	msg = trunc(msg, msgW)
	fieldsStr = trunc(fieldsStr, remaining-len([]rune(msg))-1)

	line := tPart + " " + lvPart + clientPart + " " + msg
	if fieldsStr != "" {
		line += " " + m.theme.Fields.Render(fieldsStr)
	}
	return line
}

func (m Model) formatFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	parts := []string{}
	// InterestingFields preserves order — iterate in config order for stability.
	seen := map[string]bool{}
	for _, key := range m.ds.InterestingFields {
		if v, ok := fields[key]; ok {
			short := strings.TrimPrefix(key, "fields.")
			parts = append(parts, fmt.Sprintf("%s=%v", short, v))
			seen[key] = true
		}
	}
	// Include anything else the config didn't list, alphabetical.
	extras := []string{}
	for k := range fields {
		if !seen[k] {
			extras = append(extras, k)
		}
	}
	// sort.Strings(extras) — small N, ordering doesn't matter enough to import.
	for _, k := range extras {
		short := strings.TrimPrefix(k, "fields.")
		parts = append(parts, fmt.Sprintf("%s=%v", short, fields[k]))
	}
	return strings.Join(parts, " ")
}

// ─── Right stack (stats/donut, throughput, error-rate) ───────────────────────

func (m Model) renderRightStack(width, height int) string {
	statsH := height * 42 / 100
	throughH := height * 30 / 100
	errH := height - statsH - throughH
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderStats(width, statsH),
		m.renderThroughput(width, throughH),
		m.renderErrors(width, errH),
	)
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
	rpm := 0
	if s.Total > 0 {
		rpm = int(s.Total / 60)
	}
	innerW := width - 4

	diameter := height - 6
	if diameter > 8 {
		diameter = 8
	}
	if diameter < 5 {
		diameter = 5
	}
	slices := []PieSlice{
		{Value: float64(s.Info), Color: "#7bed9f", Label: "info"},
		{Value: float64(s.Warn), Color: "#ffcc00", Label: "warn"},
		{Value: float64(s.Error), Color: "#ff4757", Label: "error"},
	}
	donut := renderDonutWithLegend(slices, diameter, m.theme)

	header := fmt.Sprintf("%s  %s events  %s",
		m.theme.PanelTitle.Render("Last Hour"),
		bold(fmt.Sprintf("%d", s.Total)),
		m.theme.TimeDim.Render(fmt.Sprintf("(~%d/min)", rpm)),
	)
	durations := fmt.Sprintf("avg %s   p95 %s   max %s",
		formatDuration(s.AvgDur),
		formatDuration(s.P95Dur),
		formatDuration(s.MaxDur),
	)
	sparkline := renderSparkline(m.errorSpark, innerW, m.theme.StatusError)

	lines := []string{header, "", donut, "", durations}
	if sparkline != "" {
		lines = append(lines, m.theme.TimeDim.Render("errors/min ")+sparkline)
	}
	body := strings.Join(lines, "\n")
	rendered := m.borderFor(FocusStats).Width(width - 2).Height(height - 2).Render(body)
	return zone.Mark(focusZone(FocusStats), rendered)
}

func (m Model) renderThroughput(width, height int) string {
	title := m.theme.PanelTitle.Render(" Throughput ")
	chartW := width - 2
	chartH := height - 3
	if chartH < 4 {
		chartH = 4
	}
	chart := renderLineChartNative(m.throughput, chartW, chartH)
	rendered := m.borderFor(FocusThroughput).Width(width - 2).Height(height - 2).Render(
		title + "\n" + chart,
	)
	return zone.Mark(focusZone(FocusThroughput), rendered)
}

func (m Model) renderErrors(width, height int) string {
	title := m.theme.PanelTitle.Render(" Errors & Warns (6h) ")
	chartW := width - 2
	chartH := height - 3
	if chartH < 4 {
		chartH = 4
	}
	chart := renderLineChartNative(m.errorRate, chartW, chartH)
	rendered := m.borderFor(FocusErrorTrend).Width(width - 2).Height(height - 2).Render(
		title + "\n" + chart,
	)
	return zone.Mark(focusZone(FocusErrorTrend), rendered)
}

// ─── Bottom row ──────────────────────────────────────────────────────────────

func (m Model) renderTopErrors(width, height int) string {
	title := m.theme.PanelTitle.Render(" Top Errors & Warns (1h) ")
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
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  all clear"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	rendered := m.borderFor(FocusTopErrors).Width(width - 2).Height(height - 2).Render(body)
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
	rendered := m.borderFor(FocusRecent).Width(width - 2).Height(height - 2).Render(body)
	return zone.Mark(focusZone(FocusRecent), rendered)
}

func (m Model) renderTopRoutes(width, height int) string {
	title := m.theme.PanelTitle.Render(" Top Routes (1h) ")
	innerW := width - 4
	innerH := height - 3
	rows := make([]string, 0, innerH)
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
	rendered := m.borderFor(FocusTopRoutes).Width(width - 2).Height(height - 2).Render(body)
	return zone.Mark(focusZone(FocusTopRoutes), rendered)
}

// ─── Footer + Help + Expand ──────────────────────────────────────────────────

func (m Model) renderFooter() string {
	// Left: keybinds; right: dataset + last refresh + toast.
	keys := []string{
		keyLabel(m.theme, "?", "help"),
		keyLabel(m.theme, "space", "pause"),
		keyLabel(m.theme, "/", "search"),
		keyLabel(m.theme, "c", "client"),
		keyLabel(m.theme, "esc", "reset"),
		keyLabel(m.theme, "tab", "focus"),
		keyLabel(m.theme, "enter", "expand"),
		keyLabel(m.theme, "r", "refresh"),
		keyLabel(m.theme, "q", "quit"),
	}
	left := strings.Join(keys, "  ")

	refreshAgo := "—"
	if !m.lastRefresh.IsZero() {
		refreshAgo = fmt.Sprintf("%ds ago", int(time.Since(m.lastRefresh).Seconds()))
	}
	rightParts := []string{
		m.theme.TimeDim.Render("focus: ") + m.theme.StatusKey.Render(focusName(m.focus)),
		m.theme.TimeDim.Render("dataset: ") + m.dataset,
		m.theme.TimeDim.Render("refresh: ") + refreshAgo,
	}
	if m.queryErr != nil {
		rightParts = append(rightParts,
			m.theme.StatusError.Render("⚠ "+trunc(m.queryErr.Error(), 40)))
	}
	if m.toast != "" && time.Now().Before(m.toastExpires) {
		rightParts = append(rightParts, m.theme.StatusFilter.Render("✓ "+m.toast))
	}
	right := strings.Join(rightParts, "  ")

	// Pad between left and right to fill width.
	gap := m.width - visualLen(left) - visualLen(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right

	return m.theme.StatusBar.Width(m.width).Render(line)
}

func keyLabel(t Theme, k, desc string) string {
	return t.StatusKey.Render(k) + " " + t.TimeDim.Render(desc)
}

// renderHelp is the ? overlay — a centered modal that lists every keybind.
func (m Model) renderHelp() string {
	rows := []string{
		m.theme.PanelTitle.Render("axiom-tui — keybinds"),
		"",
		"navigation",
		helpRow(m.theme, "j / k / ↓ / ↑", "scroll log stream"),
		helpRow(m.theme, "pgdn / pgup", "page through logs"),
		helpRow(m.theme, "g / G", "top / bottom (resume tail)"),
		helpRow(m.theme, "tab / l / →", "cycle focus forward"),
		helpRow(m.theme, "shift-tab / h / ←", "cycle focus backward"),
		helpRow(m.theme, "click", "focus any panel by clicking"),
		helpRow(m.theme, "space", "pause / resume stream"),
		"",
		"filters",
		helpRow(m.theme, "e / w / i", "toggle error / warn / info visibility"),
		helpRow(m.theme, "/", "search messages (substring)"),
		helpRow(m.theme, "c", "filter by client"),
		helpRow(m.theme, "esc / R", "reset all filters + resume tail"),
		"",
		"actions",
		helpRow(m.theme, "enter", "expand log line / drill into top error"),
		helpRow(m.theme, "y", "copy expanded event as JSON"),
		helpRow(m.theme, "r", "force refresh all queries"),
		helpRow(m.theme, "? / esc", "toggle help / close modal"),
		helpRow(m.theme, "q / ctrl-c", "quit"),
	}
	body := strings.Join(rows, "\n")
	modal := m.theme.Modal.Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
}

func helpRow(t Theme, k, desc string) string {
	return t.ModalKey.Render(padTo(k, 16)) + t.TimeDim.Render(desc)
}

// renderExpand is the Enter overlay — full event JSON, y to copy.
func (m Model) renderExpand() string {
	if m.expandedLog == nil {
		return ""
	}
	header := m.theme.PanelTitle.Render("event ") +
		m.theme.TimeDim.Render(m.expandedLog.Time.Local().Format(time.RFC3339))
	content := prettyJSON(m.expandedLog.Raw)
	// Cap modal height so huge events scroll virtually — simple top-only view.
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
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
}

// ─── small string helpers ────────────────────────────────────────────────────

func padTo(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func padRight(s string, n int) string {
	vl := visualLen(s)
	if vl >= n {
		return s
	}
	return s + strings.Repeat(" ", n-vl)
}

// visualLen estimates display width by stripping ANSI escapes. Lipgloss
// renders produce ESC[…m sequences that don't occupy cells.
func visualLen(s string) int {
	inEsc := false
	count := 0
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// skip
		default:
			count++
		}
	}
	return count
}

func bold(s string) string {
	return lipgloss.NewStyle().Bold(true).Render(s)
}
