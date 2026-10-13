package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	zone "github.com/lrstanley/bubblezone"
)

func TestMain(m *testing.M) {
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

// fakeAgentSource adds the AgentSource interface.
type fakeAgentSource struct{ fakeSource }

func (f *fakeAgentSource) Agents(ctx context.Context, lookback time.Duration) ([]AgentStat, error) {
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
	cmds := []func() interface{}{}
	for _, c := range []func() interface{}{
		func() interface{} { return m.fetchStats()() },
		func() interface{} { return m.fetchThroughput()() },
		func() interface{} { return m.fetchErrorRate()() },
		func() interface{} { return m.fetchTopErrors()() },
		func() interface{} { return m.fetchTopRoutes()() },
		func() interface{} { return m.fetchRecentIssues()() },
		func() interface{} { return m.fetchClientHealth()() },
		func() interface{} { return m.fetchCost()() },
		func() interface{} { return m.fetchLogs()() },
	} {
		cmds = append(cmds, c)
	}
	if m.agents != nil {
		cmds = append(cmds, func() interface{} { return m.fetchAgents()() })
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
			next, _ := m.handleKey(keyPress("enter"))
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
	src := &fakeAgentSource{fakeSource{info: fullInfo(), agents: true}}
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
