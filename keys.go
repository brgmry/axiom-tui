package main

import "github.com/charmbracelet/bubbles/key"

// KeyMap names every binding the app uses. Defined in one place so the help
// overlay and actual handlers can't drift, both read this struct. Each
// binding's help key lists every key it is bound to, so the overlay shows
// exactly what works.
type KeyMap struct {
	Quit       key.Binding
	Refresh    key.Binding
	Help       key.Binding
	Pause      key.Binding
	Up         key.Binding
	Down       key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	Top        key.Binding
	Bottom     key.Binding
	Tab        key.Binding
	FocusLeft  key.Binding
	FocusRight key.Binding
	Enter      key.Binding
	Yank       key.Binding
	Escape     key.Binding

	ToggleError key.Binding
	ToggleWarn  key.Binding
	ToggleInfo  key.Binding
	ToggleDebug key.Binding

	Search          key.Binding
	ClientFilter    key.Binding
	ProviderFilter  key.Binding
	OperationFilter key.Binding
	TraceRequest    key.Binding
	ClearFilter     key.Binding

	TimeRange      key.Binding
	SourceSwitcher key.Binding
	SavePreset     key.Binding
	LoadPreset     key.Binding
}

// All returns every binding, in help overlay order.
func (k KeyMap) All() []key.Binding {
	return []key.Binding{
		k.Down, k.Up, k.PageDown, k.PageUp, k.Top, k.Bottom,
		k.Tab, k.FocusRight, k.FocusLeft, k.Pause,
		k.ToggleError, k.ToggleWarn, k.ToggleInfo, k.ToggleDebug,
		k.Search, k.ClientFilter, k.ProviderFilter, k.OperationFilter, k.TraceRequest,
		k.ClearFilter, k.Escape,
		k.TimeRange, k.SourceSwitcher, k.SavePreset, k.LoadPreset,
		k.Enter, k.Yank, k.Refresh, k.Help, k.Quit,
	}
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q / ctrl+c", "quit"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "force refresh all queries"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "this help"),
		),
		Pause: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "pause / resume stream"),
		),
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k / ↑", "scroll up (older)"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j / ↓", "scroll down (newer)"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup / ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn / ctrl+d", "page down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g / home", "oldest page"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G / end", "tail (resumes the stream)"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "cycle focus forward"),
		),
		FocusLeft: key.NewBinding(
			key.WithKeys("h", "left"),
			key.WithHelp("h / ←", "focus previous panel"),
		),
		FocusRight: key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("l / →", "focus next panel"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "expand log line / drill into top error"),
		),
		Yank: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy selected or expanded event as JSON"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close modal; else reset filters + resume tail"),
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
		ToggleDebug: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "toggle debug"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search messages (substring)"),
		),
		ClientFilter: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "filter by group"),
		),
		ProviderFilter: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "filter by provider"),
		),
		OperationFilter: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "filter by operation"),
		),
		TraceRequest: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "trace by field (on focused row)"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("R", "C"),
			key.WithHelp("R / C", "reset filters"),
		),
		TimeRange: key.NewBinding(
			key.WithKeys("T"),
			key.WithHelp("T", "cycle time range (15m / 1h / 6h / 24h)"),
		),
		SourceSwitcher: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "source switcher"),
		),
		SavePreset: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "save filter as preset (next slot)"),
		),
		LoadPreset: key.NewBinding(
			key.WithKeys("1", "2", "3", "4", "5", "6", "7", "8", "9"),
			key.WithHelp("1-9", "load preset by slot"),
		),
	}
}
