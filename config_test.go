package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigLegacy(t *testing.T) {
	cfg, err := ParseConfig(readmeConfig)
	if err != nil {
		t.Fatal(err)
	}
	sc, ok := cfg.Sources["my-dataset"]
	if !ok {
		t.Fatalf("legacy dataset not mapped: %v", cfg.SourceNames())
	}
	if sc.Type != "axiom" {
		t.Errorf("type = %q, want axiom", sc.Type)
	}
	if sc.Dataset != "my-dataset" {
		t.Errorf("dataset = %q", sc.Dataset)
	}
	if sc.Fields.Group != "fields.userId" {
		t.Errorf("group = %q", sc.Fields.Group)
	}
	if len(sc.Fields.Duration) != 1 || sc.Fields.Duration[0] != "fields.durationMs" {
		t.Errorf("duration = %v", sc.Fields.Duration)
	}
	if len(sc.Fields.Interesting) != 3 {
		t.Errorf("interesting = %v", sc.Fields.Interesting)
	}
	if len(sc.RoutePrefixes) != 5 {
		t.Errorf("route prefixes = %v", sc.RoutePrefixes)
	}
	if cfg.DefaultSourceName() != "my-dataset" {
		t.Errorf("default source = %q", cfg.DefaultSourceName())
	}
	// Axiom defaults that keep the APL byte-identical.
	if sc.Fields.Cost != "fields.costDollars" || sc.Fields.CostGroup != "fields.clientName" {
		t.Errorf("cost mapping = %q by %q", sc.Fields.Cost, sc.Fields.CostGroup)
	}
	if sc.Fields.DisplayPrefix != "fields." {
		t.Errorf("display prefix = %q", sc.Fields.DisplayPrefix)
	}
	if sc.Fields.Trace != "fields.requestId" {
		t.Errorf("trace = %q", sc.Fields.Trace)
	}
	// Built-in pi is present alongside.
	if pi, ok := cfg.Sources["pi"]; !ok || pi.Type != "pi" || !pi.builtin {
		t.Errorf("built-in pi missing or wrong: %+v", pi)
	}
}

func TestConfigLegacyDefaultsWithoutFields(t *testing.T) {
	cfg, err := ParseConfig(`
[datasets.bare]
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["bare"]
	if len(sc.RoutePrefixes) != 5 {
		t.Errorf("route prefixes = %v", sc.RoutePrefixes)
	}
	if sc.Fields.Group != "" {
		t.Errorf("group should be empty, got %q", sc.Fields.Group)
	}
	if len(sc.Fields.Duration) != 0 {
		t.Errorf("duration should be empty, got %v", sc.Fields.Duration)
	}
}

func TestConfigConflict(t *testing.T) {
	_, err := ParseConfig(`
[datasets.x]
[sources.x]
type = "axiom"
`)
	if err == nil {
		t.Fatal("expected error for a name in both tables")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should name the source: %v", err)
	}
}

func TestConfigJsonlNeedsPaths(t *testing.T) {
	_, err := ParseConfig(`
[sources.app]
type = "jsonl"
`)
	if err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("expected a paths error, got %v", err)
	}
}

func TestConfigPathFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacy := filepath.Join(home, ".config", "axiom-tui")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), []byte("refresh_seconds = 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 7 {
		t.Errorf("legacy config not read: refresh = %d", cfg.RefreshSeconds)
	}

	modern := filepath.Join(home, ".config", "tailboard")
	if err := os.MkdirAll(modern, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modern, "config.toml"), []byte("refresh_seconds = 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 9 {
		t.Errorf("tailboard config should win: refresh = %d", cfg.RefreshSeconds)
	}

	// No config at all: defaults, still normalised with the built-in pi.
	t.Setenv("HOME", t.TempDir())
	cfg, err = LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshSeconds != 15 || cfg.Sources["pi"].Type != "pi" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
}

func TestConfigStringListAndDurations(t *testing.T) {
	cfg, err := ParseConfig(`
[sources.app]
type = "jsonl"
paths = "~/logs/*.jsonl"
backfill = "2h"
hide_levels = ["debug", "info"]

[sources.app.fields]
time = "ts"
level = ["lvl", "severity"]
duration = "durationMs"
level_map = { "WARNING" = "warn" }
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["app"]
	if len(sc.Paths) != 1 || sc.Paths[0] != "~/logs/*.jsonl" {
		t.Errorf("paths = %v", sc.Paths)
	}
	if sc.Backfill.D() != 2*time.Hour {
		t.Errorf("backfill = %v", sc.Backfill.D())
	}
	if len(sc.HideLevels) != 2 {
		t.Errorf("hide_levels = %v", sc.HideLevels)
	}
	if len(sc.Fields.Time) != 1 || sc.Fields.Time[0] != "ts" {
		t.Errorf("time = %v", sc.Fields.Time)
	}
	if len(sc.Fields.Level) != 2 {
		t.Errorf("level = %v", sc.Fields.Level)
	}
	if len(sc.Fields.Duration) != 1 {
		t.Errorf("duration = %v", sc.Fields.Duration)
	}
	if sc.Fields.LevelMap["WARNING"] != "warn" {
		t.Errorf("level_map = %v", sc.Fields.LevelMap)
	}
	if len(sc.Fields.Message) != 2 || sc.Fields.Message[0] != "message" {
		t.Errorf("message default = %v", sc.Fields.Message)
	}
	if sc.MaxEvents != defaultMaxEvents || sc.MaxLineBytes != defaultMaxLineBytes {
		t.Errorf("file defaults = %d / %d", sc.MaxEvents, sc.MaxLineBytes)
	}
}

func TestConfigPiDefaults(t *testing.T) {
	cfg, err := ParseConfig(`
[sources.agents]
type = "pi"
orphan_after = "30s"
`)
	if err != nil {
		t.Fatal(err)
	}
	sc := cfg.Sources["agents"]
	if len(sc.Paths) != 1 || sc.Paths[0] != defaultPiPaths {
		t.Errorf("paths = %v", sc.Paths)
	}
	if sc.Backfill.D() != defaultBackfill {
		t.Errorf("backfill = %v", sc.Backfill.D())
	}
	if sc.OrphanAfter.D() != 30*time.Second {
		t.Errorf("orphan_after = %v", sc.OrphanAfter.D())
	}
	if len(sc.HideLevels) != 1 || sc.HideLevels[0] != "debug" {
		t.Errorf("hide_levels = %v", sc.HideLevels)
	}
	if sc.builtin {
		t.Error("configured pi-type source should not be marked builtin")
	}

	// A config entry named pi replaces the built-in registration.
	cfg, err = ParseConfig(`
[sources.pi]
type = "pi"
backfill = "1h"
`)
	if err != nil {
		t.Fatal(err)
	}
	pi := cfg.Sources["pi"]
	if pi.builtin || pi.Backfill.D() != time.Hour || pi.OrphanAfter.D() != defaultOrphanAfter {
		t.Errorf("pi override wrong: %+v", pi)
	}
	if len(cfg.UserSourceNames()) != 1 {
		t.Errorf("user sources = %v", cfg.UserSourceNames())
	}
}
