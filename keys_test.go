package main

import tea "github.com/charmbracelet/bubbletea"

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
