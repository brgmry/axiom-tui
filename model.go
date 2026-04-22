package main

import (
	"context"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
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
	ModeExpand
	ModeHelp
)

// Focus tracks which interactive pane has keyboard focus. Only used for
// click-through on the top-errors table for now.
type Focus int

const (
	FocusLogs Focus = iota
	FocusErrors
)

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
	stats        Stats
	throughput   []Series
	errorRate    []Series
	topErrors    []TableRow
	topRoutes    []TableRow
	recentIssues []LogEvent
	errorSpark   []float64

	// UI state
	mode         Mode
	focus        Focus
	searchInput  textinput.Model
	clientInput  textinput.Model
	errorCursor  int
	expandedLog  *LogEvent

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

	return Model{
		cfg:          cfg,
		ds:           ds,
		dataset:      dataset,
		ax:           ax,
		keys:         DefaultKeyMap(),
		theme:        theme,
		logs:         NewLogBuffer(cfg.LogBufferSize),
		filter:       LogFilter{HideLevels: map[string]bool{}},
		searchInput:  search,
		clientInput:  clientIn,
		streamCursor: time.Now().Add(-5 * time.Second),
	}
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

	case toastMsg:
		m.toast = string(msg)
		m.toastExpires = time.Now().Add(3 * time.Second)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey dispatches on mode, then on key. Mode-specific handling first so
// text inputs can swallow keys like "e" and "/" that would otherwise trigger
// filters.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Escape works from any mode.
	if key.Matches(msg, m.keys.Escape) {
		switch m.mode {
		case ModeSearch, ModeClient:
			m.mode = ModeNormal
			m.searchInput.Blur()
			m.clientInput.Blur()
			return m, nil
		case ModeExpand, ModeHelp:
			m.mode = ModeNormal
			m.expandedLog = nil
			return m, nil
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
	case ModeExpand:
		if key.Matches(msg, m.keys.Yank) && m.expandedLog != nil {
			_ = clipboard.WriteAll(prettyJSON(m.expandedLog.Raw))
			return m, toastCmd("copied event to clipboard")
		}
		return m, nil
	case ModeHelp:
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
		if m.focus == FocusLogs {
			m.focus = FocusErrors
		} else {
			m.focus = FocusLogs
		}
		return m, nil

	case key.Matches(msg, m.keys.FocusLeft):
		if m.focus == FocusErrors {
			m.focus = FocusLogs
		} else {
			m.focus = FocusErrors
		}
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.focus == FocusErrors {
			if m.errorCursor > 0 {
				m.errorCursor--
			}
		} else {
			m.scrollOff++
			m.paused = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.focus == FocusErrors {
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
		if m.focus == FocusErrors && m.errorCursor < len(m.topErrors) {
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

	case key.Matches(msg, m.keys.ClearFilter):
		m.filter = LogFilter{HideLevels: map[string]bool{}}
		return m, toastCmd("filters cleared")
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
	)
}

func (m Model) fetchStats() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s, err := m.ax.Stats(ctx)
		return statsMsg{stats: s, err: err}
	}
}

func (m Model) fetchThroughput() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Prefer segmented when group_by_field is configured; fall back to plain.
		if m.ds.GroupByField != "" {
			series, err := m.ax.ThroughputSegmented(ctx, 5)
			if err == nil && len(series) > 0 {
				return throughputMsg{series: series, err: nil}
			}
		}
		single, err := m.ax.Throughput(ctx)
		return throughputMsg{series: []Series{single}, err: err}
	}
}

func (m Model) fetchErrorRate() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		series, err := m.ax.ErrorRate(ctx)
		if err != nil || len(series) == 0 {
			return errorRateMsg{series: series, err: err}
		}
		// Spark = error series downsampled to raw values.
		spark := make([]float64, len(series[0].Points))
		for i, p := range series[0].Points {
			spark[i] = p.Value
		}
		return errorRateMsg{series: series, spark: spark, err: nil}
	}
}

func (m Model) fetchTopErrors() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.TopErrors(ctx, 10)
		return topErrorsMsg{rows: rows, err: err}
	}
}

func (m Model) fetchTopRoutes() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := m.ax.TopRoutes(ctx, 10)
		return topRoutesMsg{rows: rows, err: err}
	}
}

func (m Model) fetchRecentIssues() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		events, err := m.ax.RecentIssues(ctx, 20)
		return recentIssuesMsg{events: events, err: err}
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
