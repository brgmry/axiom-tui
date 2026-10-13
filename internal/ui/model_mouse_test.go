package ui

import (
	"testing"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"
)

// zonesOf renders m and waits for bubblezone to record each id; Scan
// hands zones to a worker goroutine, so they land shortly after View.
func zonesOf(t *testing.T, m Model, ids ...string) map[string]*zone.ZoneInfo {
	t.Helper()
	m.View()
	deadline := time.Now().Add(2 * time.Second)
	out := map[string]*zone.ZoneInfo{}
	for _, id := range ids {
		for {
			if z := zone.Get(id); !z.IsZero() {
				out[id] = z
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("zone %q never recorded", id)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return out
}

func click(z *zone.ZoneInfo, b tea.MouseButton, a tea.MouseAction) tea.MouseMsg {
	return tea.MouseMsg{X: z.StartX, Y: z.StartY, Button: b, Action: a}
}

// TestHandleMouse: left click on a top error drills into it, left click on
// a panel focuses it, right click on a log row copies it, and anything
// else is ignored.
func TestHandleMouse(t *testing.T) {
	var clip []string
	writeClipboard = func(s string) error { clip = append(clip, s); return nil }
	defer func() { writeClipboard = clipboard.WriteAll }()

	m := newTestModel(t, &fakeSource{info: fullInfo()})
	m.width, m.height, m.ready = 120, 40, true
	m = loadAll(t, m)
	zs := zonesOf(t, m, "toperror-0", "logrow-0", focusZone(FocusStats), focusZone(FocusRecent))
	nowhere := &zone.ZoneInfo{StartX: 10000, StartY: 10000}

	cases := []struct {
		name  string
		msg   tea.MouseMsg
		check func(t *testing.T, m Model, cmd tea.Cmd)
	}{
		{"drill top error", click(zs["toperror-0"], tea.MouseButtonLeft, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.filter.Search != "boom" || m.focus != FocusLogs || m.errorCursor != 0 {
					t.Errorf("search %q focus %d", m.filter.Search, m.focus)
				}
				if got := toasts(m, cmd); !hasToast(got, "filtered by selected error") {
					t.Errorf("toasts %q", got)
				}
			}},
		{"focus panel", click(zs[focusZone(FocusRecent)], tea.MouseButtonLeft, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if m.focus != FocusRecent || cmd != nil {
					t.Errorf("focus %d", m.focus)
				}
			}},
		{"copy row", click(zs["logrow-0"], tea.MouseButtonRight, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if got := toasts(m, cmd); !hasToast(got, "copied event to clipboard") || len(clip) != 1 {
					t.Errorf("toasts %q, %d clipboard writes", got, len(clip))
				}
			}},
		{"right click off rows", click(zs[focusZone(FocusStats)], tea.MouseButtonRight, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.focus != FocusLogs {
					t.Error("right click off a row should do nothing")
				}
			}},
		{"release", click(zs[focusZone(FocusStats)], tea.MouseButtonLeft, tea.MouseActionRelease),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.focus != FocusLogs {
					t.Error("a release should do nothing")
				}
			}},
		{"middle button", click(zs[focusZone(FocusStats)], tea.MouseButtonMiddle, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.focus != FocusLogs {
					t.Error("a middle click should do nothing")
				}
			}},
		{"outside every zone", click(nowhere, tea.MouseButtonLeft, tea.MouseActionPress),
			func(t *testing.T, m Model, cmd tea.Cmd) {
				if cmd != nil || m.focus != FocusLogs || m.filter.Search != "" {
					t.Error("a click outside every zone should do nothing")
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next, cmd := m.Update(c.msg)
			c.check(t, next.(Model), cmd)
		})
	}
}
