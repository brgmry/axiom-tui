package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Presets is a slot-keyed map of saved filter snapshots. Slots 1-9 only —
// keeping the keymap tight (number keys load presets directly).
//
// Stored at ~/.config/axiom-tui/presets.toml as:
//
//   [presets.1]
//   name = "errors only"
//   search = ""
//   client = ""
//   hide_levels = ["info", "warn"]
//
// Per-dataset isolation isn't needed yet — most users hop between two
// datasets max, and presets that don't apply just no-op. Add scoping later
// if/when that becomes annoying.
type Presets struct {
	Items map[string]Preset `toml:"presets"`
}

// Preset is a stored filter snapshot. Mirrors LogFilter but flattened so
// TOML serializes cleanly.
type Preset struct {
	Name       string   `toml:"name"`
	Search     string   `toml:"search"`
	Client     string   `toml:"client"`
	HideLevels []string `toml:"hide_levels"`
}

func presetsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "axiom-tui", "presets.toml"), nil
}

// LoadPresets reads the presets file. Missing file = empty presets, not error.
func LoadPresets() (Presets, error) {
	path, err := presetsPath()
	if err != nil {
		return Presets{Items: map[string]Preset{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Presets{Items: map[string]Preset{}}, nil
		}
		return Presets{}, fmt.Errorf("read presets: %w", err)
	}
	var p Presets
	if _, err := toml.Decode(string(data), &p); err != nil {
		return Presets{}, fmt.Errorf("parse presets: %w", err)
	}
	if p.Items == nil {
		p.Items = map[string]Preset{}
	}
	return p, nil
}

// Save writes presets back to disk. Creates the parent directory if needed.
func (p Presets) Save() error {
	path, err := presetsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(p)
}

// FilterToPreset converts the live filter into a saved snapshot. The auto-name
// is "<search> @<client>" or "errors only" / "warns only" — picked to be
// recognizable in the help footer when scanning.
func FilterToPreset(f LogFilter) Preset {
	hidden := []string{}
	for lv, h := range f.HideLevels {
		if h {
			hidden = append(hidden, lv)
		}
	}
	parts := []string{}
	if f.Search != "" {
		parts = append(parts, "/"+trunc(f.Search, 16))
	}
	if f.Client != "" {
		parts = append(parts, "@"+trunc(f.Client, 16))
	}
	for _, lv := range hidden {
		parts = append(parts, "¬"+lv)
	}
	name := strings.Join(parts, " ")
	if name == "" {
		name = "(empty)"
	}
	return Preset{
		Name:       name,
		Search:     f.Search,
		Client:     f.Client,
		HideLevels: hidden,
	}
}

// PresetToFilter is the inverse — apply a saved preset to a fresh filter.
func PresetToFilter(p Preset) LogFilter {
	hide := map[string]bool{}
	for _, lv := range p.HideLevels {
		hide[lv] = true
	}
	return LogFilter{
		Search:     p.Search,
		Client:     p.Client,
		HideLevels: hide,
	}
}

// NextSlot returns the lowest-numbered free slot 1-9, or "" if all taken
// (caller can overwrite explicitly).
func (p Presets) NextSlot() string {
	for i := 1; i <= 9; i++ {
		key := fmt.Sprintf("%d", i)
		if _, ok := p.Items[key]; !ok {
			return key
		}
	}
	return ""
}
