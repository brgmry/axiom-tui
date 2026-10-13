package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// lineBreaks maps every character that would start a new terminal row or
// move the cursor to a single space.
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// oneLine flattens s so it occupies a single terminal row: newlines,
// carriage returns and tabs become spaces. Every one-row renderer goes
// through it, since messages from tool output routinely carry newlines.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n\t") {
		return s
	}
	return lineBreaks.Replace(s)
}

// prettyJSON returns indented JSON for a flat map, used in the expand modal.
// encoding/json writes map keys in sorted order, at every level, so the same
// event always renders the same way and fields line up across events.
func prettyJSON(m map[string]any) string {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Sprintf("<unmarshallable event: %v>", err)
	}
	return string(b)
}

// splitLines trims + splits text into lines, bounded by maxLines.
// Appends "…" as a sentinel when truncated.
func splitLines(s string, maxLines int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "…")
	}
	return lines
}
