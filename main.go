// Command tailboard is a terminal dashboard for log streams.
//
// Renders a live log stream with filtering, aggregate panels (stats,
// throughput, error trend), and interactive drill-in, all keyboard
// driven. Sources are JSONL files or folders, pi agent sessions, or Axiom
// datasets, configured per source via ~/.config/tailboard/config.toml.
//
// Usage:
//
//	tailboard [--source NAME | --dataset NAME | PATH...] [--config PATH] [--interval N] [--env FILE] [--check]
//
// Env (Axiom sources only):
//
//	AXIOM_TOKEN (or AXIOM_PAT / AXIOM_API_TOKEN), required
//	AXIOM_ORG_ID, for personal tokens
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

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

// usage prints the help text, with fs's flag defaults.
func usage(fs *flag.FlagSet) {
	fmt.Fprintln(os.Stderr, "tailboard, live dashboard for any log stream")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  tailboard [flags] [PATH ...]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  PATH is a .jsonl file, a directory or a glob; it opens an ad hoc jsonl source.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags:")
	fs.PrintDefaults()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Env (Axiom sources):")
	fmt.Fprintln(os.Stderr, "  AXIOM_TOKEN / AXIOM_PAT / AXIOM_API_TOKEN    (required)")
	fmt.Fprintln(os.Stderr, "  AXIOM_ORG_ID                                 (required for personal tokens)")
	fmt.Fprintln(os.Stderr, "  AXIOM_DATASET                                (fallback for --dataset)")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Read from the process env, or from a file passed with --env FILE.")
}

// cliFlags holds the parsed command-line flags.
type cliFlags struct {
	dataset  string
	source   string
	config   string
	interval int
	envFile  string
	check    bool
}

// newFlagSet declares tailboard's flags on a fresh FlagSet bound to a
// cliFlags. The set is also what reorderArgs reads to tell value flags
// from bool flags.
func newFlagSet() (*flag.FlagSet, *cliFlags) {
	f := &cliFlags{}
	fs := flag.NewFlagSet("tailboard", flag.ContinueOnError)
	fs.StringVar(&f.dataset, "dataset", "", "Axiom dataset name (an Axiom source, ad hoc when not configured)")
	fs.StringVar(&f.source, "source", "", "Source name from config, or a .jsonl file or directory")
	fs.StringVar(&f.config, "config", "", "Path to TOML config (defaults to ~/.config/tailboard/config.toml)")
	fs.IntVar(&f.interval, "interval", 0, "Refresh interval in seconds (overrides config)")
	fs.StringVar(&f.envFile, "env", "", "Load KEY=value pairs from `FILE` before startup; no env file is read otherwise")
	fs.BoolVar(&f.check, "check", false, "Headless: open the source, print a JSON summary and exit")
	fs.Usage = func() { usage(fs) }
	return fs, f
}

func main() {
	fs, flags := newFlagSet()
	if err := fs.Parse(reorderArgs(fs, os.Args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2)
	}

	// Only an explicit --env file is sourced; nothing is read implicitly.
	if flags.envFile != "" {
		LoadEnvFile(flags.envFile)
	}

	cfg, err := LoadConfig(flags.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	if flags.interval > 0 {
		cfg.RefreshSeconds = flags.interval
	}

	name, err := resolveSource(&cfg, cliOpts{
		dataset: flags.dataset,
		source:  flags.source,
		paths:   fs.Args(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if name == "" {
			fmt.Fprintln(os.Stderr)
			usage(fs)
		}
		os.Exit(1)
	}

	src, err := OpenSource(cfg, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "source:", err)
		os.Exit(1)
	}

	if flags.check {
		if err := runCheck(os.Stdout, name, src); err != nil {
			fmt.Fprintln(os.Stderr, "check:", err)
			os.Exit(1)
		}
		_ = src.Close()
		return
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

// reorderArgs moves flags in front of positional arguments so that
// `tailboard app.jsonl --check` works like `tailboard --check app.jsonl`.
// A value flag of fs consumes the next argument unless written as
// --flag=value; bool and unknown flags consume nothing. Everything after
// "--" is positional.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	takesValue := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		bf, ok := f.Value.(interface{ IsBoolFlag() bool })
		takesValue[f.Name] = !ok || !bf.IsBoolFlag()
	})
	flags := []string{}
	positional := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Keep the terminator so a path that starts with "-" still
			// parses as positional.
			flags = append(flags, "--")
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || !takesValue[name] {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, positional...)
}

// resolveSource picks the source to open, registering ad hoc sources in
// cfg as needed. Order: --dataset; --source; positional paths;
// default_source; default_dataset; $AXIOM_DATASET; the only user-configured
// source; the built-in pi when its sessions directory exists. The returned
// name is "" when nothing applies (the caller prints usage).
func resolveSource(cfg *Config, o cliOpts) (string, error) {
	if o.dataset != "" {
		// --dataset always means an Axiom dataset of that name.
		cfg.AddAxiomSource(o.dataset)
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

// checkReport is the JSON printed by --check. Keys are stable.
type checkReport struct {
	Source          string     `json:"source"`
	Kind            string     `json:"kind"`
	Files           int        `json:"files"`
	Events          int64      `json:"events"`
	SkippedLines    int64      `json:"skipped_lines"`
	Untimed         int64      `json:"untimed"`
	ParseErrors     int64      `json:"parse_errors"`
	UnreadableFiles int64      `json:"unreadable_files"`
	ElapsedMs       int64      `json:"elapsed_ms"`
	Stats24h        checkStats `json:"stats_24h"`
	Cost            *checkCost `json:"cost,omitempty"`
	// Agents is present, possibly empty, exactly when the source is an
	// AgentSource. A nil pointer is omitted; an empty slice prints [].
	Agents *[]checkAgent `json:"agents,omitempty"`
}

type checkStats struct {
	Total int64 `json:"total"`
	Info  int64 `json:"info"`
	Warn  int64 `json:"warn"`
	Error int64 `json:"error"`
}

type checkCost struct {
	Today        float64 `json:"today"`
	TrailingHour float64 `json:"trailing_hour"`
	Total24h     float64 `json:"total_24h"`
}

type checkAgent struct {
	Name          string  `json:"name"`
	ContextTokens int64   `json:"context_tokens"`
	Compactions   int     `json:"compactions"`
	ToolCalls     int     `json:"tool_calls"`
	ToolErrors    int     `json:"tool_errors"`
	OrphanCalls   int     `json:"orphan_calls"`
	SpendToday    float64 `json:"spend_today"`
	TopModel      string  `json:"top_model"`
}

// roundMoney rounds a dollar figure to four decimals so --check prints
// 0.3, not the float64 sum 0.30000000000000004.
func roundMoney(x float64) float64 { return math.Round(x*1e4) / 1e4 }

// runCheck opens nothing itself: it waits for a file source's first full
// pass (or runs one Stats call for Axiom), then prints the summary.
func runCheck(w io.Writer, name string, src Source) error {
	start := time.Now()
	info := src.Info()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var prog Progress
	if ps, ok := src.(ProgressSource); ok {
		for {
			prog = ps.Progress()
			if !prog.Loading {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for backfill (%d/%d files)", prog.FilesDone, prog.FilesTotal)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}

	stats, err := src.Stats(ctx, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("stats: %w", err)
	}
	rep := checkReport{
		Source:          name,
		Kind:            info.Kind,
		Files:           prog.FilesTotal,
		Events:          prog.Events,
		SkippedLines:    prog.SkippedLines,
		Untimed:         prog.Untimed,
		ParseErrors:     prog.ParseErrors,
		UnreadableFiles: prog.UnreadableFiles,
		Stats24h: checkStats{
			Total: stats.Total, Info: stats.Info, Warn: stats.Warn, Error: stats.Error,
		},
	}
	if info.Caps.Has(CapCost) {
		cost, err := src.Cost(ctx, 24*time.Hour)
		if err != nil {
			return fmt.Errorf("cost: %w", err)
		}
		rep.Cost = &checkCost{Today: roundMoney(cost.Today), TrailingHour: roundMoney(cost.TrailingHour), Total24h: roundMoney(cost.Total)}
	}
	if as, ok := src.(AgentSource); ok {
		agents, err := as.Agents(ctx, 24*time.Hour)
		if err != nil {
			return fmt.Errorf("agents: %w", err)
		}
		list := make([]checkAgent, 0, len(agents))
		for _, a := range agents {
			list = append(list, checkAgent{
				Name:          a.Name,
				ContextTokens: a.ContextTokens,
				Compactions:   a.Compactions,
				ToolCalls:     a.ToolCalls,
				ToolErrors:    a.ToolErrors,
				OrphanCalls:   a.OrphanCalls,
				SpendToday:    roundMoney(a.SpendToday),
				TopModel:      a.TopModel,
			})
		}
		rep.Agents = &list
	}
	rep.ElapsedMs = time.Since(start).Milliseconds()

	enc := json.NewEncoder(w)
	return enc.Encode(rep)
}
