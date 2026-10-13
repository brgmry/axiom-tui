package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestTextWidth: every helper measures terminal cells. CJK and emoji take
// two cells each, ANSI escapes take none.
func TestTextWidth(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff4757")).Bold(true).Render("error")
	for _, tc := range []struct {
		name string
		s    string
		w    int
	}{
		{"ascii", "hello", 5},
		{"cjk", "日本語", 6},
		{"emoji", "ok 🔥", 5},
		{"ansi", styled, 5},
	} {
		if got := displayWidth(tc.s); got != tc.w {
			t.Errorf("%s: displayWidth(%q) = %d, want %d", tc.name, tc.s, got, tc.w)
		}
		for _, n := range []int{tc.w + 3, tc.w - 1, 1} {
			if got := displayWidth(padTo(tc.s, n)); got > n {
				t.Errorf("%s: padTo(%d) is %d cells", tc.name, n, got)
			}
			if got := displayWidth(trunc(tc.s, n)); got > n {
				t.Errorf("%s: trunc(%d) is %d cells", tc.name, n, got)
			}
		}
		n := tc.w + 3
		if got := displayWidth(padTo(tc.s, n)); got != n {
			t.Errorf("%s: padTo(%d) is %d cells", tc.name, n, got)
		}
		if got := displayWidth(padRight(tc.s, n)); got != n {
			t.Errorf("%s: padRight(%d) is %d cells", tc.name, n, got)
		}
		if got := padLeft(tc.s, n); displayWidth(got) != n || got[:3] != "   " {
			t.Errorf("%s: padLeft(%d) = %q", tc.name, n, got)
		}
		if got := trunc(tc.s, tc.w); got != tc.s {
			t.Errorf("%s: trunc at its own width changed it to %q", tc.name, got)
		}
	}

	// A styled string cut short keeps its escapes whole.
	if got := trunc(styled, 3); brokenEsc.MatchString(got) || stripANSI(got) != "er…" {
		t.Errorf("styled trunc = %q", got)
	}
	// A wide rune that does not fit is dropped, never split.
	if got := trunc("日本語", 4); got != "日…" {
		t.Errorf("cjk trunc = %q, want 日…", got)
	}
}
