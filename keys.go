package main

import "github.com/charmbracelet/bubbles/key"

// KeyMap names every binding the app uses. Defined in one place so the help
// overlay and actual handlers can't drift — both read this struct.
type KeyMap struct {
	Quit     key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Pause    key.Binding
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding
	Tab        key.Binding
	FocusLeft  key.Binding
	FocusRight key.Binding
	Enter      key.Binding
	Yank       key.Binding
	Escape     key.Binding

	ToggleError key.Binding
	ToggleWarn  key.Binding
	ToggleInfo  key.Binding

	Search       key.Binding
	ClientFilter key.Binding
	ClearFilter  key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "force refresh"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Pause: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "pause/resume"),
		),
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k/↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j/↓", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn", "page down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "bottom / resume tail"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "cycle focus"),
		),
		FocusLeft: key.NewBinding(
			key.WithKeys("h", "left"),
			key.WithHelp("h/←", "focus left"),
		),
		FocusRight: key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("l/→", "focus right"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "expand / activate"),
		),
		Yank: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy to clipboard"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close / clear"),
		),
		ToggleError: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "toggle errors"),
		),
		ToggleWarn: key.NewBinding(
			key.WithKeys("w"),
			key.WithHelp("w", "toggle warns"),
		),
		ToggleInfo: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "toggle infos"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search message"),
		),
		ClientFilter: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "filter by client"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("C", "R"),
			key.WithHelp("esc/R", "reset filters + tail"),
		),
	}
}
