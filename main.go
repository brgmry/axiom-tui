// Command tailboard is a terminal dashboard for log streams.
//
// Renders a live log stream with filtering, aggregate panels (stats,
// throughput, error trend), and interactive drill-in, all keyboard
// driven. Sources are JSONL files or folders, pi agent sessions, or Axiom
// datasets, configured per source via ~/.config/tailboard/config.toml.
//
// Usage:
//
//	tailboard [--source NAME | --dataset NAME | PATH...] [--config PATH] [--interval N]
//
// Env (Axiom sources only):
//
//	AXIOM_TOKEN (or AXIOM_PAT / AXIOM_API_TOKEN), required
//	AXIOM_ORG_ID, for personal tokens
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
)

// cliOpts is what main collects from flags and arguments before resolving
// which source to open. Kept as a struct so the resolution is testable.
type cliOpts struct {
	dataset string
	source  string
	paths   []string
}

func usage() {
	fmt.Fprintln(os.Stderr, "tailboard, live dashboard for any log stream")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  tailboard [flags] [PATH ...]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  PATH is a .jsonl file, a directory or a glob; it opens an ad hoc jsonl source.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags:")
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Env (Axiom sources):")
	fmt.Fprintln(os.Stderr, "  AXIOM_TOKEN / AXIOM_PAT / AXIOM_API_TOKEN    (required)")
	fmt.Fprintln(os.Stderr, "  AXIOM_ORG_ID                                 (required for personal tokens)")
	fmt.Fprintln(os.Stderr, "  AXIOM_DATASET                                (fallback for --dataset)")
}

func main() {
	var (
		datasetFlag  = flag.String("dataset", "", "Axiom dataset name (an Axiom source, ad hoc when not configured)")
		sourceFlag   = flag.String("source", "", "Source name from config, or a .jsonl file or directory")
		configFlag   = flag.String("config", "", "Path to TOML config (defaults to ~/.config/tailboard/config.toml)")
		intervalFlag = flag.Int("interval", 0, "Refresh interval in seconds (overrides config)")
		envFileFlag  = flag.String("env", "", "Source KEY=value pairs from a file before startup")
	)
	flag.Usage = usage
	flag.Parse()

	// Best-effort env sourcing: CLI flag wins, then common shared-env spots.
	if *envFileFlag != "" {
		LoadEnvFile(*envFileFlag)
	} else {
		home, _ := os.UserHomeDir()
		for _, p := range []string{
			filepath.Join(home, "dev", ".env.shared"),
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

	name, err := resolveSource(&cfg, cliOpts{
		dataset: *datasetFlag,
		source:  *sourceFlag,
		paths:   flag.Args(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if name == "" {
			fmt.Fprintln(os.Stderr)
			usage()
		}
		os.Exit(1)
	}

	src, err := OpenSource(cfg, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "source:", err)
		os.Exit(1)
	}

	// bubblezone needs to track terminal regions before any rendering: this
	// global is what View() wraps panels with via zone.Mark and what Update
	// queries via zone.Get to map mouse clicks back to regions.
	zone.NewGlobal()
	defer zone.Close()

	model := NewModel(cfg, name, src)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	_ = src.Close()
}

// resolveSource picks the source to open, registering ad hoc sources in
// cfg as needed. Order: --dataset; --source; positional paths;
// default_source; default_dataset; $AXIOM_DATASET; the only user-configured
// source; the built-in pi when its sessions directory exists. The returned
// name is "" when nothing applies (the caller prints usage).
func resolveSource(cfg *Config, o cliOpts) (string, error) {
	if o.dataset != "" {
		cfg.AddAxiomSource(o.dataset)
		if sc := cfg.Sources[o.dataset]; sc.Type != "axiom" {
			return o.dataset, fmt.Errorf("--dataset %q names a configured %s source, not an Axiom dataset", o.dataset, sc.Type)
		}
		return o.dataset, nil
	}
	if o.source != "" {
		if _, ok := cfg.Sources[o.source]; ok {
			return o.source, nil
		}
		if fi, err := os.Stat(expandHome(o.source)); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			cfg.AddFileSource(o.source, []string{o.source})
			return o.source, nil
		}
		return o.source, fmt.Errorf("unknown source %q; known sources: %s", o.source, strings.Join(cfg.SourceNames(), ", "))
	}
	if len(o.paths) > 0 {
		name := o.paths[0]
		cfg.AddFileSource(name, o.paths)
		return name, nil
	}
	if n := cfg.DefaultSource; n != "" {
		if _, ok := cfg.Sources[n]; !ok {
			return n, fmt.Errorf("default_source %q is not a configured source; known sources: %s", n, strings.Join(cfg.SourceNames(), ", "))
		}
		return n, nil
	}
	if n := cfg.DefaultDataset; n != "" {
		cfg.AddAxiomSource(n)
		return n, nil
	}
	if n := os.Getenv("AXIOM_DATASET"); n != "" {
		cfg.AddAxiomSource(n)
		return n, nil
	}
	if names := cfg.UserSourceNames(); len(names) == 1 {
		return names[0], nil
	}
	if fi, err := os.Stat(expandHome(defaultPiPaths)); err == nil && fi.IsDir() {
		if _, ok := cfg.Sources[builtinPiName]; ok {
			return builtinPiName, nil
		}
	}
	return "", fmt.Errorf("no source: pass --source, --dataset or a path, or set default_source in config")
}
