// Command axiom-tui is a terminal dashboard for Axiom datasets.
//
// Renders a live log stream with filtering, aggregate panels (stats,
// throughput, error trend), and interactive drill-in — all keyboard
// driven. Configurable per-dataset via ~/.config/axiom-tui/config.toml.
//
// Usage:
//
//	axiom-tui [--dataset NAME] [--config PATH] [--interval N]
//
// Env:
//
//	AXIOM_TOKEN (or AXIOM_PAT / AXIOM_API_TOKEN) — required
//	AXIOM_ORG_ID — for personal tokens
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	var (
		datasetFlag  = flag.String("dataset", "", "Axiom dataset name (falls back to config.default_dataset then $AXIOM_DATASET)")
		configFlag   = flag.String("config", "", "Path to TOML config (defaults to ~/.config/axiom-tui/config.toml)")
		intervalFlag = flag.Int("interval", 0, "Refresh interval in seconds (overrides config)")
		envFileFlag  = flag.String("env", "", "Source KEY=value pairs from a file before startup")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "axiom-tui — live dashboard for any Axiom dataset")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  axiom-tui [flags]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Env:")
		fmt.Fprintln(os.Stderr, "  AXIOM_TOKEN / AXIOM_PAT / AXIOM_API_TOKEN    (required)")
		fmt.Fprintln(os.Stderr, "  AXIOM_ORG_ID                                 (required for personal tokens)")
		fmt.Fprintln(os.Stderr, "  AXIOM_DATASET                                (fallback for --dataset)")
	}
	flag.Parse()

	// Best-effort env sourcing: CLI flag wins, then common shared-env spots.
	if *envFileFlag != "" {
		LoadEnvFile(*envFileFlag)
	} else {
		home, _ := os.UserHomeDir()
		for _, p := range []string{
			filepath.Join(home, "Documents", ".env.shared"),
			filepath.Join(home, ".env"),
		} {
			LoadEnvFile(p)
		}
	}

	cfg, err := LoadConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	if *intervalFlag > 0 {
		cfg.RefreshSeconds = *intervalFlag
	}

	dataset := *datasetFlag
	if dataset == "" {
		dataset = cfg.DefaultDataset
	}
	if dataset == "" {
		dataset = os.Getenv("AXIOM_DATASET")
	}
	if dataset == "" {
		fmt.Fprintln(os.Stderr, "no dataset: pass --dataset, set default_dataset in config, or export AXIOM_DATASET")
		os.Exit(1)
	}

	ax, err := NewAxiomClient(dataset, cfg.DatasetOrDefault(dataset))
	if err != nil {
		fmt.Fprintln(os.Stderr, "axiom:", err)
		os.Exit(1)
	}

	model := NewModel(cfg, dataset, ax)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}
