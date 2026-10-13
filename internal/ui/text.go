package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Every width in the layout is measured in terminal cells by displayWidth.
// Byte length and rune count both get CJK, emoji and styled text wrong, so
// padding and truncation helpers live here and nowhere else.

// displayWidth is the number of terminal cells s occupies: ANSI escapes
// count zero, wide runes (CJK, most emoji) count two.
func displayWidth(s string) int {
	return ansi.StringWidth(s)
}

// padTo cuts s to at most n cells and pads it with spaces to exactly n.
func padTo(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if displayWidth(s) > n {
		s = ansi.Truncate(s, n, "")
	}
	return padRight(s, n)
}

// padRight left-aligns s in n cells by appending spaces. Wider input is
// returned unchanged.
func padRight(s string, n int) string {
	if w := displayWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// padLeft right-aligns s in n cells by prepending spaces. Wider input is
// returned unchanged.
func padLeft(s string, n int) string {
	if w := displayWidth(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

// trunc flattens s to one row (see oneLine) and shortens it to at most n
// cells, ending in an ellipsis when cut. ANSI aware, so styled input keeps
// its escapes intact. Tolerates n<=0 by returning "": callers may pass a
// negative width after subtracting other columns, and an empty cell beats
// a panic.
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = oneLine(s)
	if displayWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}
