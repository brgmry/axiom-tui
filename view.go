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

	headerH := 1 // top-bar per-client health
	footerH := 2
	bottomH := 8
	topH := m.height - headerH - footerH - bottomH
	if topH < 10 {
		topH = 10
	}

	leftW := m.width * 5 / 12
	rightW := m.width - leftW

	headerBar := m.renderTopBar()

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

	full := lipgloss.JoinVertical(lipgloss.Left, headerBar, topRow, bottomRow, m.renderFooter())
	// Bubblezone scans the rendered output for marker sequences and resolves
	// them to bounding boxes the mouse handler can hit-test against.
	return zone.Scan(full)
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
		m.theme.PanelTitle.Render("AXIOM"),
		m.dataset,
	) + statusHint + "  " + m.filterSummary()

	// Reserve rows: 1 for title, 0-1 for prompt input.
	bodyH := innerH - 1
	var promptLine string
	switch m.mode {
	case ModeSearch:
		promptLine = m.searchInput.View()
		bodyH--
	case ModeClient:
		promptLine = m.clientInput.View()
		bodyH--
	}
	if bodyH < 1 {
		bodyH = 1
	}

	filtered := m.logs.Filtered(m.filter)
	end := len(filtered) - m.scrollOff
	if end > len(filtered) {
		end = len(filtered)
	}
	if end < 0 {
		end = 0
	}
	start := end - bodyH
	if start < 0 {
		start = 0
	}
	slice := filtered[start:end]

	bodyLines := make([]string, 0, bodyH)
	selectedIdx := len(slice) - 1
	// Absolute index into the filtered slice (start..end) for click→event mapping.
	for i, ev := range slice {
		line := m.formatLogLine(ev, innerW)
		if m.focus == FocusLogs && i == selectedIdx && m.scrollOff > 0 {
			line = m.theme.Selected.Render(padRight(line, innerW))
		}
		// Mark each row with a unique zone so right-click → copy can resolve it.
		bodyLines = append(bodyLines, zone.Mark(fmt.Sprintf("logrow-%d", start+i), line))
	}
	// Empty-state hint when nothing's showing.
	if len(slice) == 0 && bodyH > 2 {
		var hint string
		if m.filter.Active() {
			hint = m.theme.TimeDim.Render("  no logs match the active filter — esc to reset")
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

	// Force exact innerW × innerH dimensions — lipgloss pads with spaces. This
	// is what makes the border render reliably regardless of content length.
	innerSized := lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, inner)
	rendered := m.borderFor(FocusLogs).Render(innerSized)
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

	totalUsed := 8 + 1 + 5 + 1 + visualLen(clientPart) + 1
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
	fieldsStr = trunc(fieldsStr, remaining-len([]rune(msg))-1)

	// Apply search highlight LAST so it wraps already-truncated text.
	msgRendered := highlightNeedle(msg, m.filter.Search)

	line := tPart + " " + lvPart + clientPart + " " + msgRendered
	if fieldsStr != "" {
		line += " " + m.theme.Fields.Render(highlightNeedle(fieldsStr, m.filter.Search))
	}
	return line
}

// highlightNeedle wraps every case-insensitive occurrence of needle in s
// with a bold/cyan style. No-op when needle is empty.
func highlightNeedle(s, needle string) string {
	if needle == "" {
		return s
	}
	lower := strings.ToLower(s)
	low := strings.ToLower(needle)
	hl := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#00e5ff")).
		Bold(true).
		Underline(true)
	var out strings.Builder
	cursor := 0
	for {
		idx := strings.Index(lower[cursor:], low)
		if idx < 0 {
			out.WriteString(s[cursor:])
			break
		}
		abs := cursor + idx
		out.WriteString(s[cursor:abs])
		out.WriteString(hl.Render(s[abs : abs+len(needle)]))
		cursor = abs + len(needle)
	}
	return out.String()
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
	// Stats is information-dense (header + 3 bars + duration + sparkline = ~9 rows).
	// Cap it tight so the freed space goes to throughput + errors charts which
	// genuinely benefit from more rows.
	statsH := 10
	if statsH > height/3 {
		statsH = height / 3
	}
	remaining := height - statsH
	throughH := remaining * 55 / 100
	errH := remaining - throughH
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
	innerW := width - 2
	innerH := height - 2

	// Health indicator dot — at-a-glance signal in the title row.
	dotColor := "#7bed9f" // green
	switch {
	case s.Total > 0 && float64(s.Error)/float64(s.Total) > 0.05:
		dotColor = "#ff4757" // red
	case s.Total > 0 && float64(s.Warn)/float64(s.Total) > 0.10:
		dotColor = "#ffcc00" // yellow
	}
	healthDot := lipgloss.NewStyle().Foreground(lipgloss.Color(dotColor)).Render("●")

	header := fmt.Sprintf("%s %s  %s  %s events  %s",
		m.theme.PanelTitle.Render("Last Hour"),
		healthDot,
		"",
		bold(fmt.Sprintf("%d", s.Total)),
		m.theme.TimeDim.Render(fmt.Sprintf("(~%d/min)", rpm)),
	)

	slices := []PieSlice{
		{Value: float64(s.Info), Color: "#7bed9f", Label: "info"},
		{Value: float64(s.Warn), Color: "#ffcc00", Label: "warn"},
		{Value: float64(s.Error), Color: "#ff4757", Label: "error"},
	}
	bars := renderHorizontalBars(slices, innerW-2, m.theme)

	durations := fmt.Sprintf("avg %s   p95 %s   max %s",
		formatDuration(s.AvgDur),
		formatDuration(s.P95Dur),
		formatDuration(s.MaxDur),
	)

	parts := []string{header, "", bars}
	parts = append(parts, "", m.theme.TimeDim.Render(durations))
	if sparkline := renderSparkline(m.errorSpark, innerW-12, m.theme.StatusError); sparkline != "" {
		parts = append(parts, m.theme.TimeDim.Render("errors/min ")+sparkline)
	}
	body := strings.Join(parts, "\n")

	innerSized := lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, body)
	rendered := m.borderFor(FocusStats).Render(innerSized)
	return zone.Mark(focusZone(FocusStats), rendered)
}

func (m Model) renderThroughput(width, height int) string {
	innerW := width - 2
	innerH := height - 2
	title := m.theme.PanelTitle.Render(" Throughput ")
	chartH := innerH - 1
	if chartH < 4 {
		chartH = 4
	}
	chart := renderLineChartNative(m.throughput, innerW, chartH)
	body := title + "\n" + chart
	innerSized := lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, body)
	rendered := m.borderFor(FocusThroughput).Render(innerSized)
	return zone.Mark(focusZone(FocusThroughput), rendered)
}

func (m Model) renderErrors(width, height int) string {
	innerW := width - 2
	innerH := height - 2
	title := m.theme.PanelTitle.Render(" Errors & Warns (6h) ")
	chartH := innerH - 1
	if chartH < 4 {
		chartH = 4
	}
	chart := renderLineChartNative(m.errorRate, innerW, chartH)
	body := title + "\n" + chart
	innerSized := lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, body)
	rendered := m.borderFor(FocusErrorTrend).Render(innerSized)
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
		// Each row gets its own zone — click drills into the stream filter.
		rows = append(rows, zone.Mark(fmt.Sprintf("toperror-%d", i), line))
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.TimeDim.Render("  all clear"))
	}
	body := title + "\n" + strings.Join(rows, "\n")
	innerSized := lipgloss.Place(width-2, height-2, lipgloss.Left, lipgloss.Top, body)
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
	innerSized := lipgloss.Place(width-2, height-2, lipgloss.Left, lipgloss.Top, body)
	rendered := m.borderFor(FocusRecent).Render(innerSized)
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
	innerSized := lipgloss.Place(width-2, height-2, lipgloss.Left, lipgloss.Top, body)
	rendered := m.borderFor(FocusTopRoutes).Render(innerSized)
	return zone.Mark(focusZone(FocusTopRoutes), rendered)
}

// ─── Top bar — per-client health summary ─────────────────────────────────────

// renderTopBar shows a one-row strip listing the noisiest clients in the last
// 15min by error+warn count. Helps you spot "client X is on fire" without
// touching any panel.
func (m Model) renderTopBar() string {
	if len(m.clientHealth) == 0 {
		hint := m.theme.TimeDim.Render(" health (15m): no errors or warns")
		return m.theme.StatusBar.Width(m.width).Render(hint)
	}
	parts := []string{m.theme.PanelTitle.Render(" health (15m): ")}
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
	parts = append(parts, m.theme.TimeDim.Render(fmt.Sprintf("  range: %s", formatRange(m.timeRange))))
	line := strings.Join(parts, "  ")
	return m.theme.StatusBar.Width(m.width).Render(trunc(line, m.width))
}

// ─── Footer + Help + Expand ──────────────────────────────────────────────────

func (m Model) renderFooter() string {
	// Left: keybinds; right: dataset + last refresh + toast.
	keys := []string{
		keyLabel(m.theme, "?", "help"),
		keyLabel(m.theme, "space", "pause"),
		keyLabel(m.theme, "/", "search"),
		keyLabel(m.theme, "T", "range"),
		keyLabel(m.theme, "D", "dataset"),
		keyLabel(m.theme, "esc", "reset"),
		keyLabel(m.theme, "tab", "focus"),
		keyLabel(m.theme, "enter", "expand"),
		keyLabel(m.theme, "q", "quit"),
	}
	left := strings.Join(keys, "  ")

	refreshAgo := "—"
	if !m.lastRefresh.IsZero() {
		refreshAgo = fmt.Sprintf("%ds ago", int(time.Since(m.lastRefresh).Seconds()))
	}
	// Inline throughput sparkline — always-visible signal even when scrolled away.
	throughSpark := ""
	if len(m.throughput) > 0 {
		vals := make([]float64, 0, len(m.throughput[0].Points))
		for _, p := range m.throughput[0].Points {
			vals = append(vals, p.Value)
		}
		throughSpark = renderSparkline(vals, 14, m.theme.PanelTitle) + " "
	}
	rightParts := []string{
		throughSpark + m.theme.TimeDim.Render("rpm"),
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
