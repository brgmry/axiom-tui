package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyPress builds a KeyMsg for a named key ("enter", "d", "esc").
func keyPress(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// helpName is how a bound key is written in a help key.
var helpName = map[string]string{
	" ": "space", "up": "↑", "down": "↓", "left": "←", "right": "→", "pgdown": "pgdn",
}

// TestHelpListsBoundKeys: every binding is in All, every key a binding
// matches is named in its help key, and every help key is in the overlay.
func TestHelpListsBoundKeys(t *testing.T) {
	km := DefaultKeyMap()
	if n := reflect.TypeOf(km).NumField(); n != len(km.All()) {
		t.Fatalf("KeyMap has %d bindings, All returns %d", n, len(km.All()))
	}

	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 120, 40, true
	m.mode = ModeHelp
	help := stripANSI(m.View())

	for _, b := range km.All() {
		h := b.Help().Key
		names := map[string]bool{}
		for _, n := range strings.Split(h, " / ") {
			names[n] = true
		}
		for _, k := range b.Keys() {
			if len(k) == 1 && k >= "1" && k <= "9" {
				if !names["1-9"] {
					t.Errorf("binding %q: key %q is not in its help key", h, k)
				}
				continue
			}
			n := k
			if alias, ok := helpName[k]; ok {
				n = alias
			}
			if !names[n] {
				t.Errorf("binding %q: key %q is not in its help key", h, k)
			}
		}
		if !strings.Contains(help, h) {
			t.Errorf("help overlay does not list %q", h)
		}
	}
	if strings.Contains(help, "shift-tab") {
		t.Error("help lists shift-tab, which is not bound")
	}
	if rows := rowsOf(help); rows > 40 {
		t.Errorf("help overlay is %d rows at 120x40", rows)
	}
}
