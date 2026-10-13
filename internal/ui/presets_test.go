package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/brgmry/tailboard/internal/source"
)

// presetHome points HOME at a fresh directory and returns the tailboard and
// legacy config directories under it, as ConfigDirs resolves them.
func presetHome(t *testing.T) (dir, legacy string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".config", "tailboard"), filepath.Join(home, ".config", "axiom-tui")
}

// TestPresetsRoundTrip: Save writes to the tailboard directory, creating it,
// and LoadPresets reads back the same presets.
func TestPresetsRoundTrip(t *testing.T) {
	dir, _ := presetHome(t)

	empty, err := LoadPresets()
	if err != nil || len(empty.Items) != 0 || empty.Items == nil {
		t.Fatalf("missing file: got %+v, %v; want empty non-nil items", empty, err)
	}

	want := Presets{Items: map[string]Preset{
		"1": {Name: "¬info ¬warn", HideLevels: []string{"info", "warn"}},
		"4": {Name: "/timeout @web", Search: "timeout", Client: "web", HideLevels: []string{}},
	}}
	if err := want.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "presets.toml")); err != nil {
		t.Fatalf("Save did not write to the tailboard dir: %v", err)
	}
	got, err := LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

// TestPresetsLegacyDir: with no tailboard file, presets load from the
// legacy axiom-tui directory.
func TestPresetsLegacyDir(t *testing.T) {
	_, legacy := presetHome(t)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	data := "[presets.2]\nname = \"old\"\nsearch = \"x\"\n"
	if err := os.WriteFile(filepath.Join(legacy, "presets.toml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Items["2"]; got.Name != "old" || got.Search != "x" {
		t.Errorf("legacy preset = %+v", got)
	}
}

// TestPresetsCorrupt: a file that is not TOML, or cannot be read, is an
// error rather than silently empty presets.
func TestPresetsCorrupt(t *testing.T) {
	dir, _ := presetHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "presets.toml")
	if err := os.WriteFile(path, []byte("[presets.1\nname = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresets(); err == nil {
		t.Error("corrupt presets file: want an error")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresets(); err == nil {
		t.Error("unreadable presets file: want an error")
	}
}

// TestPresetFilterRoundTrip: a filter saved and reapplied keeps its search,
// client and hidden levels, and the base filter's field keys survive.
func TestPresetFilterRoundTrip(t *testing.T) {
	base := NewLogFilter(source.Info{
		Filters:    source.FilterFields{Provider: "fields.provider", Operation: "fields.op", Trace: "fields.traceId"},
		HideLevels: []string{"debug"},
	})
	live := base
	live.HideLevels = map[string]bool{"info": true, "warn": true, "debug": false}
	live.Search = "timeout"
	live.Client = "web"
	live.Provider = "openai" // not part of a preset

	got := PresetToFilter(FilterToPreset(live), base)
	if got.Search != "timeout" || got.Client != "web" {
		t.Errorf("search/client = %q/%q", got.Search, got.Client)
	}
	if want := map[string]bool{"info": true, "warn": true}; !reflect.DeepEqual(got.HideLevels, want) {
		t.Errorf("hidden = %v, want %v", got.HideLevels, want)
	}
	if got.Provider != "" {
		t.Errorf("provider leaked through the preset: %q", got.Provider)
	}
	if got.fields != base.fields || !slices.Equal(got.seed, base.seed) {
		t.Errorf("base field keys lost: %+v", got)
	}

	p := Preset{Name: "/x ¬error", Search: "x", HideLevels: []string{"error"}}
	if back := FilterToPreset(PresetToFilter(p, base)); !reflect.DeepEqual(back, p) {
		t.Errorf("preset round trip = %+v, want %+v", back, p)
	}
	if empty := FilterToPreset(LogFilter{}); empty.Name != "(empty)" {
		t.Errorf("empty filter name = %q", empty.Name)
	}
}

// TestNextSlot: the lowest free slot 1-9, gaps first, "" when full.
func TestNextSlot(t *testing.T) {
	items := map[string]Preset{}
	p := Presets{Items: items}
	if got := p.NextSlot(); got != "1" {
		t.Errorf("empty: %q", got)
	}
	items["1"], items["3"] = Preset{}, Preset{}
	if got := p.NextSlot(); got != "2" {
		t.Errorf("gap: %q", got)
	}
	for _, k := range []string{"2", "4", "5", "6", "7", "8", "9"} {
		items[k] = Preset{}
	}
	if got := p.NextSlot(); got != "" {
		t.Errorf("full: %q", got)
	}
}

// TestFilterToPresetDeterministic: hidden levels come from a map, so the
// auto-name and hide_levels must not depend on iteration order.
func TestFilterToPresetDeterministic(t *testing.T) {
	f := LogFilter{
		Search:     "timeout",
		Client:     "web",
		HideLevels: map[string]bool{"warn": true, "info": true, "debug": true, "error": false},
	}
	want := Preset{
		Name:       "/timeout @web ¬debug ¬info ¬warn",
		Search:     "timeout",
		Client:     "web",
		HideLevels: []string{"debug", "info", "warn"},
	}
	for i := 0; i < 50; i++ {
		got := FilterToPreset(f)
		if got.Name != want.Name || !slices.Equal(got.HideLevels, want.HideLevels) {
			t.Fatalf("run %d: got %+v, want %+v", i, got, want)
		}
	}
}
