package main

import (
	"slices"
	"testing"
)

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
