package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AXIOM_DATASET", "")
	dir := filepath.Join(home, "logs")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "app.jsonl")
	os.WriteFile(file, []byte(""), 0o644)

	base := func(text string) Config {
		cfg, err := ParseConfig(text)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("dataset flag wins and creates ad hoc axiom", func(t *testing.T) {
		cfg := base("default_source = \"pi\"")
		name, err := resolveSource(&cfg, cliOpts{dataset: "x", source: "pi"})
		if err != nil || name != "x" || cfg.Sources["x"].Type != "axiom" {
			t.Fatalf("got %q %v %+v", name, err, cfg.Sources["x"])
		}
	})
	t.Run("source flag names a configured source", func(t *testing.T) {
		cfg := base("[sources.app]\ntype = \"jsonl\"\npaths = [\"/tmp/x.jsonl\"]")
		name, err := resolveSource(&cfg, cliOpts{source: "app"})
		if err != nil || name != "app" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("source flag with a path makes an ad hoc jsonl source", func(t *testing.T) {
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{source: dir})
		if err != nil || name != dir || cfg.Sources[dir].Type != "jsonl" {
			t.Fatalf("got %q %v %+v", name, err, cfg.Sources[dir])
		}
	})
	t.Run("unknown source errors and lists known", func(t *testing.T) {
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{source: "nope"})
		if err == nil || name != "nope" || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "known") {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("positional paths", func(t *testing.T) {
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{paths: []string{file, dir}})
		if err != nil || name != file || len(cfg.Sources[file].Paths) != 2 {
			t.Fatalf("got %q %v %+v", name, err, cfg.Sources[file])
		}
	})
	t.Run("default_source beats default_dataset", func(t *testing.T) {
		cfg := base("default_source = \"pi\"\ndefault_dataset = \"d\"")
		name, err := resolveSource(&cfg, cliOpts{})
		if err != nil || name != "pi" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("default_dataset", func(t *testing.T) {
		cfg := base("default_dataset = \"d\"")
		name, err := resolveSource(&cfg, cliOpts{})
		if err != nil || name != "d" || cfg.Sources["d"].Type != "axiom" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("AXIOM_DATASET env", func(t *testing.T) {
		t.Setenv("AXIOM_DATASET", "envds")
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{})
		if err != nil || name != "envds" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("single configured source", func(t *testing.T) {
		cfg := base("[sources.only]\ntype = \"jsonl\"\npaths = [\"/tmp/x.jsonl\"]")
		name, err := resolveSource(&cfg, cliOpts{})
		if err != nil || name != "only" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("pi sessions directory present", func(t *testing.T) {
		os.MkdirAll(filepath.Join(home, ".pi", "agent", "sessions"), 0o755)
		defer os.RemoveAll(filepath.Join(home, ".pi"))
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{})
		if err != nil || name != "pi" {
			t.Fatalf("got %q %v", name, err)
		}
	})
	t.Run("nothing applies", func(t *testing.T) {
		cfg := base("")
		name, err := resolveSource(&cfg, cliOpts{})
		if err == nil || name != "" {
			t.Fatalf("got %q %v", name, err)
		}
	})
}

func TestReorderArgs(t *testing.T) {
	got := reorderArgs([]string{"app.jsonl", "--check", "--source", "x", "dir/", "--interval=5"})
	want := "--check --source x --interval=5 app.jsonl dir/"
	if strings.Join(got, " ") != want {
		t.Errorf("reorderArgs = %q, want %q", strings.Join(got, " "), want)
	}
}
