package main

import "testing"

// TestPrettyJSONSorted: keys come out sorted at every level, so the expand
// modal shows an event the same way every time.
func TestPrettyJSONSorted(t *testing.T) {
	m := map[string]any{
		"zeta":  1,
		"alpha": map[string]any{"y": true, "b": "x"},
		"mid":   nil,
	}
	want := `{
  "alpha": {
    "b": "x",
    "y": true
  },
  "mid": null,
  "zeta": 1
}`
	for i := 0; i < 20; i++ {
		if got := prettyJSON(m); got != want {
			t.Fatalf("prettyJSON =\n%s\nwant\n%s", got, want)
		}
	}
}
