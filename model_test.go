package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeProgressSource is a fakeSource that reports backfill progress and
// counts aggregate queries.
type fakeProgressSource struct {
	fakeSource
	loading bool
	stats   int
}

func (f *fakeProgressSource) Progress() Progress { return Progress{Loading: f.loading} }
func (f *fakeProgressSource) Stats(ctx context.Context, lookback time.Duration) (Stats, error) {
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
	sourceOpeners["fake"] = func(name string, sc SourceConfig) (Source, error) { return src, nil }
	defer delete(sourceOpeners, "fake")
	m.cfg.Sources["other"] = SourceConfig{Type: "fake"}
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
	cfg := Defaults()
	if err := cfg.Normalise(); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{info: SourceInfo{Kind: "jsonl", Name: "x"}}
	m := NewModel(cfg, src.Info().Name, src)

	if !strings.HasPrefix(m.toast, "presets: ") {
		t.Fatalf("toast = %q, want a presets: error", m.toast)
	}
	if !m.toastExpires.After(time.Now()) {
		t.Fatalf("toast already expired at %v", m.toastExpires)
	}
	if m.presets.Items == nil || len(m.presets.Items) != 0 {
		t.Fatalf("presets = %#v, want empty non-nil map", m.presets.Items)
	}
}
