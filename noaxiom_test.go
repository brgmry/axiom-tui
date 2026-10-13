//go:build noaxiom

package main

import (
	"strings"
	"testing"

	"github.com/brgmry/tailboard/internal/source"
)

// TestNoaxiomConfig: in a noaxiom build an axiom source fails at load, as
// any unusable type does, with an error that says why rather than calling
// the type unknown.
func TestNoaxiomConfig(t *testing.T) {
	for _, text := range []string{
		"[sources.logs]\ntype = \"axiom\"\n",
		"[datasets.logs]\n",
		"default_source = \"app\"\n[sources.app]\ntype = \"jsonl\"\npaths = \"x.jsonl\"\n[sources.logs]\ntype = \"axiom\"\n",
	} {
		_, err := source.ParseConfig(text)
		if err == nil || !strings.Contains(err.Error(), noaxiomReason) || !strings.Contains(err.Error(), `"logs"`) {
			t.Errorf("%q: err = %v, want one naming logs and saying %q", text, err, noaxiomReason)
		}
	}
	if _, err := source.ParseConfig("[sources.app]\ntype = \"jsonl\"\npaths = \"x.jsonl\"\n"); err != nil {
		t.Errorf("a config without axiom sources should load: %v", err)
	}
}

// TestNoaxiomDataset: every way of asking for a dataset resolves as before
// and then fails to open with the noaxiom reason.
func TestNoaxiomDataset(t *testing.T) {
	asChildMain()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AXIOM_DATASET", "")
	for _, tc := range []struct {
		name, text string
		opts       cliOpts
	}{
		{"flag", "", cliOpts{dataset: "logs"}},
		{"default_dataset", "default_dataset = \"logs\"\n", cliOpts{}},
	} {
		cfg, err := source.ParseConfig(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		name, err := resolveSource(&cfg, tc.opts)
		if err != nil || name != "logs" {
			t.Fatalf("%s: resolve = %q, %v", tc.name, name, err)
		}
		if _, err := source.Open(cfg, name); err == nil || !strings.Contains(err.Error(), noaxiomReason) {
			t.Errorf("%s: open err = %v, want %q", tc.name, err, noaxiomReason)
		}
	}

	out, code := runMain(t, "--env", "/dev/null", "--dataset", "logs", "--check")
	if code != 1 || !strings.Contains(out, noaxiomReason) {
		t.Errorf("--dataset: exit %d, output %q; want 1 with %q", code, out, noaxiomReason)
	}
}
