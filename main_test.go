package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	t.Run("dataset flag means axiom even when a jsonl source has the name", func(t *testing.T) {
		cfg := base("[sources.app]\ntype = \"jsonl\"\npaths = [\"/tmp/x.jsonl\"]")
		name, err := resolveSource(&cfg, cliOpts{dataset: "app"})
		if err != nil || name != "app" || cfg.Sources["app"].Type != "axiom" || cfg.Sources["app"].Dataset != "app" {
			t.Fatalf("got %q %v %+v", name, err, cfg.Sources["app"])
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

// checkOutput runs --check against src and returns the printed JSON.
func checkOutput(t *testing.T, name string, src Source) (string, map[string]any) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "check")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := runCheck(out, name, src); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("check output is not JSON: %v\n%s", err, raw)
	}
	return string(raw), rep
}

// TestCheckAgentsKey: the agents key marks an agent source. It must be
// present, as an empty array, when the store has no agents yet, and
// absent for every other source.
func TestCheckAgentsKey(t *testing.T) {
	pi := newTestPiSource(t, t.TempDir(), 0)
	pi.pass()
	raw, rep := checkOutput(t, "pi", pi)
	agents, ok := rep["agents"]
	if !ok {
		t.Fatalf("agents key missing for an empty agent source:\n%s", raw)
	}
	if list, isList := agents.([]any); !isList || len(list) != 0 {
		t.Errorf("agents should be an empty array, got %v", agents)
	}

	path := filepath.Join(t.TempDir(), "a.jsonl")
	writeLines(t, path, 0, 3)
	fs := newTestFileSource(t, path)
	fs.pass()
	raw, rep = checkOutput(t, "files", fs)
	if _, ok := rep["agents"]; ok {
		t.Errorf("agents key should be absent for a jsonl source:\n%s", raw)
	}
	if _, ok := rep["cost"]; ok {
		t.Errorf("cost key should be absent without a cost field:\n%s", raw)
	}
}

// TestCheckRoundsMoney: cost figures are printed to the cent, not as raw
// float64 sums.
func TestCheckRoundsMoney(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Minute)
	entries := []piEntry{piHeader("s1", "/work/pi-os", base)}
	for i := 0; i < 3; i++ {
		entries = append(entries, piUsageEntry(fmt.Sprintf("u%d", i), "turn", "claude-opus-5-5", 0.1, base.Add(time.Duration(i)*time.Second)))
	}
	writePiFile(t, dir, "a.jsonl", entries)
	pi := newTestPiSource(t, dir, 0)
	pi.pass()
	raw, _ := checkOutput(t, "pi", pi)
	if noisy := regexp.MustCompile(`\d\.\d{5,}`).FindString(raw); noisy != "" {
		t.Errorf("check prints float noise %s:\n%s", noisy, raw)
	}
	if !strings.Contains(raw, `"total_24h":0.3`) {
		t.Errorf("expected total_24h 0.3:\n%s", raw)
	}
}
