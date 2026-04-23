package main

import (
	"context"
	"fmt"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
)

// ─── Modes ───────────────────────────────────────────────────────────────────
//
// Modes gate how keypresses are interpreted. Normal is the default; when a
// text input (search/client) is open, typed keys go to the input, not actions.

type Mode int

const (
	ModeNormal Mode = iota
	ModeSearch
	ModeClient
	ModeProvider
	ModeOperation
	ModeExpand
	ModeHelp
	ModeDatasetSwitcher
)

// Focus tracks which panel is "active" — keyboard actions (j/k, enter) and
// mouse clicks target the focused panel. Enumerate every visible panel so
// Tab cycles through all of them, not just logs ↔ errors.
type Focus int

const (
	FocusLogs Focus = iota
	FocusStats
	FocusThroughput
	FocusErrorTrend
	FocusTopErrors
	FocusRecent
	FocusTopRoutes
	numFocuses // sentinel — keep last
)

// focusName renders a focus enum for the footer/help.
func focusName(f Focus) string {
	switch f {
	case FocusLogs:
		return "logs"
	case FocusStats:
		return "stats"
	case FocusThroughput:
		return "throughput"
	case FocusErrorTrend:
		return "error trend"
	case FocusTopErrors:
		return "top errors"
	case FocusRecent:
		return "recent issues"
	case FocusTopRoutes:
		return "top routes"
	}
	return ""
}

// focusZone maps Focus enum → bubblezone id. Used by the mouse handler to
// translate a click into a focus change and by the renderer to wrap regions.
func focusZone(f Focus) string {
	switch f {
	case FocusLogs:
		return "zone-logs"
	case FocusStats:
		return "zone-stats"
	case FocusThroughput:
		return "zone-throughput"
	case FocusErrorTrend:
		return "zone-errortrend"
	case FocusTopErrors:
		return "zone-toperrors"
	case FocusRecent:
		return "zone-recent"
	case FocusTopRoutes:
		return "zone-toproutes"
	}
	return ""
}

// ─── Model ───────────────────────────────────────────────────────────────────

// Model is the single Bubble Tea model. Everything the UI renders reads off
// this struct — no globals, no hidden state.
type Model struct {
	cfg      Config
	ds       DatasetConfig
	dataset  string
	ax       *AxiomClient
	keys     KeyMap
	theme    Theme

	// Live log state
	logs       *LogBuffer
	filter     LogFilter
	paused     bool
	scrollOff  int // 0 = tailing; N = N lines above tail
	selectedIx int // cursor index into the filtered view

	// Aggregate state
	stats         Stats
	throughput    []Series
	errorRate     []Series
	topErrors     []TableRow
	topRoutes     []TableRow
	recentIssues  []LogEvent
	errorSpark    []float64
	clientHealth  []ClientHealth // per-client error+warn hot-spots
	throughSpark  []float64      // tiny inline sparkline for the footer
	costs         []ClientCost   // AI spend by client for the current window
	totalCost     float64        // grand total across all clients

	// Lookback window for aggregate queries (cycles via T key).
	timeRange time.Duration

	// UI state
	mode          Mode
	focus         Focus
	searchInput    textinput.Model
	clientInput    textinput.Model
	providerInput  textinput.Model
	operationInput textinput.Model
	errorCursor   int
	expandedLog   *LogEvent
	datasets      []string // names available for the switcher modal
	datasetCursor int      // index into datasets when modal is open
	presets       Presets  // loaded from disk on startup

	// Status + errors
	width, height int
	ready         bool
	lastRefresh   time.Time
	queryErr      error
	toast         string
	toastExpires  time.Time

	// Stream cursor — earliest time we still need to fetch from.
	streamCursor time.Time
}

// ─── Tea.Msg types ───────────────────────────────────────────────────────────

type tickMsg time.Time
type streamTickMsg time.Time

type statsMsg struct {
	stats Stats
	err   error
}
type throughputMsg struct {
	series []Series
	err    error
}
type errorRateMsg struct {
	series []Series
	spark  []float64
	err    error
}
type topErrorsMsg struct {
	rows []TableRow
	err  error
}
type topRoutesMsg struct {
	rows []TableRow
	err  error
}
type recentIssuesMsg struct {
	events []LogEvent
	err    error
}
type clientHealthMsg struct {
	rows []ClientHealth
	err  error
}
type costMsg struct {
	rows  []ClientCost
	total float64
	err   error
}
type logBatchMsg struct {
	events []LogEvent
	cursor time.Time
	err    error
}
type toastMsg string

// ─── Construction ────────────────────────────────────────────────────────────

func NewModel(cfg Config, dataset string, ax *AxiomClient) Model {
	theme := NewTheme()
	ds := cfg.DatasetOrDefault(dataset)

	search := textinput.New()
	search.Prompt = "/"
	search.CharLimit = 64
	search.Placeholder = "filter message…"

	clientIn := textinput.New()
	clientIn.Prompt = "@"
	clientIn.CharLimit = 64
	clientIn.Placeholder = "client name…"

	providerIn := textinput.New()
	providerIn.Prompt = "provider:"
	providerIn.CharLimit = 64
	providerIn.Placeholder = "provider name…"

	operationIn := textinput.New()
	operationIn.Prompt = "op:"
	operationIn.CharLimit = 64
	operationIn.Placeholder = "operation name…"

	// Available datasets for the switcher — names from the config file.
	datasets := make([]string, 0, len(cfg.Datasets))
	for name := range cfg.Datasets {
		datasets = append(datasets, name)
	}
	if len(datasets) == 0 || cfg.DefaultDataset != "" {
		// Always include the active dataset even if not in config.
		seen := false
		for _, n := range datasets {
			if n == dataset {
				seen = true
				break
			}
		}
		if !seen {
			datasets = append(datasets, dataset)
		}
	}

	presets, _ := LoadPresets()

	return Model{
		cfg:          cfg,
		ds:           ds,
		dataset:      dataset,
		ax:           ax,
		keys:         DefaultKeyMap(),
		theme:        theme,
		logs:           NewLogBuffer(cfg.LogBufferSize),
		filter:         LogFilter{HideLevels: map[string]bool{}},
		searchInput:    search,
		clientInput:    clientIn,
		providerInput:  providerIn,
		operationInput: operationIn,
		streamCursor:   time.Now().Add(-5 * time.Second),
		timeRange:    time.Hour,
		datasets:     datasets,
		presets:      presets,
	}
}

// timeRangeOptions enumerates the supported lookback windows for the T-cycle.
var timeRangeOptions = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

func nextTimeRange(cur time.Duration) time.Duration {
	for i, d := range timeRangeOptions {
		if d == cur {
			return timeRangeOptions[(i+1)%len(timeRangeOptions)]
		}
	}
	return time.Hour
}

func formatRange(d time.Duration) string {
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// Init kicks off the first refresh + starts both tickers.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.refreshAll(),
		m.streamTick(),
		tickEvery(time.Duration(m.cfg.RefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
			return tickMsg(t)
		}),
	)
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.searchInput.Width = msg.Width / 3
		m.clientInput.Width = msg.Width / 3
		m.providerInput.Width = msg.Width / 3
		m.operationInput.Width = msg.Width / 3
		return m, nil

	case tickMsg:
		// Periodic aggregate refresh.
		return m, tea.Batch(
			m.refreshAll(),
			tickEvery(time.Duration(m.cfg.RefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
				return tickMsg(t)
			}),
		)

	case streamTickMsg:
		if m.paused {
			return m, m.streamTick()
		}
		return m, tea.Batch(m.fetchLogs(), m.streamTick())

	case logBatchMsg:
		if msg.err == nil {
			m.logs.Append(msg.events)
			if !msg.cursor.IsZero() {
				m.streamCursor = msg.cursor
			}
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case statsMsg:
		if msg.err == nil {
			m.stats = msg.stats
			m.lastRefresh = time.Now()
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case throughputMsg:
		if msg.err == nil {
			m.throughput = msg.series
		}
		return m, nil

	case errorRateMsg:
		if msg.err == nil {
			m.errorRate = msg.series
			m.errorSpark = msg.spark
		}
		return m, nil

	case topErrorsMsg:
		if msg.err == nil {
			m.topErrors = msg.rows
			if m.errorCursor >= len(m.topErrors) {
				m.errorCursor = len(m.topErrors) - 1
			}
			if m.errorCursor < 0 {
				m.errorCursor = 0
			}
		}
		return m, nil

	case topRoutesMsg:
		if msg.err == nil {
			m.topRoutes = msg.rows
		}
		return m, nil

	case recentIssuesMsg:
		if msg.err == nil {
			m.recentIssues = msg.events
		}
		return m, nil

	case clientHealthMsg:
		if msg.err == nil {
			m.clientHealth = msg.rows
		}
		return m, nil

	case costMsg:
		if msg.err == nil {
			m.costs = msg.rows
			m.totalCost = msg.total
		}
		return m, nil

	case toastMsg:
		m.toast = string(msg)
		m.toastExpires = time.Now().Add(3 * time.Second)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// handleMouse routes click events:
//   - Left click on a top-error row → drill the stream into that message.
//   - Right click on a log row → copy that event's full JSON to clipboard.
//   - Left click on any panel → focus it.
//
// Drag/scroll events are ignored for now (would map to log-pane scrolling).
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// Right-click on a log row → copy its raw JSON.
	if msg.Button == tea.MouseButtonRight {
		filtered := m.logs.Filtered(m.filter)
		for i := range filtered {
			z := zone.Get(fmt.Sprintf("logrow-%d", i))
			if z != nil && z.InBounds(msg) {
				_ = clipboard.WriteAll(prettyJSON(filtered[i].Raw))
				return m, toastCmd("copied event to clipboard")
			}
		}
		return m, nil
	}

	if msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// Click on a top-error row → set search filter to its message + focus stream.
	for i, r := range m.topErrors {
		z := zone.Get(fmt.Sprintf("toperror-%d", i))
		if z != nil && z.InBounds(msg) {
			m.filter.Search = r.Message
			m.focus = FocusLogs
			m.errorCursor = i
			return m, toastCmd("filtered by selected error")
		}
	}

	// Otherwise — focus the clicked panel.
	for f := Focus(0); f < numFocuses; f++ {
		z := zone.Get(focusZone(f))
		if z != nil && z.InBounds(msg) {
			m.focus = f
			return m, nil
		}
	}
	return m, nil
}

// handleKey dispatches on mode, then on key. Mode-specific handling first so
// text inputs can swallow keys like "e" and "/" that would otherwise trigger
// filters.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Escape works from any mode — closes modals/inputs first, then acts as a
	// "reset" in normal mode (clears all filters + resumes tailing).
	if key.Matches(msg, m.keys.Escape) {
		switch m.mode {
		case ModeSearch, ModeClient, ModeProvider, ModeOperation:
			m.mode = ModeNormal
			m.searchInput.Blur()
			m.clientInput.Blur()
			m.providerInput.Blur()
			m.operationInput.Blur()
			return m, nil
		case ModeExpand, ModeHelp, ModeDatasetSwitcher:
			m.mode = ModeNormal
			m.expandedLog = nil
			return m, nil
		case ModeNormal:
			// Reset to defaults: clear filters, unpause, jump to tail.
			m.filter = LogFilter{HideLevels: map[string]bool{}}
			m.paused = false
			m.scrollOff = 0
			return m, toastCmd("reset — filters cleared, tailing resumed")
		}
	}

	// Text input modes absorb most keys.
	switch m.mode {
	case ModeSearch:
		if msg.Type == tea.KeyEnter {
			m.filter.Search = m.searchInput.Value()
			m.mode = ModeNormal
			m.searchInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	case ModeClient:
		if msg.Type == tea.KeyEnter {
			m.filter.Client = m.clientInput.Value()
			m.mode = ModeNormal
			m.clientInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.clientInput, cmd = m.clientInput.Update(msg)
		return m, cmd
	case ModeProvider:
		if msg.Type == tea.KeyEnter {
			m.filter.Provider = m.providerInput.Value()
			m.mode = ModeNormal
			m.providerInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.providerInput, cmd = m.providerInput.Update(msg)
		return m, cmd
	case ModeOperation:
		if msg.Type == tea.KeyEnter {
			m.filter.Operation = m.operationInput.Value()
			m.mode = ModeNormal
			m.operationInput.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.operationInput, cmd = m.operationInput.Update(msg)
		return m, cmd
	case ModeExpand:
		if key.Matches(msg, m.keys.Yank) && m.expandedLog != nil {
			_ = clipboard.WriteAll(prettyJSON(m.expandedLog.Raw))
			return m, toastCmd("copied event to clipboard")
		}
		return m, nil
	case ModeHelp:
		return m, nil
	case ModeDatasetSwitcher:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.datasetCursor > 0 {
				m.datasetCursor--
			}
		case key.Matches(msg, m.keys.Down):
			if m.datasetCursor < len(m.datasets)-1 {
				m.datasetCursor++
			}
		case msg.Type == tea.KeyEnter && len(m.datasets) > 0:
			next := m.datasets[m.datasetCursor]
			if next != m.dataset {
				m.dataset = next
				m.ds = m.cfg.DatasetOrDefault(next)
				if ax, err := NewAxiomClient(next, m.ds); err == nil {
					m.ax = ax
				}
				// Wipe the buffer so we don't mix datasets.
				m.logs = NewLogBuffer(m.cfg.LogBufferSize)
				m.streamCursor = time.Now().Add(-5 * time.Second)
				m.mode = ModeNormal
				return m, tea.Batch(m.refreshAll(), toastCmd("dataset → "+next))
			}
			m.mode = ModeNormal
		}
		return m, nil
	}

	// Normal mode
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Refresh):
		return m, tea.Batch(m.refreshAll(), toastCmd("refreshed"))

	case key.Matches(msg, m.keys.Help):
		m.mode = ModeHelp
		return m, nil

	case key.Matches(msg, m.keys.Pause):
		m.paused = !m.paused
		if m.paused {
			return m, toastCmd("paused — space to resume")
		}
		m.scrollOff = 0
		return m, toastCmd("resumed")

	case key.Matches(msg, m.keys.Tab), key.Matches(msg, m.keys.FocusRight):
		m.focus = (m.focus + 1) % numFocuses
		return m, nil

	case key.Matches(msg, m.keys.FocusLeft):
		m.focus = (m.focus - 1 + numFocuses) % numFocuses
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.focus == FocusTopErrors {
			if m.errorCursor > 0 {
				m.errorCursor--
			}
		} else {
			m.scrollOff++
			m.paused = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.focus == FocusTopErrors {
			if m.errorCursor < len(m.topErrors)-1 {
				m.errorCursor++
			}
		} else if m.scrollOff > 0 {
			m.scrollOff--
		}
		return m, nil

	case key.Matches(msg, m.keys.PageUp):
		m.scrollOff += 10
		m.paused = true
		return m, nil

	case key.Matches(msg, m.keys.PageDown):
		m.scrollOff -= 10
		if m.scrollOff < 0 {
			m.scrollOff = 0
		}
		return m, nil

	case key.Matches(msg, m.keys.Top):
		m.scrollOff = m.logs.Len()
		m.paused = true
		return m, nil

	case key.Matches(msg, m.keys.Bottom):
		m.scrollOff = 0
		m.paused = false
		return m, toastCmd("tailing live")

	case key.Matches(msg, m.keys.Enter):
		if m.focus == FocusTopErrors && m.errorCursor < len(m.topErrors) {
			m.filter.Search = m.topErrors[m.errorCursor].Message
			m.focus = FocusLogs
			return m, toastCmd("filtered by selected error")
		}
		// Expand selected log line from the filtered view.
		filtered := m.logs.Filtered(m.filter)
		if idx := m.visibleIndex(filtered); idx >= 0 && idx < len(filtered) {
			ev := filtered[idx]
			m.expandedLog = &ev
			m.mode = ModeExpand
		}
		return m, nil

	case key.Matches(msg, m.keys.ToggleError):
		m.filter.HideLevels["error"] = !m.filter.HideLevels["error"]
		return m, nil
	case key.Matches(msg, m.keys.ToggleWarn):
		m.filter.HideLevels["warn"] = !m.filter.HideLevels["warn"]
		return m, nil
	case key.Matches(msg, m.keys.ToggleInfo):
		m.filter.HideLevels["info"] = !m.filter.HideLevels["info"]
		return m, nil

	case key.Matches(msg, m.keys.Search):
		m.mode = ModeSearch
		m.searchInput.SetValue(m.filter.Search)
		m.searchInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.ClientFilter):
		m.mode = ModeClient
		m.clientInput.SetValue(m.filter.Client)
		m.clientInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.ProviderFilter):
		m.mode = ModeProvider
		m.providerInput.SetValue(m.filter.Provider)
		m.providerInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.OperationFilter):
		m.mode = ModeOperation
		m.operationInput.SetValue(m.filter.Operation)
		m.operationInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.TraceRequest):
		// Only traceable from the logs panel — other focuses have no row.
		if m.focus != FocusLogs {
			return m, nil
		}
		filtered := m.logs.Filtered(m.filter)
		idx := m.visibleIndex(filtered)
		if idx < 0 || idx >= len(filtered) {
			return m, nil
		}
		rid := asFieldString(filtered[idx].Fields["fields.requestId"])
		if rid == "" {
			return m, toastCmd("no requestId on selected row")
		}
		m.filter.RequestId = rid
		short := rid
		if len(short) > 8 {
			short = short[:8]
		}
		return m, toastCmd("trace " + short)

	case key.Matches(msg, m.keys.ClearFilter):
		m.filter = LogFilter{HideLevels: map[string]bool{}}
		return m, toastCmd("filters cleared")

	case key.Matches(msg, m.keys.TimeRange):
		m.timeRange = nextTimeRange(m.timeRange)
		return m, tea.Batch(
			m.refreshAll(),
			toastCmd(fmt.Sprintf("time range → %s", formatRange(m.timeRange))),
		)

	case key.Matches(msg, m.keys.DatasetSwitcher):
		// Snap cursor to the active dataset for clearer UX.
		for i, n := range m.datasets {
			if n == m.dataset {
				m.datasetCursor = i
				break
			}
		}
		m.mode = ModeDatasetSwitcher
		return m, nil

	case key.Matches(msg, m.keys.SavePreset):
		slot := m.presets.NextSlot()
		if slot == "" {
			return m, toastCmd("all preset slots full (1-9) — load + delete first")
		}
		preset := FilterToPreset(m.filter)
		if m.presets.Items == nil {
			m.presets.Items = map[string]Preset{}
		}
		m.presets.Items[slot] = preset
		if err := m.presets.Save(); err != nil {
			return m, toastCmd("save failed: " + err.Error())
		}
		return m, toastCmd(fmt.Sprintf("saved as preset %s — %s", slot, preset.Name))
	}

	// Number keys 1-9 load the corresponding preset (only in normal mode,
	// not when an input is focused).
	if len(msg.Runes) == 1 {
		ch := msg.Runes[0]
		if ch >= '1' && ch <= '9' {
			slot := string(ch)
			if p, ok := m.presets.Items[slot]; ok {
				m.filter = PresetToFilter(p)
				m.scrollOff = 0
				return m, toastCmd("preset " + slot + " — " + p.Name)
			}
		}
	}
	return m, nil
}

// visibleIndex returns the index into `filtered` that corresponds to the
// user's scroll position. When tailing (scrollOff=0) we point at the last
// line; when scrolled up, we point `scrollOff` lines above the tail.
func (m Model) visibleIndex(filtered []LogEvent) int {
	if len(filtered) == 0 {
		return -1
	}
	return len(filtered) - 1 - m.scrollOff
}

// ─── Command builders ────────────────────────────────────────────────────────

func (m Model) refreshAll() tea.Cmd {
	return tea.Batch(
		m.fetchStats(),
		m.fetchThroughput(),
		m.fetchErrorRate(),
		m.fetchTopErrors(),
		m.fetchTopRoutes(),
		m.fetchRecentIssues(),
		m.fetchClientHealth(),
		m.fetchCost(),
	)
}

// fetchCost sums fields.costDollars by client over the current window. Runs
// alongside stats on every refresh tick — same cadence as the Last Hour panel.
func (m Model) fetchCost() tea.Cmd {
	tr := m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.QueryCost(ctx, tr)
		if err != nil {
			return costMsg{err: err}
		}
		total := 0.0
		for _, r := range rows {
			total += r.Dollars
		}
		return costMsg{rows: rows, total: total, err: nil}
	}
}

func (m Model) fetchStats() tea.Cmd {
	tr := m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s, err := m.ax.Stats(ctx, tr)
		return statsMsg{stats: s, err: err}
	}
}

func (m Model) fetchThroughput() tea.Cmd {
	tr := m.timeRange
	groupBy := m.ds.GroupByField
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if groupBy != "" {
			series, err := m.ax.ThroughputSegmented(ctx, 5, tr)
			if err == nil && len(series) > 0 {
				return throughputMsg{series: series, err: nil}
			}
		}
		single, err := m.ax.Throughput(ctx, tr)
		return throughputMsg{series: []Series{single}, err: err}
	}
}

func (m Model) fetchErrorRate() tea.Cmd {
	// Error rate panel uses a longer lookback (max(timeRange*4, 6h)) so the
	// trend is informative even when stats are short-window.
	tr := m.timeRange * 4
	if tr < 6*time.Hour {
		tr = 6 * time.Hour
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		series, err := m.ax.ErrorRate(ctx, tr)
		if err != nil || len(series) == 0 {
			return errorRateMsg{series: series, err: err}
		}
		spark := make([]float64, len(series[0].Points))
		for i, p := range series[0].Points {
			spark[i] = p.Value
		}
		return errorRateMsg{series: series, spark: spark, err: nil}
	}
}

func (m Model) fetchTopErrors() tea.Cmd {
	tr := m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.TopErrors(ctx, 10, tr)
		return topErrorsMsg{rows: rows, err: err}
	}
}

func (m Model) fetchTopRoutes() tea.Cmd {
	tr := m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.TopRoutes(ctx, 10, tr)
		return topRoutesMsg{rows: rows, err: err}
	}
}

func (m Model) fetchRecentIssues() tea.Cmd {
	tr := m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		events, err := m.ax.RecentIssues(ctx, 20, tr)
		return recentIssuesMsg{events: events, err: err}
	}
}

// Health uses a fixed 15m window — short enough that the top bar reflects
// current state, not stale incidents from earlier in the day.
func (m Model) fetchClientHealth() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.TopClientHealth(ctx, 5, 15*time.Minute)
		return clientHealthMsg{rows: rows, err: err}
	}
}

func (m Model) fetchLogs() tea.Cmd {
	cursor := m.streamCursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		events, err := m.ax.StreamSince(ctx, cursor)
		if err != nil {
			return logBatchMsg{err: err}
		}
		next := cursor
		for _, ev := range events {
			if ev.Time.After(next) {
				next = ev.Time
			}
		}
		return logBatchMsg{events: events, cursor: next, err: nil}
	}
}

func (m Model) streamTick() tea.Cmd {
	d := time.Duration(m.cfg.StreamPollMs) * time.Millisecond
	return tea.Tick(d, func(t time.Time) tea.Msg { return streamTickMsg(t) })
}

func tickEvery(d time.Duration, f func(time.Time) tea.Msg) tea.Cmd {
	return tea.Tick(d, f)
}

func toastCmd(s string) tea.Cmd {
	return func() tea.Msg { return toastMsg(s) }
}
