package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/brgmry/tailboard/internal/source"
)

func TestResolveSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AXIOM_DATASET", "")
	dir := filepath.Join(home, "logs")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "app.jsonl")
	os.WriteFile(file, []byte(""), 0o644)

	base := func(text string) source.Config {
		cfg, err := source.ParseConfig(text)
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

// TestMainEnvFile runs main in a child process: a missing --env file exits
// 1 with a message naming it, and --env /dev/null still starts.
func TestMainEnvFile(t *testing.T) {
	if os.Getenv("TAILBOARD_TEST_MAIN") == "1" {
		os.Args = append([]string{"tailboard"}, strings.Split(os.Getenv("TAILBOARD_TEST_ARGS"), "\n")...)
		main()
		os.Exit(0)
	}
	run := func(args ...string) (string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainEnvFile$")
		cmd.Env = append(os.Environ(), "TAILBOARD_TEST_MAIN=1", "TAILBOARD_TEST_ARGS="+strings.Join(args, "\n"),
			"HOME="+t.TempDir(), "AXIOM_DATASET=")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}

	missing := filepath.Join(t.TempDir(), "missing.env")
	out, code := run("--env", missing, "--check", "internal/source/testdata/generic/sample.jsonl")
	if code != 1 || !strings.Contains(out, missing) {
		t.Errorf("missing --env: exit %d, output %q; want 1 naming %s", code, out, missing)
	}
	out, code = run("--env", "/dev/null", "--check", "internal/source/testdata/generic/sample.jsonl")
	if code != 0 || !strings.Contains(out, `"events"`) {
		t.Errorf("--env /dev/null: exit %d, output %q", code, out)
	}
}

func TestReorderArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"flags after positionals", []string{"app.jsonl", "--check", "--source", "x", "dir/", "--interval=5"}, "--check --source x --interval=5 app.jsonl dir/"},
		{"terminator keeps the rest positional", []string{"app.jsonl", "--check", "--", "--source", "x"}, "--check -- app.jsonl --source x"},
		{"terminator protects a dash path", []string{"--", "-odd.jsonl"}, "-- -odd.jsonl"},
		{"single-dash flag=value after a positional", []string{"app.jsonl", "-source=x", "-check"}, "-source=x -check app.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, _ := newFlagSet()
			got := strings.Join(reorderArgs(fs, tc.args), " ")
			if got != tc.want {
				t.Errorf("reorderArgs = %q, want %q", got, tc.want)
			}
			if err := fs.Parse(reorderArgs(fs, tc.args)); err != nil {
				t.Errorf("parse: %v", err)
			}
		})
	}
}

// checkOutput runs --check against src and returns the printed JSON.
func checkOutput(t *testing.T, name string, src source.Source) (string, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	if err := runCheck(&out, name, src); err != nil {
		t.Fatal(err)
	}
	raw := out.Bytes()
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("check output is not JSON: %v\n%s", err, raw)
	}
	return string(raw), rep
}

// openTestSource opens a configured source of type typ over path, the way
// main does, and closes it when the test ends.
func openTestSource(t *testing.T, typ, path string) source.Source {
	t.Helper()
	cfg, err := source.ParseConfig(fmt.Sprintf("[sources.s]\ntype = %q\npaths = %q\n", typ, path))
	if err != nil {
		t.Fatal(err)
	}
	src, err := source.Open(cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return src
}

// writeJSONL writes one JSON object per line to path.
func writeJSONL(t *testing.T, path string, entries ...map[string]any) {
	t.Helper()
	var b bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckAgentsKey: the agents key marks an agent source. It must be
// present, as an empty array, when the store has no agents yet, and
// absent for every other source.
func TestCheckAgentsKey(t *testing.T) {
	pi := openTestSource(t, "pi", t.TempDir())
	raw, rep := checkOutput(t, "pi", pi)
	agents, ok := rep["agents"]
	if !ok {
		t.Fatalf("agents key missing for an empty agent source:\n%s", raw)
	}
	if list, isList := agents.([]any); !isList || len(list) != 0 {
		t.Errorf("agents should be an empty array, got %v", agents)
	}

	path := filepath.Join(t.TempDir(), "a.jsonl")
	lines := make([]map[string]any, 0, 3)
	for i := 0; i < 3; i++ {
		lines = append(lines, map[string]any{"timestamp": fmt.Sprintf("2026-10-12T09:%02d:00Z", i), "level": "info", "message": fmt.Sprintf("line %d", i), "n": i})
	}
	writeJSONL(t, path, lines...)
	fs := openTestSource(t, "jsonl", path)
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
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	entries := []map[string]any{{"type": "session", "version": 3, "id": "s1", "timestamp": ts(base), "cwd": "/work/pi-os"}}
	for i := 0; i < 3; i++ {
		entries = append(entries, map[string]any{
			"type": "usage", "id": fmt.Sprintf("u%d", i), "parentId": "x", "timestamp": ts(base.Add(time.Duration(i) * time.Second)),
			"kind": "turn", "provider": "anthropic", "model": "claude-opus-5-5",
			"usage": map[string]any{"totalTokens": 100, "cost": map[string]any{"total": 0.1}},
		})
	}
	writeJSONL(t, filepath.Join(dir, "a.jsonl"), entries...)
	pi := openTestSource(t, "pi", dir)
	raw, _ := checkOutput(t, "pi", pi)
	if noisy := regexp.MustCompile(`\d\.\d{5,}`).FindString(raw); noisy != "" {
		t.Errorf("check prints float noise %s:\n%s", noisy, raw)
	}
	if !strings.Contains(raw, `"total_24h":0.3`) {
		t.Errorf("expected total_24h 0.3:\n%s", raw)
	}
}

// TestRunCheckSample runs --check end to end on the generic sample as an
// ad hoc jsonl source, the way
// `tailboard internal/source/testdata/generic/sample.jsonl --check` does.
// The sample has one line that is not JSON and one line with no parseable
// time.
func TestRunCheckSample(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AXIOM_DATASET", "")
	const path = "internal/source/testdata/generic/sample.jsonl"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Count(data, []byte("\n"))

	cfg, err := source.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := resolveSource(&cfg, cliOpts{paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := source.Open(cfg, name)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	raw, rep := checkOutput(t, name, src)
	if rep["kind"] != "jsonl" {
		t.Errorf("kind = %v, want jsonl:\n%s", rep["kind"], raw)
	}
	for key, want := range map[string]float64{
		"events":       float64(lines - 1),
		"parse_errors": 1,
		"untimed":      1,
	} {
		if got, _ := rep[key].(float64); got != want {
			t.Errorf("%s = %v, want %v:\n%s", key, rep[key], want, raw)
		}
	}
	for _, key := range []string{"elapsed_ms", "files", "skipped_lines", "unreadable_files", "stats_24h"} {
		if _, ok := rep[key]; !ok {
			t.Errorf("%s key missing:\n%s", key, raw)
		}
	}
}
