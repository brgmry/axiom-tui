package main

import (
	"hash/fnv"

	"github.com/charmbracelet/lipgloss"
)

// Theme holds every style the app uses. One struct so colors can be swapped in
// one place later (e.g. a --theme=light flag).
type Theme struct {
	Border        lipgloss.Style
	BorderFocused lipgloss.Style
	PanelTitle    lipgloss.Style
	StatusBar     lipgloss.Style
	StatusKey     lipgloss.Style
	StatusFilter  lipgloss.Style
	StatusError   lipgloss.Style
	LevelInfo     lipgloss.Style
	LevelWarn     lipgloss.Style
	LevelError    lipgloss.Style
	LevelDebug    lipgloss.Style
	TimeDim       lipgloss.Style
	Fields        lipgloss.Style
	FieldKey      lipgloss.Style
	Selected      lipgloss.Style
	InputPrompt   lipgloss.Style
	InputActive   lipgloss.Style
	Modal         lipgloss.Style
	ModalKey      lipgloss.Style
}

func NewTheme() Theme {
	cyan := lipgloss.Color("#00e5ff")
	yellow := lipgloss.Color("#ffcc00")
	red := lipgloss.Color("#ff4757")
	green := lipgloss.Color("#7bed9f")
	grey := lipgloss.Color("#747d8c")
	white := lipgloss.Color("#f1f2f6")
	bg := lipgloss.Color("#1e2028")

	return Theme{
		Border: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(cyan),
		BorderFocused: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(yellow),
		PanelTitle: lipgloss.NewStyle().
			Foreground(cyan).
			Bold(true),
		StatusBar: lipgloss.NewStyle().
			Background(bg).
			Foreground(white),
		StatusKey: lipgloss.NewStyle().
			Foreground(cyan).
			Bold(true),
		StatusFilter: lipgloss.NewStyle().
			Foreground(yellow),
		StatusError: lipgloss.NewStyle().
			Foreground(red),
		LevelInfo: lipgloss.NewStyle().
			Foreground(green).
			Bold(true),
		LevelWarn: lipgloss.NewStyle().
			Foreground(yellow).
			Bold(true),
		LevelError: lipgloss.NewStyle().
			Foreground(red).
			Bold(true),
		LevelDebug: lipgloss.NewStyle().
			Foreground(grey),
		TimeDim: lipgloss.NewStyle().
			Foreground(grey),
		Fields: lipgloss.NewStyle().
			Foreground(grey),
		FieldKey: lipgloss.NewStyle().
			Foreground(cyan),
		Selected: lipgloss.NewStyle().
			Background(lipgloss.Color("#2f3542")).
			Foreground(white),
		InputPrompt: lipgloss.NewStyle().
			Foreground(cyan).
			Bold(true),
		InputActive: lipgloss.NewStyle().
			Foreground(white).
			Background(lipgloss.Color("#2f3542")),
		Modal: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cyan).
			Padding(1, 2),
		ModalKey: lipgloss.NewStyle().
			Foreground(yellow).
			Bold(true),
	}
}

// clientPalette is a curated set of readable colors for per-client
// color-coding. Picked to stay readable on a dark bg, no overlap with level
// colors (green/yellow/red), and visually distinct from each other.
var clientPalette = []lipgloss.Color{
	lipgloss.Color("#00bcd4"), // cyan
	lipgloss.Color("#a29bfe"), // lavender
	lipgloss.Color("#fdcb6e"), // gold
	lipgloss.Color("#55efc4"), // mint
	lipgloss.Color("#fab1a0"), // peach
	lipgloss.Color("#74b9ff"), // sky
	lipgloss.Color("#e17055"), // terracotta
	lipgloss.Color("#6c5ce7"), // indigo
	lipgloss.Color("#ffeaa7"), // sand
	lipgloss.Color("#81ecec"), // aqua
	lipgloss.Color("#dfe6e9"), // platinum
	lipgloss.Color("#f8a5c2"), // rose
}

// ColorForClient hashes a client name into a stable palette color. Same name
// always yields the same color across sessions — deterministic is more useful
// than "prettier" here because you learn to associate colors with clients.
func ColorForClient(name string) lipgloss.Style {
	if name == "" {
		return lipgloss.NewStyle()
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	return lipgloss.NewStyle().Foreground(clientPalette[int(h.Sum32())%len(clientPalette)])
}

// LevelStyle returns the lipgloss style for a log level string. Unknown levels
// fall through to plain text.
func (t Theme) LevelStyle(level string) lipgloss.Style {
	switch level {
	case "error", "ERROR":
		return t.LevelError
	case "warn", "WARN":
		return t.LevelWarn
	case "info", "INFO":
		return t.LevelInfo
	case "debug", "DEBUG":
		return t.LevelDebug
	}
	return lipgloss.NewStyle()
}
