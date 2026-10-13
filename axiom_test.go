//go:build !noaxiom

package main

import (
	"strings"
	"testing"

	"github.com/brgmry/tailboard/internal/source"
)

// TestAxiomLinked: a default build registers the axiom type, so --dataset
// gets as far as the client's own token check.
func TestAxiomLinked(t *testing.T) {
	for _, k := range []string{"AXIOM_PAT", "AXIOM_PERSONAL_TOKEN", "AXIOM_TOKEN", "AXIOM_API_TOKEN"} {
		t.Setenv(k, "")
	}
	if _, err := source.ParseConfig("[sources.logs]\ntype = \"axiom\"\n"); err != nil {
		t.Fatalf("axiom source should load: %v", err)
	}
	cfg := source.Defaults()
	name, err := resolveSource(&cfg, cliOpts{dataset: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Open(cfg, name); err == nil || !strings.Contains(err.Error(), "AXIOM_PAT") {
		t.Errorf("open err = %v, want the missing token error", err)
	}
}
