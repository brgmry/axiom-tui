package main

import (
	"context"
	"fmt"
	"time"

	"github.com/atotto/clipboard"
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
	logs      *LogBuffer
	filter    LogFilter
	paused    bool
	scrollOff int // 0 = tailing; N = N lines above tail

	// Aggregate state
	stats        Stats
	throughput   []Series
	errorRate    []Series
	topErrors    []TableRow
	topRoutes    []TableRow
	recentIssues []LogEvent
	errorSpark   []float64
	clientHealth []ClientHealth // per-group error+warn hot-spots
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
	expandLoading  bool     // expandedLog.Raw is being hydrated
	sources        []string // names available for the switcher modal
	sourceCursor   int      // index into sources when modal is open
	presets        Presets  // loaded from disk on startup

	// Status + errors
	width, height int
	ready         bool
	lastRefresh   time.Time
	queryErr      error
	toast         string
	toastLevel    toastLevel
	toastExpires  time.Time

	// Stream cursor: where the next Stream call continues from.
	streamCursor Cursor

	// epoch counts adopted sources. Fetch messages carry the epoch they
	// were built under; Update drops any from an earlier one.
	epoch uint64

	// settled records that the panels have been refreshed at least once
	// after the source's backfill completed. Init's refresh runs against a
	// store that is still filling, so the first stream tick that finds the
	// source idle refreshes once more instead of waiting for the next
	// refresh_seconds tick. A backfill that finishes inside the first poll
	// interval is the normal case, not the exception.
	settled bool
}

// ─── Tea.Msg types ───────────────────────────────────────────────────────────

type tickMsg time.Time
type streamTickMsg time.Time

type statsMsg struct {
	epoch uint64
	stats Stats
	err   error
}
type throughputMsg struct {
	epoch  uint64
	series []Series
	err    error
}
type errorRateMsg struct {
	epoch  uint64
	series []Series
	spark  []float64
	err    error
}
type topErrorsMsg struct {
	epoch uint64
	rows  []TableRow
	err   error
}
type topRoutesMsg struct {
	epoch uint64
	rows  []TableRow
	err   error
}
type recentIssuesMsg struct {
	epoch  uint64
	events []LogEvent
	err    error
}
type clientHealthMsg struct {
	epoch uint64
	rows  []ClientHealth
	err   error
}
type costMsg struct {
	epoch  uint64
	report CostReport
	err    error
}
type agentsMsg struct {
	epoch uint64
	rows  []AgentStat
	err   error
}
type logBatchMsg struct {
	epoch  uint64
	events []LogEvent
	cursor Cursor
	err    error
}

// toastLevel marks a toast as a confirmation or a failure; the footer
// ticks the first and crosses the second.
type toastLevel int

const (
	toastOK toastLevel = iota
	toastError
)

type toastMsg struct {
	text  string
	level toastLevel
}

// epochMsg is a result produced against one source. Every fetch command
// stamps its message with the epoch the model had when the command was
// built; Update drops a message whose epoch is not current, so a result
// from a source that was switched away never lands on the new one.
type epochMsg interface{ Epoch() uint64 }

func (m statsMsg) Epoch() uint64        { return m.epoch }
func (m throughputMsg) Epoch() uint64   { return m.epoch }
func (m errorRateMsg) Epoch() uint64    { return m.epoch }
func (m topErrorsMsg) Epoch() uint64    { return m.epoch }
func (m topRoutesMsg) Epoch() uint64    { return m.epoch }
func (m recentIssuesMsg) Epoch() uint64 { return m.epoch }
func (m clientHealthMsg) Epoch() uint64 { return m.epoch }
func (m costMsg) Epoch() uint64         { return m.epoch }
func (m agentsMsg) Epoch() uint64       { return m.epoch }
func (m logBatchMsg) Epoch() uint64     { return m.epoch }
func (m hydrateMsg) Epoch() uint64      { return m.epoch }

// hydratePurpose says what a hydrate result is for.
type hydratePurpose int

const (
	hydrateExpand hydratePurpose = iota // fill the expand modal
	hydrateCopy                         // copied to the clipboard by the command
)

// hydrateMsg is the result of a Hydrate call made off the update
// goroutine. For hydrateCopy the clipboard write has already happened in
// the command: the fields were copied when err is set, and copyErr is the
// clipboard failure.
type hydrateMsg struct {
	epoch   uint64
	ev      LogEvent
	raw     map[string]any
	err     error
	copyErr error
	purpose hydratePurpose
}

// toastLifetime is how long a toast stays in the footer.
const toastLifetime = 3 * time.Second

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

	// A corrupt presets file must not silently drop every preset: start
	// empty, but tell the user why.
	presets, presetsErr := LoadPresets()
	if presets.Items == nil {
		presets.Items = map[string]Preset{}
	}

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
	if presetsErr != nil {
		m.setToast("presets: "+presetsErr.Error(), toastError)
	}
	return m
}

// adoptSource points the model at a freshly opened source: capability
// lookup, prompt labels, startup filter and an empty stream.
func (m *Model) adoptSource(name string, src Source, info SourceInfo) {
	m.epoch++
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
	m.settled = false

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
		tea.Tick(time.Duration(m.cfg.RefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
			return tickMsg(t)
		}),
	)
}

// ─── Update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if em, ok := msg.(epochMsg); ok && em.Epoch() != m.epoch {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		for _, ti := range textInputs {
			ti.input(&m).Width = msg.Width / 3
		}
		return m, nil

	case tickMsg:
		// Periodic aggregate refresh. A stale error from the previous cycle
		// is dropped; any method that still fails sets it again.
		m.queryErr = nil
		return m, tea.Batch(
			m.refreshAll(),
			tea.Tick(time.Duration(m.cfg.RefreshSeconds)*time.Second, func(t time.Time) tea.Msg {
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
		// on the first tick that finds the backfill complete, whether or
		// not a tick ever saw it running.
		if ps, ok := m.src.(ProgressSource); ok {
			loading := ps.Progress().Loading
			if loading || !m.settled {
				cmds = append(cmds, m.refreshAll())
			}
			m.settled = !loading
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

	case hydrateMsg:
		return m.applyHydrate(msg)

	case toastMsg:
		m.setToast(msg.text, msg.level)
		return m, nil

	case tea.KeyMsg:
		// Filters and new events move the list under the stored offset;
		// bring it back in range before the key acts on it.
		m.scrollOff = m.scrollPos(m.logs.Filtered(m.filter))
		return m.handleKey(msg)

	case tea.MouseMsg:
		m.scrollOff = m.scrollPos(m.logs.Filtered(m.filter))
		return m.handleMouse(msg)
	}
	return m, nil
}

// hydrateCmd fetches the full raw object for an event off the update
// goroutine. Expand fills the modal when the message lands; copy writes
// the clipboard in the command and reports through the message.
func (m Model) hydrateCmd(ev LogEvent, purpose hydratePurpose) tea.Cmd {
	src, ep := m.src, m.epoch
	return func() tea.Msg {
		raw, err := ev.Raw, error(nil)
		if raw == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			raw, err = src.Hydrate(ctx, ev)
		}
		msg := hydrateMsg{epoch: ep, ev: ev, raw: raw, err: err, purpose: purpose}
		if purpose == hydrateCopy {
			body := raw
			if err != nil || body == nil {
				body = ev.Fields
			}
			msg.copyErr = writeClipboard(prettyJSON(body))
		}
		return msg
	}
}

// applyHydrate lands a hydrate result: the expand modal gets the full
// object, or the event's fields when hydrate failed; a copy becomes a
// toast.
func (m Model) applyHydrate(msg hydrateMsg) (tea.Model, tea.Cmd) {
	if msg.purpose == hydrateCopy {
		switch {
		case msg.copyErr != nil:
			m.setToast("copy failed: "+msg.copyErr.Error(), toastError)
		case msg.err != nil:
			m.setToast("copied fields only, hydrate failed: "+msg.err.Error(), toastError)
		default:
			m.setToast("copied event to clipboard", toastOK)
		}
		return m, nil
	}
	ex := m.expandedLog
	if m.mode != ModeExpand || !m.expandLoading || ex == nil || ex.ID != msg.ev.ID || !ex.Time.Equal(msg.ev.Time) {
		return m, nil // the modal was closed or moved on
	}
	ev := *ex
	m.expandLoading = false
	if msg.err != nil || msg.raw == nil {
		ev.Raw = ev.Fields
		m.expandedLog = &ev
		if msg.err != nil {
			m.setToast("hydrate failed: "+msg.err.Error(), toastError)
		}
		return m, nil
	}
	ev.Raw = msg.raw
	m.expandedLog = &ev
	return m, nil
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
				return m, m.hydrateCmd(filtered[i], hydrateCopy)
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

// switchSource opens the named source, closes the old one and resets every
// piece of per-source state. A failure leaves the current source in place.
func (m Model) switchSource(next string) (tea.Model, tea.Cmd) {
	src, err := OpenSource(m.cfg, next)
	if err != nil {
		return m, errorToastCmd("switch failed: " + err.Error())
	}
	if m.src != nil {
		_ = m.src.Close()
	}
	m.adoptSource(next, src, src.Info())
	m.paused = false
	return m, tea.Batch(m.refreshAll(), toastCmd("source: "+next))
}

// scrollBy moves the scroll offset by delta lines (positive is up, away
// from the tail), clamped to the filtered list so the panel always shows a
// full page and never runs past the oldest event.
func (m *Model) scrollBy(delta int) {
	top := m.maxScroll(len(m.logs.Filtered(m.filter)))
	m.scrollOff = clampInt(m.scrollOff+delta, 0, top)
}

// visibleIndex returns the index into `filtered` that corresponds to the
// user's scroll position. When tailing (scrollOff=0) we point at the last
// line; when scrolled up, we point `scrollOff` lines above the tail, using
// the same clamped offset the view draws with.
func (m Model) visibleIndex(filtered []LogEvent) int {
	if len(filtered) == 0 {
		return -1
	}
	return len(filtered) - 1 - m.scrollPos(filtered)
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
	src, tr, ep := m.src, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rep, err := src.Cost(ctx, tr)
		return costMsg{epoch: ep, report: rep, err: wrapErr("cost", err)}
	}
}

func (m Model) fetchStats() tea.Cmd {
	src, tr, ep := m.src, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s, err := src.Stats(ctx, tr)
		return statsMsg{epoch: ep, stats: s, err: wrapErr("stats", err)}
	}
}

func (m Model) fetchThroughput() tea.Cmd {
	src, tr, ep := m.src, m.timeRange, m.epoch
	segmented := m.info.Caps.Has(CapGroup)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if segmented {
			series, err := src.ThroughputSegmented(ctx, 5, tr)
			if err != nil {
				return throughputMsg{epoch: ep, err: wrapErr("throughputSegmented", err)}
			}
			if len(series) > 0 {
				return throughputMsg{epoch: ep, series: series}
			}
		}
		single, err := src.Throughput(ctx, tr)
		return throughputMsg{epoch: ep, series: []Series{single}, err: wrapErr("throughput", err)}
	}
}

func (m Model) fetchErrorRate() tea.Cmd {
	src, tr, ep := m.src, m.trendRange(), m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		series, err := src.ErrorRate(ctx, tr)
		if err != nil || len(series) == 0 {
			return errorRateMsg{epoch: ep, series: series, err: wrapErr("errorRate", err)}
		}
		spark := make([]float64, len(series[0].Points))
		for i, p := range series[0].Points {
			spark[i] = p.Value
		}
		return errorRateMsg{epoch: ep, series: series, spark: spark}
	}
}

func (m Model) fetchTopErrors() tea.Cmd {
	src, tr, ep := m.src, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopErrors(ctx, 10, tr)
		return topErrorsMsg{epoch: ep, rows: rows, err: wrapErr("topErrors", err)}
	}
}

func (m Model) fetchTopRoutes() tea.Cmd {
	src, tr, ep := m.src, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopRoutes(ctx, 10, tr)
		return topRoutesMsg{epoch: ep, rows: rows, err: wrapErr("topRoutes", err)}
	}
}

func (m Model) fetchRecentIssues() tea.Cmd {
	src, tr, ep := m.src, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		events, err := src.RecentIssues(ctx, 20, tr)
		return recentIssuesMsg{epoch: ep, events: events, err: wrapErr("recentIssues", err)}
	}
}

// Health uses a fixed 15m window, short enough that the top bar reflects
// current state, not stale incidents from earlier in the day.
func (m Model) fetchClientHealth() tea.Cmd {
	src, ep := m.src, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := src.TopClientHealth(ctx, 5, 15*time.Minute)
		return clientHealthMsg{epoch: ep, rows: rows, err: wrapErr("topClientHealth", err)}
	}
}

func (m Model) fetchAgents() tea.Cmd {
	agents, tr, ep := m.agents, m.timeRange, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rows, err := agents.Agents(ctx, tr)
		return agentsMsg{epoch: ep, rows: rows, err: wrapErr("agents", err)}
	}
}

func (m Model) fetchLogs() tea.Cmd {
	src, cursor, ep := m.src, m.streamCursor, m.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		events, next, err := src.Stream(ctx, cursor, streamLimit)
		if err != nil {
			return logBatchMsg{epoch: ep, err: wrapErr("stream", err)}
		}
		return logBatchMsg{epoch: ep, events: events, cursor: next}
	}
}

func (m Model) streamTick() tea.Cmd {
	d := time.Duration(m.cfg.StreamPollMs) * time.Millisecond
	return tea.Tick(d, func(t time.Time) tea.Msg { return streamTickMsg(t) })
}

// writeClipboard is the system clipboard write; tests replace it.
var writeClipboard = clipboard.WriteAll

// toastCmd shows a confirmation in the footer.
func toastCmd(s string) tea.Cmd {
	return func() tea.Msg { return toastMsg{s, toastOK} }
}

// errorToastCmd shows a failure in the footer.
func errorToastCmd(s string) tea.Cmd {
	return func() tea.Msg { return toastMsg{s, toastError} }
}

// setToast shows s in the footer for toastLifetime.
func (m *Model) setToast(s string, level toastLevel) {
	m.toast = s
	m.toastLevel = level
	m.toastExpires = time.Now().Add(toastLifetime)
}
