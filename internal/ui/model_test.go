package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atotto/clipboard"
	"github.com/brgmry/tailboard/internal/source"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeProgressSource is a fakeSource that reports backfill progress and
// counts aggregate queries.
type fakeProgressSource struct {
	fakeSource
	loading bool
	stats   int
}

func (f *fakeProgressSource) Progress() source.Progress { return source.Progress{Loading: f.loading} }
func (f *fakeProgressSource) Stats(ctx context.Context, lookback time.Duration) (source.Stats, error) {
	f.stats++
	return f.fakeSource.Stats(ctx, lookback)
}

// runCmds executes a command tree synchronously, skipping nothing: the
// model's tick commands fire after StreamPollMs, which the test sets low.
func runCmds(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runCmds(c)
		}
	}
}

// TestRefreshWhileLoading: a backfilling source gets its panels refreshed
// on every stream tick, then once more after loading completes, then only
// on the refresh tick.
func TestRefreshWhileLoading(t *testing.T) {
	src := &fakeProgressSource{fakeSource: fakeSource{info: fullInfo()}, loading: true}
	m := newTestModel(t, src)
	m.cfg.StreamPollMs = 1

	tick := func(loading bool) int {
		src.loading = loading
		before := src.stats
		next, cmd := m.Update(streamTickMsg(time.Now()))
		m = next.(Model)
		runCmds(cmd)
		return src.stats - before
	}
	if n := tick(true); n != 1 {
		t.Errorf("loading tick should refresh once, got %d stats calls", n)
	}
	if n := tick(true); n != 1 {
		t.Errorf("second loading tick should refresh again, got %d", n)
	}
	if n := tick(false); n != 1 {
		t.Errorf("first tick after loading should refresh once more, got %d", n)
	}
	if n := tick(false); n != 0 {
		t.Errorf("steady state ticks should not refresh, got %d", n)
	}
	m.paused = true
	if n := tick(true); n != 1 {
		t.Errorf("pause stops the stream, not the panel refresh, got %d", n)
	}
}

// TestRefreshAfterFastBackfill: the pi backfill takes tens of
// milliseconds, so the source is no longer loading by the first stream
// tick. Init's refresh ran against an empty store; the first tick must
// refresh the panels once more, and later ticks must not.
func TestRefreshAfterFastBackfill(t *testing.T) {
	src := &fakeProgressSource{fakeSource: fakeSource{info: fullInfo()}, loading: false}
	m := newTestModel(t, src)
	m.cfg.StreamPollMs = 1

	tick := func() int {
		before := src.stats
		next, cmd := m.Update(streamTickMsg(time.Now()))
		m = next.(Model)
		runCmds(cmd)
		return src.stats - before
	}
	if n := tick(); n != 1 {
		t.Errorf("first tick after a fast backfill should refresh once, got %d", n)
	}
	if n := tick(); n != 0 {
		t.Errorf("steady state ticks should not refresh, got %d", n)
	}

	// A source switch starts the cycle again.
	defer source.Register("fake", source.Type{Open: func(name string, sc source.SourceConfig, _ source.OpenOptions) (source.Source, error) {
		return src, nil
	}})()
	m.cfg.Sources["other"] = source.SourceConfig{Type: "fake"}
	next, _ := m.switchSource("other")
	m = next.(Model)
	if n := tick(); n != 1 {
		t.Errorf("first tick after a switch should refresh once, got %d", n)
	}
}

// TestCorruptPresetsSurfaced: a presets file that fails to parse leaves the
// model with empty presets and a startup toast naming the error.
func TestCorruptPresetsSurfaced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "tailboard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.toml"), []byte("[items\nthis is not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := source.Defaults()
	if err := cfg.Normalise(); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{info: source.Info{Kind: "jsonl", Name: "x"}}
	m := NewModel(cfg, src.Info().Name, src)

	if !strings.HasPrefix(m.toast, "presets: ") || m.toastLevel != toastError {
		t.Fatalf("toast = %q level %d, want a presets: error", m.toast, m.toastLevel)
	}
	if !m.toastExpires.After(time.Now()) {
		t.Fatalf("toast already expired at %v", m.toastExpires)
	}
	if m.presets.Items == nil || len(m.presets.Items) != 0 {
		t.Fatalf("presets = %#v, want empty non-nil map", m.presets.Items)
	}
}

// TestStaleSourceDropped: a stream batch built against a source that was
// then switched away carries the old cursor. Applying it would position
// the new source's stream at the old source's offset and fill the buffer
// with the old source's events.
func TestStaleSourceDropped(t *testing.T) {
	m := newTestModel(t, &fakeSource{info: fullInfo()})
	stale := m.fetchLogs()
	staleStats := m.fetchStats()

	defer source.Register("fake", source.Type{Open: func(name string, sc source.SourceConfig, _ source.OpenOptions) (source.Source, error) {
		return &fakeSource{info: source.Info{Name: name, Kind: "fake"}}, nil
	}})()
	m.cfg.Sources["other"] = source.SourceConfig{Type: "fake"}
	next, _ := m.switchSource("other")
	m = next.(Model)

	next, _ = m.Update(stale())
	m = next.(Model)
	if !m.streamCursor.IsZero() || m.logs.Len() != 0 {
		t.Fatalf("stale batch applied: cursor %+v, %d events", m.streamCursor, m.logs.Len())
	}
	next, _ = m.Update(staleStats())
	m = next.(Model)
	if m.stats.Total != 0 {
		t.Errorf("stale stats applied: total %d", m.stats.Total)
	}

	next, _ = m.Update(m.fetchLogs()())
	m = next.(Model)
	if m.streamCursor.Seq != 2 || m.logs.Len() != 1 {
		t.Errorf("fresh batch not applied: cursor %+v, %d events", m.streamCursor, m.logs.Len())
	}
	next, _ = m.Update(m.fetchStats()())
	m = next.(Model)
	if m.stats.Total != 120 {
		t.Errorf("fresh stats not applied: total %d", m.stats.Total)
	}
}

// failHydrateSource is a fakeSource whose Hydrate fails.
type failHydrateSource struct{ fakeSource }

func (f *failHydrateSource) Hydrate(ctx context.Context, ev source.LogEvent) (map[string]any, error) {
	return nil, errors.New("file rotated")
}

// toastOf runs cmd, feeds the result through Update and returns the toast.
func toastOf(t *testing.T, m Model, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	next, _ := m.Update(cmd())
	return next.(Model).toast
}

// TestHydrateAsync: expand, copy and preset save do their IO in commands,
// never inside Update.
func TestHydrateAsync(t *testing.T) {
	var clip []string
	writeClipboard = func(s string) error { clip = append(clip, s); return nil }
	defer func() { writeClipboard = clipboard.WriteAll }()

	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)

	next, cmd := m.Update(keyPress("enter"))
	m = next.(Model)
	if cmd == nil || m.mode != ModeExpand || m.expandedLog == nil || m.expandedLog.Raw != nil {
		t.Fatalf("enter should open the modal unhydrated and return a command")
	}
	if !strings.Contains(stripANSI(m.View()), "loading...") {
		t.Error("modal should say loading until the hydrate lands")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.expandLoading || m.expandedLog.Raw["full"] != true {
		t.Fatalf("hydrate result not applied: %v", m.expandedLog.Raw)
	}
	if strings.Contains(stripANSI(m.View()), "loading...") {
		t.Error("modal still loading after the result landed")
	}

	// y in the modal copies in the command.
	_, cmd = m.Update(keyPress("y"))
	if len(clip) != 0 {
		t.Fatal("clipboard written inside Update")
	}
	if got := toastOf(t, m, cmd); got != "copied event to clipboard" || len(clip) != 1 || !strings.Contains(clip[0], `"full": true`) {
		t.Errorf("modal copy: toast %q, clipboard %q", got, clip)
	}

	// y on a stream row.
	next, _ = m.Update(keyPress("esc"))
	m = next.(Model)
	_, cmd = m.Update(keyPress("y"))
	if got := toastOf(t, m, cmd); got != "copied event to clipboard" || len(clip) != 2 {
		t.Errorf("row copy: toast %q, %d clipboard writes", got, len(clip))
	}

	// A failed copy says so.
	writeClipboard = func(string) error { return errors.New("no pbcopy") }
	_, cmd = m.Update(keyPress("y"))
	if got := toastOf(t, m, cmd); got != "copy failed: no pbcopy" {
		t.Errorf("failed copy toast = %q", got)
	}

	// s writes presets.toml in the command.
	path, err := presetsWritePath()
	if err != nil {
		t.Fatal(err)
	}
	_, cmd = m.Update(keyPress("s"))
	if _, err := os.Stat(path); err == nil {
		t.Fatal("presets written inside Update")
	}
	if got := toastOf(t, m, cmd); !strings.HasPrefix(got, "saved as preset 1") {
		t.Errorf("preset save toast = %q", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("presets not written: %v", err)
	}
}

// TestHydrateError: a failed hydrate shows the event's fields and says why.
func TestHydrateError(t *testing.T) {
	m := newTestModel(t, &failHydrateSource{fakeSource{info: fullInfo()}})
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	next, cmd := m.Update(keyPress("enter"))
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.expandLoading || m.expandedLog.Raw["route"] != "/x" {
		t.Errorf("modal should fall back to the fields, got %v", m.expandedLog.Raw)
	}
	if m.toast != "hydrate failed: file rotated" {
		t.Errorf("toast = %q", m.toast)
	}

	// Copying an event that cannot be hydrated copies its fields and says so.
	var clip string
	writeClipboard = func(s string) error { clip = s; return nil }
	defer func() { writeClipboard = clipboard.WriteAll }()
	row, _ := m.Update(keyPress("esc"))
	_, cmd = row.Update(keyPress("y"))
	if got := toastOf(t, m, cmd); got != "copied fields only, hydrate failed: file rotated" || !strings.Contains(clip, `"route": "/x"`) {
		t.Errorf("fallback copy: toast %q, clipboard %q", got, clip)
	}

	// A result from an earlier source is dropped.
	next, _ = m.Update(keyPress("esc"))
	m = next.(Model)
	next, _ = m.Update(keyPress("enter"))
	m = next.(Model)
	ev := *m.expandedLog
	next, _ = m.Update(hydrateMsg{epoch: m.epoch - 1, ev: ev, raw: map[string]any{"stale": true}})
	if got := next.(Model); !got.expandLoading || got.expandedLog.Raw != nil {
		t.Error("hydrate result from an earlier source was applied")
	}

	// A result for a modal that was closed is dropped.
	next, _ = m.Update(keyPress("esc"))
	m = next.(Model)
	if next, _ = m.Update(hydrateMsg{epoch: m.epoch, ev: ev}); next.(Model).expandedLog != nil {
		t.Error("hydrate result reopened a closed modal")
	}
}

// scrollModel is a model with n numbered events and a 10-row log body.
func scrollModel(t *testing.T, n int) Model {
	t.Helper()
	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 80, 24, true
	topH, _ := m.rowHeights()
	if rows := m.logBodyRows(topH); rows != 10 {
		t.Fatalf("log body is %d rows, test wants 10", rows)
	}
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	evs := make([]source.LogEvent, n)
	for i := range evs {
		evs[i] = source.LogEvent{ID: fmt.Sprintf("1:%d", i), Time: base.Add(time.Duration(i) * time.Second),
			Level: "info", Message: fmt.Sprintf("ev%03d.", i)}
	}
	m.logs.Append(evs)
	return m
}

// press feeds one key through Update.
func press(m Model, k string) Model {
	var msg tea.KeyMsg
	switch k {
	case "pgup":
		msg = tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	default:
		msg = keyPress(k)
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

// TestScrollClamp: g used to set the offset to the buffer length, which
// blanked the panel, and k and pgup had no upper bound.
func TestScrollClamp(t *testing.T) {
	m := scrollModel(t, 100)
	top := press(m, "g")
	if top.scrollOff != 90 {
		t.Errorf("g: scrollOff = %d, want 90", top.scrollOff)
	}
	out := stripANSI(top.View())
	if !strings.Contains(out, "ev000.") || !strings.Contains(out, "ev009.") || strings.Contains(out, "ev010.") {
		t.Errorf("g should show the oldest page, ev000 to ev009:\n%s", out)
	}
	if strings.Contains(out, "no logs in view") {
		t.Error("g blanked the panel")
	}

	k := m
	for i := 0; i < 200; i++ {
		k = press(k, "k")
	}
	if k.scrollOff != top.scrollOff {
		t.Errorf("200 k presses: scrollOff = %d, want %d (same as g)", k.scrollOff, top.scrollOff)
	}

	if tail := press(top, "G"); tail.scrollOff != 0 || tail.paused {
		t.Errorf("G should return to the tail and resume, got off=%d paused=%v", tail.scrollOff, tail.paused)
	}

	pg := m
	for i := 0; i < 20; i++ {
		pg = press(pg, "pgup")
	}
	if pg.scrollOff != 90 {
		t.Errorf("pgup past the top: scrollOff = %d, want 90", pg.scrollOff)
	}
	if pg = press(pg, "pgdown"); pg.scrollOff != 80 {
		t.Errorf("pgdown from the top: scrollOff = %d, want 80", pg.scrollOff)
	}

	// Fewer events than rows: nothing to scroll.
	if short := press(scrollModel(t, 5), "g"); short.scrollOff != 0 {
		t.Errorf("g on a short list: scrollOff = %d, want 0", short.scrollOff)
	}
}
