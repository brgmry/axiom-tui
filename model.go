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
	ModeSourceSwitcher
)

// Focus tracks which panel is "active": keyboard actions (j/k, enter) and
// mouse clicks target the focused panel. Enumerate every visible panel so
// Tab cycles through all of them, not just logs and errors.
type Focus int

const (
	FocusLogs Focus = iota
	FocusStats
	FocusThroughput
	FocusErrorTrend
	FocusTopErrors
	FocusRecent
	FocusTopRoutes // slot for whichever panel is drawn bottom right
	numFocuses     // sentinel, keep last
)

// focusName renders a focus enum for the footer/help.
func (m Model) focusName(f Focus) string {
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
		if m.agents != nil {
			return "agents"
		}
		return "top routes"
	}
	return ""
}

// focusZone maps Focus enum to a bubblezone id. Used by the mouse handler to
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
// this struct: no globals, no hidden state. The model only knows the Source
// interface; which backend sits behind it is the factory's business.
type Model struct {
	cfg    Config
	name   string
	src    Source
	agents AgentSource // non-nil when src implements AgentSource
	info   SourceInfo
	keys   KeyMap
	theme  Theme

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
	clientHealth []ClientHealth // per-group error+warn hot-spots
	throughSpark []float64      // tiny inline sparkline for the footer
	cost         CostReport     // spend for the current window
	agentStats   []AgentStat    // rows of the Agents panel

	// Lookback window for aggregate queries (cycles via T key).
	timeRange time.Duration

	// UI state
	mode           Mode
	focus          Focus
	searchInput    textinput.Model
	clientInput    textinput.Model
	providerInput  textinput.Model
	operationInput textinput.Model
	errorCursor    int
	expandedLog    *LogEvent
	sources        []string // names available for the switcher modal
	sourceCursor   int      // index into sources when modal is open
	presets        Presets  // loaded from disk on startup

	// Status + errors
	width, height int
	ready         bool
	lastRefresh   time.Time
	queryErr      error
	toast         string
	toastExpires  time.Time

	// Stream cursor: where the next Stream call continues from.
	streamCursor Cursor

	// loadingSeen records that the source was still backfilling on the last
	// stream tick, so the tick after it finishes refreshes the panels once
	// more instead of waiting for the next refresh_seconds tick.
	loadingSeen bool
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
	report CostReport
	err    error
}
type agentsMsg struct {
	rows []AgentStat
	err  error
}
type logBatchMsg struct {
	events []LogEvent
	cursor Cursor
	err    error
}
type toastMsg string

// ─── Construction ────────────────────────────────────────────────────────────

func NewModel(cfg Config, name string, src Source) Model {
	theme := NewTheme()
	info := src.Info()

	search := textinput.New()
	search.Prompt = "/"
	search.CharLimit = 64
	search.Placeholder = "filter message…"

	clientIn := textinput.New()
	clientIn.Prompt = "@"
	clientIn.CharLimit = 64

	providerIn := textinput.New()
	providerIn.CharLimit = 64

	operationIn := textinput.New()
	operationIn.CharLimit = 64

	presets, _ := LoadPresets()

	m := Model{
		cfg:            cfg,
		keys:           DefaultKeyMap(),
		theme:          theme,
		logs:           NewLogBuffer(cfg.LogBufferSize),
		searchInput:    search,
		clientInput:    clientIn,
		providerInput:  providerIn,
		operationInput: operationIn,
		timeRange:      time.Hour,
		presets:        presets,
	}
	m.adoptSource(name, src, info)
	return m
}

// adoptSource points the model at a freshly opened source: capability
// lookup, prompt labels, startup filter and an empty stream.
func (m *Model) adoptSource(name string, src Source, info SourceInfo) {
	m.name = name
	m.src = src
	m.info = info
	m.agents, _ = src.(AgentSource)
	m.logs = NewLogBuffer(m.cfg.LogBufferSize)
	m.filter = NewLogFilter(info)
	m.streamCursor = Cursor{}
	m.stats = Stats{}
	m.throughput = nil
	m.errorRate = nil
	m.topErrors = nil
	m.topRoutes = nil
	m.recentIssues = nil
	m.errorSpark = nil
	m.clientHealth = nil
	m.cost = CostReport{}
	m.agentStats = nil
	m.queryErr = nil
	m.scrollOff = 0
	m.errorCursor = 0
	m.loadingSeen = false

	m.clientInput.Placeholder = info.GroupLabel + " name…"
	m.providerInput.Prompt = info.Label(info.Filters.Provider) + ":"
	m.providerInput.Placeholder = info.Label(info.Filters.Provider) + "…"
	m.operationInput.Prompt = info.Label(info.Filters.Operation) + ":"
	m.operationInput.Placeholder = info.Label(info.Filters.Operation) + "…"

	// Switcher list: every configured source (built-in pi included) plus
	// the active one when it was created ad hoc.
	m.sources = m.cfg.SourceNames()
	seen := false
	for _, n := range m.sources {
		if n == name {
			seen = true
			break
		}
	}
	if !seen {
		m.sources = append(m.sources, name)
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
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// trendRange is the lookback of the error trend chart: max(window*4, 6h) so
// the trend is informative even when stats are short-window.
func (m Model) trendRange() time.Duration {
	tr := m.timeRange * 4
	if tr < 6*time.Hour {
		tr = 6 * time.Hour
	}
	return tr
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
		// Periodic aggregate refresh. A stale error from the previous cycle
		// is dropped; any method that still fails sets it again.
		m.queryErr = nil
		return m, tea.Batch(
			m.refreshAll(),
			tickEvery(time.Duration(m.cfg.RefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
				return tickMsg(t)
			}),
		)

	case streamTickMsg:
		cmds := []tea.Cmd{m.streamTick()}
		if !m.paused {
			cmds = append(cmds, m.fetchLogs())
		}
		// While a file source backfills, the panels render whatever is
		// loaded so far: refresh them on the stream cadence, and once more
		// on the first tick after loading completes.
		if ps, ok := m.src.(ProgressSource); ok {
			loading := ps.Progress().Loading
			if loading || m.loadingSeen {
				cmds = append(cmds, m.refreshAll())
			}
			m.loadingSeen = loading
		}
		return m, tea.Batch(cmds...)

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
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case errorRateMsg:
		if msg.err == nil {
			m.errorRate = msg.series
			m.errorSpark = msg.spark
		} else {
			m.queryErr = msg.err
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
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case topRoutesMsg:
		if msg.err == nil {
			m.topRoutes = msg.rows
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case recentIssuesMsg:
		if msg.err == nil {
			m.recentIssues = msg.events
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case clientHealthMsg:
		if msg.err == nil {
			m.clientHealth = msg.rows
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case costMsg:
		if msg.err == nil {
			m.cost = msg.report
		} else {
			m.queryErr = msg.err
		}
		return m, nil

	case agentsMsg:
		if msg.err == nil {
			m.agentStats = msg.rows
		} else {
			m.queryErr = msg.err
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

// hydrate fetches the full raw object for an event. Expand and copy are the
// only places Update touches the source directly.
func (m Model) hydrate(ev LogEvent) map[string]any {
	if ev.Raw != nil {
		return ev.Raw
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := m.src.Hydrate(ctx, ev)
	if err != nil || raw == nil {
		return ev.Fields
	}
	return raw
}

// handleMouse routes click events:
//   - Left click on a top-error row: drill the stream into that message.
//   - Right click on a log row: copy that event's full JSON to clipboard.
//   - Left click on any panel: focus it.
//
// Drag/scroll events are ignored for now (would map to log-pane scrolling).
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// Right-click on a log row: copy its raw JSON.
	if msg.Button == tea.MouseButtonRight {
		filtered := m.logs.Filtered(m.filter)
		for i := range filtered {
			z := zone.Get(fmt.Sprintf("logrow-%d", i))
			if z != nil && z.InBounds(msg) {
				_ = clipboard.WriteAll(prettyJSON(m.hydrate(filtered[i])))
				return m, toastCmd("copied event to clipboard")
			}
		}
		return m, nil
	}

	if msg.Button != tea.MouseButtonLeft {
		return m, nil
	}

	// Click on a top-error row: set search filter to its message + focus stream.
	for i, r := range m.topErrors {
		z := zone.Get(fmt.Sprintf("toperror-%d", i))
		if z != nil && z.InBounds(msg) {
			m.filter.Search = r.Message
			m.focus = FocusLogs
			m.errorCursor = i
			return m, toastCmd("filtered by selected error")
		}
	}

	// Otherwise, focus the clicked panel.
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
	// Escape works from any mode: closes modals/inputs first, then acts as a
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
		case ModeExpand, ModeHelp, ModeSourceSwitcher:
			m.mode = ModeNormal
			m.expandedLog = nil
			return m, nil
		case ModeNormal:
			// Reset to defaults: clear filters, unpause, jump to tail.
			m.filter = m.filter.Reset()
			m.paused = false
			m.scrollOff = 0
			return m, toastCmd("reset: filters cleared, tailing resumed")
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
	case ModeSourceSwitcher:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.sourceCursor > 0 {
				m.sourceCursor--
			}
		case key.Matches(msg, m.keys.Down):
			if m.sourceCursor < len(m.sources)-1 {
				m.sourceCursor++
			}
		case msg.Type == tea.KeyEnter && len(m.sources) > 0:
			next := m.sources[m.sourceCursor]
			m.mode = ModeNormal
			if next == m.name {
				return m, nil
			}
			return m.switchSource(next)
		}
		return m, nil
	}

	// Normal mode
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Refresh):
		m.queryErr = nil
		return m, tea.Batch(m.refreshAll(), toastCmd("refreshed"))

	case key.Matches(msg, m.keys.Help):
		m.mode = ModeHelp
		return m, nil

	case key.Matches(msg, m.keys.Pause):
		m.paused = !m.paused
		if m.paused {
			return m, toastCmd("paused: space to resume")
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
		switch {
		case m.focus == FocusTopErrors:
			if m.errorCursor > 0 {
				m.errorCursor--
			}
		case m.focus == FocusTopRoutes && m.agents != nil:
			// The Agents panel is read-only.
		default:
			m.scrollOff++
			m.paused = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		switch {
		case m.focus == FocusTopErrors:
			if m.errorCursor < len(m.topErrors)-1 {
				m.errorCursor++
			}
		case m.focus == FocusTopRoutes && m.agents != nil:
			// The Agents panel is read-only.
		case m.scrollOff > 0:
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
		if m.focus == FocusTopRoutes && m.agents != nil {
			return m, nil
		}
		// Expand selected log line from the filtered view. Hydrate once and
		// cache the result on the expanded copy so y can copy it.
		filtered := m.logs.Filtered(m.filter)
		if idx := m.visibleIndex(filtered); idx >= 0 && idx < len(filtered) {
			ev := filtered[idx]
			ev.Raw = m.hydrate(ev)
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
	case key.Matches(msg, m.keys.ToggleDebug):
		m.filter.HideLevels["debug"] = !m.filter.HideLevels["debug"]
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
		if m.info.Filters.Provider == "" {
			return m, nil
		}
		m.mode = ModeProvider
		m.providerInput.SetValue(m.filter.Provider)
		m.providerInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.OperationFilter):
		if m.info.Filters.Operation == "" {
			return m, nil
		}
		m.mode = ModeOperation
		m.operationInput.SetValue(m.filter.Operation)
		m.operationInput.Focus()
		return m, nil

	case key.Matches(msg, m.keys.TraceRequest):
		// Only traceable from the logs panel; other focuses have no row.
		if m.focus != FocusLogs || m.info.Filters.Trace == "" {
			return m, nil
		}
		filtered := m.logs.Filtered(m.filter)
		idx := m.visibleIndex(filtered)
		if idx < 0 || idx >= len(filtered) {
			return m, nil
		}
		label := m.info.Label(m.info.Filters.Trace)
		rid := asFieldString(filtered[idx].Fields[m.info.Filters.Trace])
		if rid == "" {
			return m, toastCmd("no " + label + " on selected row")
		}
		m.filter.Trace = rid
		short := rid
		if len(short) > 8 {
			short = short[:8]
		}
		return m, toastCmd(label + " " + short)

	case key.Matches(msg, m.keys.ClearFilter):
		m.filter = m.filter.Reset()
		return m, toastCmd("filters cleared")

	case key.Matches(msg, m.keys.TimeRange):
		m.timeRange = nextTimeRange(m.timeRange)
		return m, tea.Batch(
			m.refreshAll(),
			toastCmd(fmt.Sprintf("time range: %s", formatRange(m.timeRange))),
		)

	case key.Matches(msg, m.keys.SourceSwitcher):
		// Snap cursor to the active source for clearer UX.
		for i, n := range m.sources {
			if n == m.name {
				m.sourceCursor = i
				break
			}
		}
		m.mode = ModeSourceSwitcher
		return m, nil

	case key.Matches(msg, m.keys.SavePreset):
		slot := m.presets.NextSlot()
		if slot == "" {
			return m, toastCmd("all preset slots full (1-9): load + delete first")
		}
		preset := FilterToPreset(m.filter)
		if m.presets.Items == nil {
			m.presets.Items = map[string]Preset{}
		}
		m.presets.Items[slot] = preset
		if err := m.presets.Save(); err != nil {
			return m, toastCmd("save failed: " + err.Error())
		}
		return m, toastCmd(fmt.Sprintf("saved as preset %s: %s", slot, preset.Name))
	}

	// Number keys 1-9 load the corresponding preset (only in normal mode,
	// not when an input is focused).
	if len(msg.Runes) == 1 {
		ch := msg.Runes[0]
		if ch >= '1' && ch <= '9' {
			slot := string(ch)
			if p, ok := m.presets.Items[slot]; ok {
				m.filter = PresetToFilter(p, m.filter)
				m.scrollOff = 0
				return m, toastCmd("preset " + slot + ": " + p.Name)
			}
		}
	}
	return m, nil
}

// switchSource opens the named source, closes the old one and resets every
// piece of per-source state. A failure leaves the current source in place.
func (m Model) switchSource(next string) (tea.Model, tea.Cmd) {
	src, err := OpenSource(m.cfg, next)
	if err != nil {
		return m, toastCmd("switch failed: " + err.Error())
	}
	if m.src != nil {
		_ = m.src.Close()
	}
	m.adoptSource(next, src, src.Info())
	m.paused = false
	return m, tea.Batch(m.refreshAll(), toastCmd("source: "+next))
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
	cmds := []tea.Cmd{
		m.fetchStats(),
		m.fetchThroughput(),
		m.fetchErrorRate(),
		m.fetchTopErrors(),
		m.fetchTopRoutes(),
		m.fetchRecentIssues(),
		m.fetchClientHealth(),
		m.fetchCost(),
	}
	if m.agents != nil {
		cmds = append(cmds, m.fetchAgents())
	}
	return tea.Batch(cmds...)
}

func wrapErr(method string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", method, err)
}

// fetchCost runs alongside stats on every refresh tick, same cadence as the
// stats panel. Without CapCost the source answers with a zero report.
func (m Model) fetchCost() tea.Cmd {
	src, tr := m.src, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rep, err := src.Cost(ctx, tr)
		return costMsg{report: rep, err: wrapErr("cost", err)}
	}
}

func (m Model) fetchStats() tea.Cmd {
	src, tr := m.src, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s, err := src.Stats(ctx, tr)
		return statsMsg{stats: s, err: wrapErr("stats", err)}
	}
}

func (m Model) fetchThroughput() tea.Cmd {
	src, tr := m.src, m.timeRange
	segmented := m.info.Caps.Has(CapGroup)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if segmented {
			series, err := src.ThroughputSegmented(ctx, 5, tr)
			if err != nil {
				return throughputMsg{err: wrapErr("throughputSegmented", err)}
			}
			if len(series) > 0 {
				return throughputMsg{series: series}
			}
		}
		single, err := src.Throughput(ctx, tr)
		return throughputMsg{series: []Series{single}, err: wrapErr("throughput", err)}
	}
}

func (m Model) fetchErrorRate() tea.Cmd {
	src, tr := m.src, m.trendRange()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		series, err := src.ErrorRate(ctx, tr)
		if err != nil || len(series) == 0 {
			return errorRateMsg{series: series, err: wrapErr("errorRate", err)}
		}
		spark := make([]float64, len(series[0].Points))
		for i, p := range series[0].Points {
			spark[i] = p.Value
		}
		return errorRateMsg{series: series, spark: spark}
	}
}

func (m Model) fetchTopErrors() tea.Cmd {
	src, tr := m.src, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopErrors(ctx, 10, tr)
		return topErrorsMsg{rows: rows, err: wrapErr("topErrors", err)}
	}
}

func (m Model) fetchTopRoutes() tea.Cmd {
	src, tr := m.src, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopRoutes(ctx, 10, tr)
		return topRoutesMsg{rows: rows, err: wrapErr("topRoutes", err)}
	}
}

func (m Model) fetchRecentIssues() tea.Cmd {
	src, tr := m.src, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		events, err := src.RecentIssues(ctx, 20, tr)
		return recentIssuesMsg{events: events, err: wrapErr("recentIssues", err)}
	}
}

// Health uses a fixed 15m window, short enough that the top bar reflects
// current state, not stale incidents from earlier in the day.
func (m Model) fetchClientHealth() tea.Cmd {
	src := m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopClientHealth(ctx, 5, 15*time.Minute)
		return clientHealthMsg{rows: rows, err: wrapErr("topClientHealth", err)}
	}
}

func (m Model) fetchAgents() tea.Cmd {
	agents, tr := m.agents, m.timeRange
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := agents.Agents(ctx, tr)
		return agentsMsg{rows: rows, err: wrapErr("agents", err)}
	}
}

func (m Model) fetchLogs() tea.Cmd {
	src, cursor := m.src, m.streamCursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		events, next, err := src.Stream(ctx, cursor, streamLimit)
		if err != nil {
			return logBatchMsg{err: wrapErr("stream", err)}
		}
		return logBatchMsg{events: events, cursor: next}
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
