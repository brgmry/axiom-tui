package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// specialKeys maps a bound key name to the KeyMsg type bubbletea sends.
var specialKeys = map[string]tea.KeyType{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, " ": tea.KeySpace,
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
	"ctrl+u": tea.KeyCtrlU, "ctrl+d": tea.KeyCtrlD, "ctrl+c": tea.KeyCtrlC,
}

// keyMsgFor builds the KeyMsg bubbletea delivers for a bound key name.
func keyMsgFor(t *testing.T, name string) tea.KeyMsg {
	t.Helper()
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	if kt, ok := specialKeys[name]; ok {
		msg = tea.KeyMsg{Type: kt}
	}
	if msg.String() != name {
		t.Fatalf("key %q builds a message that reads %q", name, msg.String())
	}
	return msg
}

// drain runs a command tree and returns every message it produced. Ticks
// never appear: no key handler schedules one.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// toasts runs cmd, lands each result on m and returns the toasts in order.
func toasts(m Model, cmd tea.Cmd) []string {
	var out []string
	for _, msg := range drain(cmd) {
		next, _ := m.Update(msg)
		m = next.(Model)
		if tm, ok := msg.(toastMsg); ok {
			out = append(out, tm.text)
		} else if hm, ok := msg.(hydrateMsg); ok && hm.purpose == hydrateCopy {
			out = append(out, m.toast)
		}
	}
	return out
}

// hasToast reports whether any toast starts with prefix.
func hasToast(got []string, prefix string) bool {
	for _, s := range got {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// keyModel is a model with 30 events, two top errors and a row carrying a
// trace id at the tail.
func keyModel(t *testing.T) Model {
	t.Helper()
	m := scrollModel(t, 30)
	m.logs.Append([]LogEvent{{ID: "1:99", Time: m.logs.Filtered(m.filter)[29].Time.Add(1),
		Level: "info", Message: "traced.", Fields: map[string]any{"requestId": "abcdef123456"}}})
	m.topErrors = []TableRow{{Count: 3, Level: "error", Message: "boom"}, {Count: 1, Level: "error", Message: "bang"}}
	return m
}

type keyCase struct {
	name    string
	binding func(KeyMap) key.Binding
	setup   func(m *Model)
	check   func(t *testing.T, m Model, cmd tea.Cmd)
}

func keyCases() []keyCase {
	focus := func(f Focus) func(*Model) { return func(m *Model) { m.focus = f } }
	inMode := func(md Mode) func(*Model) { return func(m *Model) { m.mode = md } }
	toastIs := func(prefix string) func(*testing.T, Model, tea.Cmd) {
		return func(t *testing.T, m Model, cmd tea.Cmd) {
			if got := toasts(m, cmd); !hasToast(got, prefix) {
				t.Errorf("toasts %q, want one starting %q", got, prefix)
			}
		}
	}
	level := func(lv string, f func(KeyMap) key.Binding) keyCase {
		return keyCase{"toggle " + lv, f, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if !m.filter.HideLevels[lv] {
				t.Errorf("%s not hidden", lv)
			}
		}}
	}
	input := func(md Mode, f func(KeyMap) key.Binding) keyCase {
		return keyCase{"open prompt", f, func(m *Model) { m.filter.Search = "seed" }, func(t *testing.T, m Model, _ tea.Cmd) {
			ti, _ := textInputFor(md)
			if in := ti.input(&m); m.mode != md || !in.Focused() || in.Value() != *ti.value(&m.filter) {
				t.Errorf("mode %d focused %v value %q", m.mode, in.Focused(), in.Value())
			}
		}}
	}
	return []keyCase{
		{"quit", func(k KeyMap) key.Binding { return k.Quit }, nil, func(t *testing.T, m Model, cmd tea.Cmd) {
			if cmd == nil {
				t.Fatal("no command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Error("does not quit")
			}
		}},
		{"refresh", func(k KeyMap) key.Binding { return k.Refresh }, func(m *Model) { m.queryErr = errors.New("old") },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.queryErr != nil {
					t.Error("stale query error kept")
				}
				var stats bool
				for _, msg := range drain(cmd) {
					_, stats = msg.(statsMsg)
					if stats {
						break
					}
				}
				if !stats || !hasToast(toasts(m, cmd), "refreshed") {
					t.Error("refresh should query stats and toast")
				}
			}},
		{"help", func(k KeyMap) key.Binding { return k.Help }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.mode != ModeHelp {
				t.Errorf("mode %d", m.mode)
			}
		}},
		{"pause", func(k KeyMap) key.Binding { return k.Pause }, nil, func(t *testing.T, m Model, cmd tea.Cmd) {
			if !m.paused || !hasToast(toasts(m, cmd), "paused") {
				t.Error("space should pause")
			}
		}},
		{"resume", func(k KeyMap) key.Binding { return k.Pause }, func(m *Model) { m.paused, m.scrollOff = true, 5 },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.paused || m.scrollOff != 0 || !hasToast(toasts(m, cmd), "resumed") {
					t.Error("space should resume at the tail")
				}
			}},
		{"scroll up", func(k KeyMap) key.Binding { return k.Up }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.scrollOff != 1 || !m.paused {
				t.Errorf("off %d paused %v", m.scrollOff, m.paused)
			}
		}},
		{"error cursor up", func(k KeyMap) key.Binding { return k.Up }, func(m *Model) { m.focus, m.errorCursor = FocusTopErrors, 1 },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.errorCursor != 0 || m.scrollOff != 0 {
					t.Errorf("cursor %d off %d", m.errorCursor, m.scrollOff)
				}
			}},
		{"agents read-only up", func(k KeyMap) key.Binding { return k.Up }, func(m *Model) { m.focus, m.agents = FocusTopRoutes, &fakeAgentSource{} },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.scrollOff != 0 || m.paused {
					t.Error("agents panel should not scroll the stream")
				}
			}},
		{"scroll down", func(k KeyMap) key.Binding { return k.Down }, func(m *Model) { m.scrollOff = 5 }, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.scrollOff != 4 {
				t.Errorf("off %d", m.scrollOff)
			}
		}},
		{"error cursor down", func(k KeyMap) key.Binding { return k.Down }, focus(FocusTopErrors), func(t *testing.T, m Model, _ tea.Cmd) {
			if m.errorCursor != 1 {
				t.Errorf("cursor %d", m.errorCursor)
			}
		}},
		{"agents read-only down", func(k KeyMap) key.Binding { return k.Down },
			func(m *Model) { m.focus, m.agents, m.scrollOff = FocusTopRoutes, &fakeAgentSource{}, 5 },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.scrollOff != 5 {
					t.Error("agents panel should not scroll the stream")
				}
			}},
		{"page up", func(k KeyMap) key.Binding { return k.PageUp }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.scrollOff != 10 || !m.paused {
				t.Errorf("off %d paused %v", m.scrollOff, m.paused)
			}
		}},
		{"page down", func(k KeyMap) key.Binding { return k.PageDown }, func(m *Model) { m.scrollOff = 15 }, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.scrollOff != 5 {
				t.Errorf("off %d", m.scrollOff)
			}
		}},
		{"top", func(k KeyMap) key.Binding { return k.Top }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.scrollOff != 21 || !m.paused {
				t.Errorf("off %d paused %v, want 21 paused", m.scrollOff, m.paused)
			}
		}},
		{"bottom", func(k KeyMap) key.Binding { return k.Bottom }, func(m *Model) { m.scrollOff, m.paused = 5, true },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.scrollOff != 0 || m.paused || !hasToast(toasts(m, cmd), "tailing live") {
					t.Errorf("off %d paused %v", m.scrollOff, m.paused)
				}
			}},
		{"focus next", func(k KeyMap) key.Binding { return k.Tab }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.focus != FocusStats {
				t.Errorf("focus %d", m.focus)
			}
		}},
		{"focus next wraps", func(k KeyMap) key.Binding { return k.FocusRight }, focus(numFocuses - 1), func(t *testing.T, m Model, _ tea.Cmd) {
			if m.focus != FocusLogs {
				t.Errorf("focus %d", m.focus)
			}
		}},
		{"focus prev wraps", func(k KeyMap) key.Binding { return k.FocusLeft }, nil, func(t *testing.T, m Model, _ tea.Cmd) {
			if m.focus != numFocuses-1 {
				t.Errorf("focus %d", m.focus)
			}
		}},
		{"expand", func(k KeyMap) key.Binding { return k.Enter }, nil, func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.mode != ModeExpand || !m.expandLoading || m.expandedLog.Message != "traced." || cmd == nil {
				t.Errorf("mode %d loading %v", m.mode, m.expandLoading)
			}
		}},
		{"expand hydrated", func(k KeyMap) key.Binding { return k.Enter },
			func(m *Model) {
				m.logs.Append([]LogEvent{{ID: "1:100", Time: m.logs.Filtered(m.filter)[30].Time.Add(1), Raw: map[string]any{"a": 1}}})
			},
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.mode != ModeExpand || m.expandLoading || cmd != nil {
					t.Error("an event with its raw object needs no hydrate")
				}
			}},
		{"drill top error", func(k KeyMap) key.Binding { return k.Enter }, func(m *Model) { m.focus, m.errorCursor = FocusTopErrors, 1 },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.filter.Search != "bang" || m.focus != FocusLogs || !hasToast(toasts(m, cmd), "filtered by selected error") {
					t.Errorf("search %q focus %d", m.filter.Search, m.focus)
				}
			}},
		{"enter on agents", func(k KeyMap) key.Binding { return k.Enter }, func(m *Model) { m.focus, m.agents = FocusTopRoutes, &fakeAgentSource{} },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.mode != ModeNormal || cmd != nil {
					t.Error("agents panel has nothing to expand")
				}
			}},
		{"copy row", func(k KeyMap) key.Binding { return k.Yank }, nil, toastIs("copied event to clipboard")},
		{"copy off logs", func(k KeyMap) key.Binding { return k.Yank }, focus(FocusStats), func(t *testing.T, m Model, cmd tea.Cmd) {
			if cmd != nil {
				t.Error("y off the logs panel should do nothing")
			}
		}},
		{"copy in modal", func(k KeyMap) key.Binding { return k.Yank },
			func(m *Model) {
				ev := LogEvent{ID: "x", Raw: map[string]any{"a": 1}}
				m.mode, m.expandedLog = ModeExpand, &ev
			},
			toastIs("copied event to clipboard")},
		level("error", func(k KeyMap) key.Binding { return k.ToggleError }),
		level("warn", func(k KeyMap) key.Binding { return k.ToggleWarn }),
		level("info", func(k KeyMap) key.Binding { return k.ToggleInfo }),
		level("debug", func(k KeyMap) key.Binding { return k.ToggleDebug }),
		input(ModeSearch, func(k KeyMap) key.Binding { return k.Search }),
		input(ModeClient, func(k KeyMap) key.Binding { return k.ClientFilter }),
		input(ModeProvider, func(k KeyMap) key.Binding { return k.ProviderFilter }),
		input(ModeOperation, func(k KeyMap) key.Binding { return k.OperationFilter }),
		{"provider unsupported", func(k KeyMap) key.Binding { return k.ProviderFilter }, func(m *Model) { m.info.Filters.Provider = "" },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.mode != ModeNormal {
					t.Error("p opened a prompt for a source with no provider field")
				}
			}},
		{"operation unsupported", func(k KeyMap) key.Binding { return k.OperationFilter }, func(m *Model) { m.info.Filters.Operation = "" },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.mode != ModeNormal {
					t.Error("o opened a prompt for a source with no operation field")
				}
			}},
		{"trace", func(k KeyMap) key.Binding { return k.TraceRequest }, nil, func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.filter.Trace != "abcdef123456" || !hasToast(toasts(m, cmd), "requestId abcdef12") {
				t.Errorf("trace %q", m.filter.Trace)
			}
		}},
		{"trace without id", func(k KeyMap) key.Binding { return k.TraceRequest }, func(m *Model) { m.scrollOff = 1 },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.filter.Trace != "" || !hasToast(toasts(m, cmd), "no requestId on selected row") {
					t.Errorf("trace %q", m.filter.Trace)
				}
			}},
		{"trace off logs", func(k KeyMap) key.Binding { return k.TraceRequest }, focus(FocusStats), func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.filter.Trace != "" || cmd != nil {
				t.Error("t off the logs panel should do nothing")
			}
		}},
		{"clear filters", func(k KeyMap) key.Binding { return k.ClearFilter },
			func(m *Model) { m.filter.Search, m.filter.HideLevels["info"] = "x", true },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.filter.Search != "" || m.filter.HideLevels["info"] || !hasToast(toasts(m, cmd), "filters cleared") {
					t.Errorf("filter %+v", m.filter)
				}
			}},
		{"reset", func(k KeyMap) key.Binding { return k.Escape },
			func(m *Model) { m.filter.Search, m.paused, m.scrollOff = "x", true, 5 },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.filter.Search != "" || m.paused || m.scrollOff != 0 || !hasToast(toasts(m, cmd), "reset: ") {
					t.Error("esc in normal mode should reset and tail")
				}
			}},
		{"time range", func(k KeyMap) key.Binding { return k.TimeRange }, nil, func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.timeRange != timeRangeOptions[2] || !hasToast(toasts(m, cmd), "time range: 6h") {
				t.Errorf("range %v", m.timeRange)
			}
		}},
		{"source switcher", func(k KeyMap) key.Binding { return k.SourceSwitcher },
			func(m *Model) { m.sources, m.name, m.sourceCursor = []string{"a", "b", "c"}, "b", 0 },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.mode != ModeSourceSwitcher || m.sourceCursor != 1 {
					t.Errorf("mode %d cursor %d", m.mode, m.sourceCursor)
				}
			}},
		{"switcher up", func(k KeyMap) key.Binding { return k.Up },
			func(m *Model) { m.mode, m.sources, m.sourceCursor = ModeSourceSwitcher, []string{"a", "b"}, 1 },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.sourceCursor != 0 || m.scrollOff != 0 {
					t.Errorf("cursor %d off %d", m.sourceCursor, m.scrollOff)
				}
			}},
		{"switcher down", func(k KeyMap) key.Binding { return k.Down },
			func(m *Model) { m.mode, m.sources, m.sourceCursor = ModeSourceSwitcher, []string{"a", "b"}, 0 },
			func(t *testing.T, m Model, _ tea.Cmd) {
				if m.sourceCursor != 1 {
					t.Errorf("cursor %d", m.sourceCursor)
				}
			}},
		{"switcher keeps source", func(k KeyMap) key.Binding { return k.Enter },
			func(m *Model) { m.mode, m.sources, m.sourceCursor = ModeSourceSwitcher, []string{m.name}, 0 },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.mode != ModeNormal || cmd != nil {
					t.Error("enter on the active source should just close")
				}
			}},
		{"switcher selects", func(k KeyMap) key.Binding { return k.Enter },
			func(m *Model) {
				m.cfg.Sources["other"] = SourceConfig{Type: "fake"}
				m.mode, m.sources, m.sourceCursor = ModeSourceSwitcher, []string{m.name, "other"}, 1
			},
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.mode != ModeNormal || m.name != "other" || !hasToast(toasts(m, cmd), "source: other") {
					t.Errorf("mode %d name %q", m.mode, m.name)
				}
			}},
		{"save preset", func(k KeyMap) key.Binding { return k.SavePreset }, func(m *Model) { m.filter.Search = "boom" },
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.presets.Items["1"].Search != "boom" || !hasToast(toasts(m, cmd), "saved as preset 1: /boom") {
					t.Errorf("presets %+v", m.presets.Items)
				}
				if p, err := LoadPresets(); err != nil || p.Items["1"].Search != "boom" {
					t.Errorf("presets on disk %+v, %v", p.Items, err)
				}
			}},
		{"save preset full", func(k KeyMap) key.Binding { return k.SavePreset },
			func(m *Model) {
				for _, s := range DefaultKeyMap().LoadPreset.Keys() {
					m.presets.Items[s] = Preset{Name: s}
				}
			},
			toastIs("all preset slots full")},
		{"help swallows", func(k KeyMap) key.Binding { return k.Quit }, inMode(ModeHelp), func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.mode != ModeHelp || cmd != nil {
				t.Error("keys in the help overlay should do nothing")
			}
		}},
		{"modal swallows", func(k KeyMap) key.Binding { return k.TimeRange }, inMode(ModeExpand), func(t *testing.T, m Model, cmd tea.Cmd) {
			if m.mode != ModeExpand || m.timeRange != timeRangeOptions[1] || cmd != nil {
				t.Error("keys other than y and esc in the modal should do nothing")
			}
		}},
	}
}

// TestEveryKeyBinding presses every key of every binding in the modes
// where it acts, and checks the result.
func TestEveryKeyBinding(t *testing.T) {
	var clip []string
	writeClipboard = func(s string) error { clip = append(clip, s); return nil }
	defer func() { writeClipboard = clipboard.WriteAll }()
	sourceOpeners["fake"] = func(name string, sc SourceConfig) (Source, error) {
		return &fakeSource{info: SourceInfo{Name: name, Kind: "fake"}}, nil
	}
	defer delete(sourceOpeners, "fake")

	covered := map[string]bool{}
	for _, c := range keyCases() {
		b := c.binding(DefaultKeyMap())
		covered[b.Help().Key] = true
		for _, k := range b.Keys() {
			t.Run(c.name+"/"+k, func(t *testing.T) {
				m := keyModel(t)
				if c.setup != nil {
					c.setup(&m)
				}
				next, cmd := m.Update(keyMsgFor(t, k))
				c.check(t, next.(Model), cmd)
			})
		}
	}
	// Load preset has a case per slot below.
	covered[DefaultKeyMap().LoadPreset.Help().Key] = true
	for _, b := range DefaultKeyMap().All() {
		if !covered[b.Help().Key] {
			t.Errorf("binding %q has no case", b.Help().Key)
		}
	}
	if len(clip) == 0 {
		t.Error("no copy case reached the clipboard")
	}
}

// TestLoadPresetKeys: each of 1-9 loads its slot and an empty slot is a
// no-op.
func TestLoadPresetKeys(t *testing.T) {
	for _, slot := range DefaultKeyMap().LoadPreset.Keys() {
		m := keyModel(t)
		m.scrollOff = 5
		m.presets.Items[slot] = Preset{Name: "p" + slot, Search: "s" + slot, HideLevels: []string{"warn"}}
		next, cmd := m.Update(keyMsgFor(t, slot))
		got := next.(Model)
		if got.filter.Search != "s"+slot || !got.filter.HideLevels["warn"] || got.scrollOff != 0 {
			t.Errorf("slot %s: filter %+v off %d", slot, got.filter, got.scrollOff)
		}
		if ts := toasts(got, cmd); !hasToast(ts, "preset "+slot+": p"+slot) {
			t.Errorf("slot %s: toasts %q", slot, ts)
		}

		empty := keyModel(t)
		next, cmd = empty.Update(keyMsgFor(t, slot))
		if next.(Model).filter.Search != "" || cmd != nil {
			t.Errorf("slot %s: an empty slot changed the filter", slot)
		}
	}
}

// TestPromptKeys: in each prompt, typed keys edit the input instead of
// acting, enter commits the value to the filter and esc closes without
// committing.
func TestPromptKeys(t *testing.T) {
	for _, ti := range textInputs {
		m := keyModel(t)
		next, _ := m.Update(keyMsgFor(t, ti.binding(m.keys).Keys()[0]))
		m = next.(Model)
		for _, k := range []string{"e", "q", "/", "T"} {
			next, _ = m.Update(keyMsgFor(t, k))
			m = next.(Model)
		}
		if m.mode != ti.mode || m.filter.HideLevels["error"] || m.timeRange != timeRangeOptions[1] {
			t.Fatalf("mode %d: typed keys acted instead of editing", ti.mode)
		}
		if got := ti.input(&m).Value(); got != "eq/T" {
			t.Errorf("mode %d: input %q, want eq/T", ti.mode, got)
		}

		closed, _ := m.Update(keyMsgFor(t, "esc"))
		cm := closed.(Model)
		if cm.mode != ModeNormal || *ti.value(&cm.filter) != "" {
			t.Errorf("mode %d: esc should close without committing", ti.mode)
		}
		for _, other := range textInputs {
			if other.input(&cm).Focused() {
				t.Errorf("mode %d: esc left prompt %d focused", ti.mode, other.mode)
			}
		}

		next, _ = m.Update(keyMsgFor(t, "enter"))
		m = next.(Model)
		if m.mode != ModeNormal || *ti.value(&m.filter) != "eq/T" || ti.input(&m).Focused() {
			t.Errorf("mode %d: enter should commit eq/T, got %q", ti.mode, *ti.value(&m.filter))
		}
	}
}

// TestModalEscape: esc closes the expand modal, help and the switcher.
func TestModalEscape(t *testing.T) {
	for _, md := range []Mode{ModeExpand, ModeHelp, ModeSourceSwitcher} {
		m := keyModel(t)
		ev := LogEvent{ID: "x"}
		m.mode, m.expandedLog, m.expandLoading = md, &ev, true
		next, cmd := m.Update(keyMsgFor(t, "esc"))
		got := next.(Model)
		if got.mode != ModeNormal || got.expandedLog != nil || got.expandLoading || cmd != nil {
			t.Errorf("mode %d: esc left mode %d", md, got.mode)
		}
	}
}

// TestCopyFailureToast: a clipboard that cannot be written says so.
func TestCopyFailureToast(t *testing.T) {
	writeClipboard = func(string) error { return errors.New("no pbcopy") }
	defer func() { writeClipboard = clipboard.WriteAll }()
	m := keyModel(t)
	_, cmd := m.Update(keyMsgFor(t, "y"))
	if got := toasts(m, cmd); !hasToast(got, "copy failed: no pbcopy") {
		t.Errorf("toasts %q", got)
	}
}
