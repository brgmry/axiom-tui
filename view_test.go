package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	// go test has no TTY, so lipgloss would drop every escape sequence and
	// the ANSI-aware paths would never be exercised. Render as a real
	// terminal does.
	lipgloss.SetColorProfile(termenv.TrueColor)
	zone.NewGlobal()
	code := m.Run()
	zone.Close()
	cleanupBenchFixtures()
	os.Exit(code)
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// fakeSource is an in-memory Source for view tests. Every capability is on
// unless the test narrows info.Caps.
type fakeSource struct {
	info   SourceInfo
	agents bool
	closed int
}

func fullInfo() SourceInfo {
	return SourceInfo{
		Name:              "fake",
		Kind:              "jsonl",
		Caps:              CapGroup | CapCost | CapDuration | CapRoutes,
		GroupLabel:        "client",
		InterestingFields: []string{"route"},
		Filters:           FilterFields{Provider: "provider", Operation: "operation", Trace: "requestId"},
	}
}

func (f *fakeSource) Info() SourceInfo { return f.info }
func (f *fakeSource) Stats(ctx context.Context, lookback time.Duration) (Stats, error) {
	s := Stats{Total: 120, Info: 100, Warn: 15, Error: 5}
	if f.info.Caps.Has(CapDuration) {
		s.Durations = []DurationStat{{Field: "durationMs", Avg: 12, P95: 40, Max: 200}}
	}
	return s, nil
}
func (f *fakeSource) Throughput(ctx context.Context, lookback time.Duration) (Series, error) {
	return Series{Name: "Req/min", Points: fakePoints(lookback)}, nil
}
func (f *fakeSource) ThroughputSegmented(ctx context.Context, topN int, lookback time.Duration) ([]Series, error) {
	if !f.info.Caps.Has(CapGroup) {
		return nil, nil
	}
	return []Series{{Name: "acme", Points: fakePoints(lookback)}, {Name: "beta", Points: fakePoints(lookback)}}, nil
}
func (f *fakeSource) ErrorRate(ctx context.Context, lookback time.Duration) ([]Series, error) {
	return []Series{{Name: "Errors", Points: fakePoints(lookback)}, {Name: "Warns", Points: fakePoints(lookback)}}, nil
}
func (f *fakeSource) TopErrors(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	return []TableRow{{Count: 7, Level: "error", Message: "boom"}}, nil
}
func (f *fakeSource) TopRoutes(ctx context.Context, n int, lookback time.Duration) ([]TableRow, error) {
	if !f.info.Caps.Has(CapRoutes) {
		return nil, nil
	}
	return []TableRow{{Count: 3, Message: "GET /health"}}, nil
}
func (f *fakeSource) RecentIssues(ctx context.Context, n int, lookback time.Duration) ([]LogEvent, error) {
	return []LogEvent{{Time: time.Now(), Level: "warn", Message: "slow"}}, nil
}
func (f *fakeSource) TopClientHealth(ctx context.Context, n int, lookback time.Duration) ([]ClientHealth, error) {
	if !f.info.Caps.Has(CapGroup) {
		return nil, nil
	}
	return []ClientHealth{{Client: "acme", Errors: 2, Warns: 1}}, nil
}
func (f *fakeSource) Cost(ctx context.Context, lookback time.Duration) (CostReport, error) {
	if !f.info.Caps.Has(CapCost) {
		return CostReport{}, nil
	}
	rep := CostReport{ByGroup: []ClientCost{{Client: "acme", Dollars: 19.4}}, Total: 19.4}
	if f.agents {
		rep.HasSummary = true
		rep.Today = 19.4
		rep.TrailingHour = 1.12
		rep.ByModel = []ClientCost{{Client: "claude-opus-5-5", Dollars: 18.2}, {Client: "claude-sonnet-5-5", Dollars: 0.9}}
	}
	return rep, nil
}
func (f *fakeSource) Stream(ctx context.Context, cur Cursor, limit int) ([]LogEvent, Cursor, error) {
	ev := LogEvent{ID: "1:1", Time: time.Now(), Level: "info", Message: "hello", Client: "acme",
		Fields: map[string]any{"route": "/x", "provider": "p"}}
	return []LogEvent{ev}, Cursor{Seq: 2}, nil
}
func (f *fakeSource) Hydrate(ctx context.Context, ev LogEvent) (map[string]any, error) {
	return map[string]any{"message": ev.Message, "full": true}, nil
}
func (f *fakeSource) Close() error { f.closed++; return nil }

// fakeAgentSource adds the AgentSource interface. When list is set it is
// returned as is; otherwise one representative agent.
type fakeAgentSource struct {
	fakeSource
	list []AgentStat
}

func (f *fakeAgentSource) Agents(ctx context.Context, lookback time.Duration) ([]AgentStat, error) {
	if f.list != nil {
		return f.list, nil
	}
	return []AgentStat{{
		Name: "pi-os", LastSeen: time.Now(), SpendToday: 12.1, SpendHour: 1.1,
		ContextTokens: 68224, Compactions: 2, ToolCalls: 120, ToolErrors: 3,
		TopModel: "claude-opus-5-5", FallbackN: 1, FallbackD: 4,
	}}, nil
}

func fakePoints(lookback time.Duration) []ChartPoint {
	now := time.Now()
	pts := make([]ChartPoint, 0, 10)
	for i := 0; i < 10; i++ {
		pts = append(pts, ChartPoint{Time: now.Add(-lookback + lookback*time.Duration(i)/10), Value: float64(i % 4)})
	}
	return pts
}

func newTestModel(t *testing.T, src Source) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := Defaults()
	if err := cfg.Normalise(); err != nil {
		t.Fatal(err)
	}
	return NewModel(cfg, src.Info().Name, src)
}

// loadAll runs every fetch command synchronously and feeds the messages
// back through Update so the view has data.
func loadAll(t *testing.T, m Model) Model {
	t.Helper()
	cmds := []func() any{
		func() any { return m.fetchStats()() },
		func() any { return m.fetchThroughput()() },
		func() any { return m.fetchErrorRate()() },
		func() any { return m.fetchTopErrors()() },
		func() any { return m.fetchTopRoutes()() },
		func() any { return m.fetchRecentIssues()() },
		func() any { return m.fetchClientHealth()() },
		func() any { return m.fetchCost()() },
		func() any { return m.fetchLogs()() },
	}
	if m.agents != nil {
		cmds = append(cmds, func() any { return m.fetchAgents()() })
	}
	for _, c := range cmds {
		next, _ := m.Update(c())
		m = next.(Model)
	}
	return m
}

func TestViewSmoke(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		for _, tr := range timeRangeOptions {
			m := newTestModel(t, src)
			m.width, m.height, m.ready = size[0], size[1], true
			m.timeRange = tr
			m = loadAll(t, m)
			out := stripANSI(m.View())
			if tr == 15*time.Minute {
				if !strings.Contains(out, "Last 15m") {
					t.Errorf("%v at %v: stats title should read Last 15m", size, tr)
				}
				if !strings.Contains(out, "Top Routes (15m)") {
					t.Errorf("%v at %v: Top Routes title should carry the window", size, tr)
				}
				if !strings.Contains(out, "Errors & Warns (6h)") {
					t.Errorf("%v at %v: error trend should carry (6h)", size, tr)
				}
			}
			if !strings.Contains(out, "120 events") {
				t.Errorf("%v at %v: stats panel should show the total", size, tr)
			}
			if !strings.Contains(out, "INFO") {
				t.Errorf("%v at %v: stats panel should show the level rows", size, tr)
			}
			if size == [2]int{80, 24} {
				// Too short for line charts: each panel is a one-line
				// summary, never an empty frame.
				if l := firstLine(out, "Throughput"); !strings.Contains(l, "120 events") || !strings.Contains(l, "ev/min") {
					t.Errorf("%v at %v: throughput should summarise, got %q", size, tr, l)
				}
				if l := firstLine(out, "Errors & Warns ("); !strings.Contains(l, "Errors 13") || !strings.Contains(l, "Warns 13") {
					t.Errorf("%v at %v: error trend should summarise, got %q", size, tr, l)
				}
			}
			if !strings.Contains(out, "JSONL Live [fake]") {
				t.Errorf("stream title should name the kind and source: %q", firstLine(out, "Live"))
			}
			if !strings.Contains(out, "source: fake") {
				t.Error("footer should show the source name")
			}
			// Overlays render too.
			for _, mode := range []Mode{ModeHelp, ModeSourceSwitcher} {
				m.mode = mode
				_ = m.View()
			}
			m.mode = ModeNormal
			next, cmd := m.handleKey(keyPress("enter"))
			m = next.(Model)
			if cmd == nil {
				t.Fatal("enter should hydrate in a command")
			}
			next, _ = m.Update(cmd())
			m = next.(Model)
			if m.mode != ModeExpand || m.expandedLog == nil || m.expandedLog.Raw["full"] != true {
				t.Error("enter should expand the tail row with the hydrated object")
			}
			_ = m.View()
		}
	}
}

func TestViewCaps(t *testing.T) {
	info := fullInfo()
	info.Caps = 0
	src := &fakeSource{info: info}
	m := newTestModel(t, src)
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	out := stripANSI(m.View())
	if strings.Contains(out, "Cost (") {
		t.Error("Cost panel should not be drawn without CapCost")
	}
	if strings.Contains(out, "health (15m)") {
		t.Error("health row should not be drawn without CapGroup")
	}
	if !strings.Contains(out, "no route_prefixes configured") {
		t.Error("Top Routes should explain the missing prefixes")
	}
	if !strings.Contains(out, "Last 1h") {
		t.Error("stats panel should still render")
	}
}

func TestViewAgents(t *testing.T) {
	src := &fakeAgentSource{fakeSource: fakeSource{info: fullInfo(), agents: true}}
	src.info.Kind = "pi"
	src.info.GroupLabel = "agent"
	m := newTestModel(t, src)
	if m.agents == nil {
		t.Fatal("model should detect AgentSource")
	}
	m.width, m.height, m.ready = 140, 40, true
	m = loadAll(t, m)
	out := stripANSI(m.View())
	for _, want := range []string{"Agents", "pi-os", "opus-5-5", "1/4", "today $19.40", "by model:", "agent health (15m)"} {
		if !strings.Contains(out, want) {
			t.Errorf("agents view missing %q", want)
		}
	}
	for _, absent := range []string{"Top Routes", "Recent Issues"} {
		if strings.Contains(out, absent) {
			t.Errorf("agents view should not draw %q", absent)
		}
	}
	m.focus = FocusTopRoutes
	if m.focusName(m.focus) != "agents" {
		t.Error("bottom-right focus should be named agents")
	}
}

func TestSwitchSourceClosesOld(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	m := newTestModel(t, src)
	sourceOpeners["fake"] = func(name string, sc SourceConfig) (Source, error) {
		return &fakeSource{info: SourceInfo{Name: name, Kind: "fake"}}, nil
	}
	defer delete(sourceOpeners, "fake")
	m.cfg.Sources["other"] = SourceConfig{Type: "fake"}
	next, _ := m.switchSource("other")
	m = next.(Model)
	if src.closed != 1 {
		t.Errorf("old source should be closed once, got %d", src.closed)
	}
	if m.name != "other" || m.info.Kind != "fake" {
		t.Errorf("switch did not adopt the new source: %s %s", m.name, m.info.Kind)
	}
	if !m.streamCursor.IsZero() {
		t.Error("stream cursor should reset on switch")
	}
}

func firstLine(s, needle string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

// rowsOf counts the rendered rows of a frame; lipgloss never emits a
// trailing newline, so this is the number of terminal rows used.
func rowsOf(s string) int { return strings.Count(s, "\n") + 1 }

// TestViewFitsTerminal asserts the frame never exceeds the terminal: a
// taller frame scrolls the header and the stream title off the top. Both
// layouts are tried with the busiest content the panels can be handed.
func TestViewFitsTerminal(t *testing.T) {
	manyAgents := make([]AgentStat, 0, 9)
	for i := 0; i < 9; i++ {
		manyAgents = append(manyAgents, AgentStat{Name: fmt.Sprintf("agent-%d", i), LastSeen: time.Now(), SpendToday: float64(i)})
	}
	busyCost := CostReport{Total: 42, HasSummary: true, Today: 42, TrailingHour: 3,
		ByModel: []ClientCost{{Client: "claude-opus-5-5", Dollars: 30}, {Client: "claude-sonnet-5-5", Dollars: 12}}}
	for i := 0; i < 7; i++ {
		busyCost.ByGroup = append(busyCost.ByGroup, ClientCost{Client: fmt.Sprintf("group-%d", i), Dollars: float64(7 - i)})
	}
	twoDurations := []DurationStat{{Field: "llm", Avg: 1, P95: 2, Max: 3}, {Field: "tool", Avg: 1, P95: 2, Max: 3}}

	layouts := []struct {
		name string
		src  Source
	}{
		{"jsonl", &fakeSource{info: fullInfo()}},
		{"pi", func() Source {
			s := &fakeAgentSource{fakeSource: fakeSource{info: fullInfo(), agents: true}}
			s.info.Kind = "pi"
			return s
		}()},
	}
	for _, lay := range layouts {
		for _, size := range [][2]int{{140, 40}, {80, 24}} {
			m := newTestModel(t, lay.src)
			m.width, m.height, m.ready = size[0], size[1], true
			m = loadAll(t, m)
			if m.agents != nil {
				m.agentStats = manyAgents
				m.cost = busyCost
				m.stats.Durations = twoDurations
			}
			for _, mode := range []Mode{ModeNormal, ModeSearch} {
				m.mode = mode
				out := stripANSI(m.View())
				if rows := rowsOf(out); rows > size[1] {
					t.Errorf("%s at %dx%d mode %d: %d rows rendered, terminal has %d", lay.name, size[0], size[1], mode, rows, size[1])
				}
				for i, line := range strings.Split(out, "\n") {
					if w := displayWidth(line); w > size[0] {
						t.Errorf("%s at %dx%d: row %d is %d cells wide", lay.name, size[0], size[1], i, w)
					}
				}
				if !strings.Contains(out, "Live [") {
					t.Errorf("%s at %dx%d: stream title missing", lay.name, size[0], size[1])
				}
			}
		}
	}
}

// TestRightStackBudget: the stats row gets its rows first, the charts share
// the rest, the stack always fills its height, and no size panics or
// leaves a chart panel as an empty frame.
func TestRightStackBudget(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	m := newTestModel(t, src)
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	for h := 0; h <= 40; h++ {
		statsH, throughH, errH := m.rightStackRows(h)
		if statsH+throughH+errH != h {
			t.Errorf("height %d: rows %d+%d+%d do not add up", h, statsH, throughH, errH)
		}
		if errH > 0 && throughH == 0 {
			t.Errorf("height %d: error trend kept while throughput was dropped", h)
		}
		if h >= m.statsRows(false)+summaryPanelRows && statsH < m.statsRows(false) {
			t.Errorf("height %d: stats got %d rows, needs %d", h, statsH, m.statsRows(false))
		}
	}
	for _, w := range []int{40, 60, 80, 100, 160} {
		for h := 6; h <= 40; h += 3 {
			m.width, m.height = w, h
			out := stripANSI(m.View())
			if rows := rowsOf(out); rows > h {
				t.Errorf("%dx%d: %d rows", w, h, rows)
			}
		}
	}
}

// TestFooterFits: the footer was one long line left to lipgloss to wrap.
// Each row must fit the terminal, hints giving way before the status.
func TestFooterFits(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	m := newTestModel(t, src)
	m.width, m.height, m.ready = 80, 24, true
	m = loadAll(t, m)
	for _, busy := range []bool{false, true} {
		if busy {
			m.queryErr = fmt.Errorf("stats: upstream timed out after 15s while reading the dataset")
			m.toast = "copied event to clipboard"
			m.toastExpires = time.Now().Add(time.Minute)
		}
		for f := Focus(0); f < numFocuses; f++ {
			m.focus = f
			footer := m.renderFooter()
			rows := strings.Split(footer, "\n")
			if len(rows) > 2 {
				t.Errorf("busy=%v focus %s: footer is %d rows", busy, m.focusName(f), len(rows))
			}
			for i, r := range rows {
				if w := displayWidth(r); w > m.width {
					t.Errorf("busy=%v focus %s: row %d is %d cells wide", busy, m.focusName(f), i, w)
				}
			}
			plain := stripANSI(footer)
			if !strings.Contains(plain, "source: fake") {
				t.Errorf("busy=%v focus %s: footer lost the source:\n%s", busy, m.focusName(f), plain)
			}
			// Hints are the first to give way, so only a quiet footer
			// is sure to keep them.
			if !busy && !strings.Contains(plain, "? help") {
				t.Errorf("focus %s: footer lost the help hint:\n%s", m.focusName(f), plain)
			}
			if busy && !strings.Contains(plain, "⚠ stats") {
				t.Errorf("focus %s: footer lost the query error:\n%s", m.focusName(f), plain)
			}
		}
	}
}

// TestFooterSparkErrors: the footer sparkline draws the Errors series, not
// throughput.
func TestFooterSparkErrors(t *testing.T) {
	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 160, 40, true
	m = loadAll(t, m)
	m.throughput = nil
	m.errorSpark = []float64{0, 4, 0, 4}
	footer := stripANSI(m.renderFooter())
	if !strings.Contains(footer, "errs") || strings.Contains(footer, "ev/min") {
		t.Errorf("footer should label the sparkline errs:\n%s", footer)
	}
	if !strings.Contains(footer, "█") {
		t.Errorf("footer should draw the errors sparkline with throughput cleared:\n%s", footer)
	}
}

// TestStatsCopy: counts pluralise and a quiet stream's rate keeps a
// decimal instead of reading ~0/min.
func TestStatsCopy(t *testing.T) {
	for _, tc := range []struct {
		total int64
		tr    time.Duration
		want  string
	}{
		{1, 15 * time.Minute, "1 event "},
		{2, 15 * time.Minute, "2 events "},
		{7, 15 * time.Minute, "(~0.5/min)"},
		{1200, 60 * time.Minute, "(~20/min)"},
	} {
		src := &fakeSource{info: fullInfo()}
		m := newTestModel(t, src)
		m.width, m.height, m.ready = 160, 40, true
		m.timeRange = tc.tr
		m.stats = Stats{Total: tc.total, Info: tc.total}
		if out := stripANSI(m.renderStats(60, 8)); !strings.Contains(out, tc.want) {
			t.Errorf("total %d over %v: want %q in\n%s", tc.total, tc.tr, tc.want, out)
		}
	}
	for r, want := range map[float64]string{0: "~0.0", 0.46: "~0.5", 9.94: "~9.9", 10: "~10", 123.7: "~123"} {
		if got := formatRate(r); got != want {
			t.Errorf("formatRate(%v) = %q, want %q", r, got, want)
		}
	}
}

// TestViewAgentsRowBudget: the bottom row must have room for every agent
// up to the panel's maximum height. The panel spends four rows on borders,
// title and column header, so a budget of agents+3 lost the last agent.
func TestViewAgentsRowBudget(t *testing.T) {
	for _, n := range []int{3, 5, 8} {
		src := &fakeAgentSource{fakeSource: fakeSource{info: fullInfo(), agents: true}}
		for i := 0; i < n; i++ {
			src.list = append(src.list, AgentStat{Name: fmt.Sprintf("agent-%02d", i), LastSeen: time.Now(), SpendToday: float64(n - i)})
		}
		m := newTestModel(t, src)
		m.width, m.height, m.ready = 140, 40, true
		m = loadAll(t, m)
		out := stripANSI(m.View())
		for i := 0; i < n; i++ {
			if name := fmt.Sprintf("agent-%02d", i); !strings.Contains(out, name) {
				t.Errorf("%d agents: panel dropped %s", n, name)
			}
		}
		if got := rowsOf(out); got != 40 {
			t.Errorf("%d agents: frame is %d rows, terminal is 40", n, got)
		}
	}

	// Beyond the maximum height the overflow is stated, not silent.
	src := &fakeAgentSource{fakeSource: fakeSource{info: fullInfo(), agents: true}}
	for i := 0; i < 9; i++ {
		src.list = append(src.list, AgentStat{Name: fmt.Sprintf("agent-%02d", i), LastSeen: time.Now(), SpendToday: float64(9 - i)})
	}
	m := newTestModel(t, src)
	m.width, m.height, m.ready = 140, 40, true
	m = loadAll(t, m)
	out := stripANSI(m.View())
	if !strings.Contains(out, "2 more") {
		t.Errorf("9 agents: expected an overflow line naming 2 more agents")
	}
	if strings.Contains(out, "agent-08") || strings.Contains(out, "agent-07") {
		t.Errorf("9 agents: the overflow line should replace the last rows, not add to them")
	}
	if got := rowsOf(out); got != 40 {
		t.Errorf("9 agents: frame is %d rows, terminal is 40", got)
	}
}

// TestViewOneRowPerEvent: pi tool results and assistant text carry
// newlines. Every one-row renderer must flatten them, or a single event
// takes two rows, the panel overruns its budget and placeBox clips the
// newest row off the bottom.
func TestViewOneRowPerEvent(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	m := newTestModel(t, src)
	// Wide enough that no message is cut by the column width, so a missing
	// second half can only mean it went to another row.
	m.width, m.height, m.ready = 220, 40, true
	m = loadAll(t, m)

	m.logs.Append([]LogEvent{{
		ID: "1:9", Time: time.Now(), Level: "info", Message: "bash ok: ---\nname: streamhead", Client: "acme",
		Fields: map[string]any{"route": "GET\n/multi"},
	}})
	m.topErrors = []TableRow{{Count: 3, Level: "error", Message: "bash error: ---\npublish: topfail"}}
	m.recentIssues = []LogEvent{{Time: time.Now(), Level: "warn", Message: "slow\nquery: recentpair"}}
	m.topRoutes = []TableRow{{Count: 2, Message: "POST /a\n/routepair"}}

	out := stripANSI(m.View())
	if got := rowsOf(out); got > 40 {
		t.Errorf("frame is %d rows, terminal is 40", got)
	}
	for _, pair := range [][2]string{
		{"bash ok:", "name: streamhead"},
		{"GET", "/multi"},
		{"bash error:", "publish: topfail"},
		{"slow", "query: recentpair"},
		{"POST /a", "/routepair"},
	} {
		line := firstLine(out, pair[1])
		if line == "" {
			t.Errorf("%q was not rendered", pair[1])
			continue
		}
		if !strings.Contains(line, pair[0]) {
			t.Errorf("%q and %q should share one row, got %q", pair[0], pair[1], line)
		}
	}
}

// TestHighlightNeedleUnicode: the needle was found in a lowered copy of the
// string and the original was sliced with those offsets. Lowering can change
// byte lengths, which panicked on U+023A and mangled a dotted capital I.
func TestHighlightNeedleUnicode(t *testing.T) {
	for _, tc := range []struct {
		s, needle, hit string
	}{
		{"Ⱥ boom", "boom", "boom"},
		{"Kİz kiz", "kiz", "Kİz"},
		{"Request FAILED again", "failed", "FAILED"},
	} {
		out := highlightNeedle(tc.s, tc.needle)
		if got := stripANSI(out); got != tc.s {
			t.Errorf("%q: text changed to %q", tc.s, got)
		}
		if !strings.Contains(out, needleStyle.Render(tc.hit)) {
			t.Errorf("%q: %q should be highlighted as written, got %q", tc.s, tc.hit, out)
		}
	}
}

// TestScrollClampedToFilter: scrolling up then pinning a trace left
// scrollOff above the filtered length, so the panel reported no matches
// although the pinned row matched. The scroll offset must follow the
// filtered list, and the selected row must be the one shown.
func TestScrollClampedToFilter(t *testing.T) {
	src := &fakeSource{info: fullInfo()}
	m := newTestModel(t, src)
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	now := time.Now()
	m.logs.Append([]LogEvent{
		{ID: "1:2", Time: now, Level: "info", Message: "alpha", Fields: map[string]any{"requestId": "A"}},
		{ID: "1:3", Time: now, Level: "info", Message: "beta", Fields: map[string]any{"requestId": "A"}},
		{ID: "1:4", Time: now, Level: "info", Message: "gamma", Fields: map[string]any{"requestId": "B"}},
	})
	m.scrollOff = 2 // two k presses: selection on alpha
	m.focus = FocusLogs
	m.filter.Trace = "B"

	out := stripANSI(m.View())
	if strings.Contains(out, "no logs match") {
		t.Error("panel reports no matches although gamma matches the pinned trace")
	}
	if !strings.Contains(out, "gamma") {
		t.Error("the pinned row should be visible")
	}
	filtered := m.logs.Filtered(m.filter)
	if idx := m.visibleIndex(filtered); idx != 0 {
		t.Errorf("visibleIndex should point at the only row, got %d", idx)
	}

	// A key press re-clamps the stored offset before acting, so j from the
	// stale offset lands on the tail instead of two rows above it.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(Model)
	if m.scrollOff != 0 {
		t.Errorf("scrollOff should be clamped to the filtered page, got %d", m.scrollOff)
	}
}
