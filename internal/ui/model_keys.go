package ui

import (
	"fmt"
	"maps"

	"github.com/brgmry/tailboard/internal/source"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── Text inputs ─────────────────────────────────────────────────────────────

// textInput ties a prompt mode to the binding that opens it, the input
// field that edits it and the filter value it sets on enter.
type textInput struct {
	mode    Mode
	binding func(KeyMap) key.Binding
	input   func(*Model) *textinput.Model
	value   func(*LogFilter) *string
	// field names the source field the filter needs; nil means always
	// available.
	field func(source.FilterFields) string
}

var textInputs = []textInput{
	{
		mode:    ModeSearch,
		binding: func(k KeyMap) key.Binding { return k.Search },
		input:   func(m *Model) *textinput.Model { return &m.searchInput },
		value:   func(f *LogFilter) *string { return &f.Search },
	},
	{
		mode:    ModeClient,
		binding: func(k KeyMap) key.Binding { return k.ClientFilter },
		input:   func(m *Model) *textinput.Model { return &m.clientInput },
		value:   func(f *LogFilter) *string { return &f.Client },
	},
	{
		mode:    ModeProvider,
		binding: func(k KeyMap) key.Binding { return k.ProviderFilter },
		input:   func(m *Model) *textinput.Model { return &m.providerInput },
		value:   func(f *LogFilter) *string { return &f.Provider },
		field:   func(f source.FilterFields) string { return f.Provider },
	},
	{
		mode:    ModeOperation,
		binding: func(k KeyMap) key.Binding { return k.OperationFilter },
		input:   func(m *Model) *textinput.Model { return &m.operationInput },
		value:   func(f *LogFilter) *string { return &f.Operation },
		field:   func(f source.FilterFields) string { return f.Operation },
	},
}

// textInputFor returns the input behind a prompt mode.
func textInputFor(mode Mode) (textInput, bool) {
	for _, ti := range textInputs {
		if ti.mode == mode {
			return ti, true
		}
	}
	return textInput{}, false
}

// blurInputs closes every prompt.
func (m *Model) blurInputs() {
	for _, ti := range textInputs {
		ti.input(m).Blur()
	}
}

// ─── Key dispatch ────────────────────────────────────────────────────────────

// handleKey dispatches on mode, then on key. Mode-specific handling first so
// text inputs can swallow keys like "e" and "/" that would otherwise trigger
// filters.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Escape works from any mode: closes modals/inputs first, then acts as a
	// "reset" in normal mode (clears all filters + resumes tailing).
	if key.Matches(msg, m.keys.Escape) {
		return m.handleEscape()
	}
	if ti, ok := textInputFor(m.mode); ok {
		return m.handleInputKey(ti, msg)
	}
	switch m.mode {
	case ModeExpand:
		return m.handleExpandKey(msg)
	case ModeHelp:
		return m, nil
	case ModeSourceSwitcher:
		return m.handleSwitcherKey(msg)
	}
	for _, a := range m.normalKeys() {
		if key.Matches(msg, a.binding) {
			cmd := a.run(&m, msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) handleEscape() (tea.Model, tea.Cmd) {
	switch m.mode {
	case ModeSearch, ModeClient, ModeProvider, ModeOperation:
		m.mode = ModeNormal
		m.blurInputs()
		return m, nil
	case ModeExpand, ModeHelp, ModeSourceSwitcher:
		m.mode = ModeNormal
		m.expandedLog = nil
		m.expandLoading = false
		return m, nil
	}
	// Reset to defaults: clear filters, unpause, jump to tail.
	m.filter = m.filter.Reset()
	m.paused = false
	m.scrollOff = 0
	return m, toastCmd("reset: filters cleared, tailing resumed")
}

// handleInputKey feeds a key to the open prompt; enter commits its value
// to the filter.
func (m Model) handleInputKey(ti textInput, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	in := ti.input(&m)
	if msg.Type == tea.KeyEnter {
		*ti.value(&m.filter) = in.Value()
		m.mode = ModeNormal
		in.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	*in, cmd = in.Update(msg)
	return m, cmd
}

func (m Model) handleExpandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Yank) && m.expandedLog != nil {
		// Still loading: the copy hydrates on its own.
		return m, m.hydrateCmd(*m.expandedLog, hydrateCopy)
	}
	return m, nil
}

func (m Model) handleSwitcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.sourceCursor > 0 {
			m.sourceCursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.sourceCursor < len(m.sources)-1 {
			m.sourceCursor++
		}
	case msg.Type == tea.KeyEnter && len(m.sources) > 0:
		next := m.sources[m.sourceCursor]
		m.mode = ModeNormal
		if next == m.name {
			return m, nil
		}
		return m.switchSource(next)
	}
	return m, nil
}

// ─── Normal mode ─────────────────────────────────────────────────────────────

// keyAction is one normal-mode binding and what it does. run mutates the
// model in place and returns the follow-up command.
type keyAction struct {
	binding key.Binding
	run     func(*Model, tea.KeyMsg) tea.Cmd
}

// normalKeys is the normal-mode key table. No two bindings share a key, so
// order only matters for reading.
func (m *Model) normalKeys() []keyAction {
	k := m.keys
	actions := []keyAction{
		{k.Quit, func(*Model, tea.KeyMsg) tea.Cmd { return tea.Quit }},
		{k.Refresh, (*Model).keyRefresh},
		{k.Help, func(m *Model, _ tea.KeyMsg) tea.Cmd { m.mode = ModeHelp; return nil }},
		{k.Pause, (*Model).keyPause},
		{k.Tab, (*Model).keyFocusNext},
		{k.FocusRight, (*Model).keyFocusNext},
		{k.FocusLeft, (*Model).keyFocusPrev},
		{k.Up, (*Model).keyUp},
		{k.Down, (*Model).keyDown},
		{k.PageUp, (*Model).keyPageUp},
		{k.PageDown, func(m *Model, _ tea.KeyMsg) tea.Cmd { m.scrollBy(-10); return nil }},
		{k.Top, (*Model).keyTop},
		{k.Bottom, (*Model).keyBottom},
		{k.Enter, (*Model).keyEnter},
		{k.Yank, (*Model).keyYank},
		{k.ToggleError, toggleLevel("error")},
		{k.ToggleWarn, toggleLevel("warn")},
		{k.ToggleInfo, toggleLevel("info")},
		{k.ToggleDebug, toggleLevel("debug")},
		{k.TraceRequest, (*Model).keyTrace},
		{k.ClearFilter, (*Model).keyClearFilter},
		{k.TimeRange, (*Model).keyTimeRange},
		{k.SourceSwitcher, (*Model).keySourceSwitcher},
		{k.SavePreset, (*Model).keySavePreset},
		{k.LoadPreset, (*Model).keyLoadPreset},
	}
	for _, ti := range textInputs {
		actions = append(actions, keyAction{ti.binding(k), openInput(ti)})
	}
	return actions
}

func (m *Model) keyRefresh(tea.KeyMsg) tea.Cmd {
	m.queryErr = nil
	return tea.Batch(m.refreshAll(), toastCmd("refreshed"))
}

func (m *Model) keyPause(tea.KeyMsg) tea.Cmd {
	m.paused = !m.paused
	if m.paused {
		return toastCmd("paused: space to resume")
	}
	m.scrollOff = 0
	return toastCmd("resumed")
}

func (m *Model) keyFocusNext(tea.KeyMsg) tea.Cmd {
	m.focus = (m.focus + 1) % numFocuses
	return nil
}

func (m *Model) keyFocusPrev(tea.KeyMsg) tea.Cmd {
	m.focus = (m.focus - 1 + numFocuses) % numFocuses
	return nil
}

// agentsFocused reports whether focus is on the Agents panel, which is
// read-only.
func (m *Model) agentsFocused() bool {
	return m.focus == FocusTopRoutes && m.agents != nil
}

func (m *Model) keyUp(tea.KeyMsg) tea.Cmd {
	switch {
	case m.focus == FocusTopErrors:
		if m.errorCursor > 0 {
			m.errorCursor--
		}
	case m.agentsFocused():
	default:
		m.scrollBy(1)
		m.paused = true
	}
	return nil
}

func (m *Model) keyDown(tea.KeyMsg) tea.Cmd {
	switch {
	case m.focus == FocusTopErrors:
		if m.errorCursor < len(m.topErrors)-1 {
			m.errorCursor++
		}
	case m.agentsFocused():
	default:
		m.scrollBy(-1)
	}
	return nil
}

func (m *Model) keyPageUp(tea.KeyMsg) tea.Cmd {
	m.scrollBy(10)
	m.paused = true
	return nil
}

func (m *Model) keyTop(tea.KeyMsg) tea.Cmd {
	// The oldest full page, not past it: an offset beyond maxScroll
	// would leave the panel blank.
	m.scrollOff = m.maxScroll(len(m.logs.Filtered(m.filter)))
	m.paused = true
	return nil
}

func (m *Model) keyBottom(tea.KeyMsg) tea.Cmd {
	m.scrollOff = 0
	m.paused = false
	return toastCmd("tailing live")
}

// selectedRow is the stream row under the cursor.
func (m *Model) selectedRow() (source.LogEvent, bool) {
	filtered := m.logs.Filtered(m.filter)
	if idx := m.visibleIndex(filtered); idx >= 0 && idx < len(filtered) {
		return filtered[idx], true
	}
	return source.LogEvent{}, false
}

func (m *Model) keyEnter(tea.KeyMsg) tea.Cmd {
	if m.focus == FocusTopErrors && m.errorCursor < len(m.topErrors) {
		m.filter.Search = m.topErrors[m.errorCursor].Message
		m.focus = FocusLogs
		return toastCmd("filtered by selected error")
	}
	if m.agentsFocused() {
		return nil
	}
	// Expand selected log line from the filtered view. The modal opens
	// at once and shows loading until the hydrate result lands on the
	// expanded copy, where y can copy it.
	ev, ok := m.selectedRow()
	if !ok {
		return nil
	}
	m.expandedLog = &ev
	m.mode = ModeExpand
	if ev.Raw == nil {
		m.expandLoading = true
		return m.hydrateCmd(ev, hydrateExpand)
	}
	return nil
}

// keyYank copies the selected stream row.
func (m *Model) keyYank(tea.KeyMsg) tea.Cmd {
	if m.focus != FocusLogs {
		return nil
	}
	if ev, ok := m.selectedRow(); ok {
		return m.hydrateCmd(ev, hydrateCopy)
	}
	return nil
}

// toggleLevel shows or hides one level in the stream.
func toggleLevel(level string) func(*Model, tea.KeyMsg) tea.Cmd {
	return func(m *Model, _ tea.KeyMsg) tea.Cmd {
		m.filter.HideLevels[level] = !m.filter.HideLevels[level]
		return nil
	}
}

// openInput opens a prompt seeded with the current filter value, unless
// the source has no field for it.
func openInput(ti textInput) func(*Model, tea.KeyMsg) tea.Cmd {
	return func(m *Model, _ tea.KeyMsg) tea.Cmd {
		if ti.field != nil && ti.field(m.info.Filters) == "" {
			return nil
		}
		m.mode = ti.mode
		in := ti.input(m)
		in.SetValue(*ti.value(&m.filter))
		in.Focus()
		return nil
	}
}

func (m *Model) keyTrace(tea.KeyMsg) tea.Cmd {
	// Only traceable from the logs panel; other focuses have no row.
	if m.focus != FocusLogs || m.info.Filters.Trace == "" {
		return nil
	}
	ev, ok := m.selectedRow()
	if !ok {
		return nil
	}
	label := m.info.Label(m.info.Filters.Trace)
	rid := asFieldString(ev.Fields[m.info.Filters.Trace])
	if rid == "" {
		return errorToastCmd("no " + label + " on selected row")
	}
	m.filter.Trace = rid
	short := rid
	if len(short) > 8 {
		short = short[:8]
	}
	return toastCmd(label + " " + short)
}

func (m *Model) keyClearFilter(tea.KeyMsg) tea.Cmd {
	m.filter = m.filter.Reset()
	return toastCmd("filters cleared")
}

func (m *Model) keyTimeRange(tea.KeyMsg) tea.Cmd {
	m.timeRange = nextTimeRange(m.timeRange)
	return tea.Batch(
		m.refreshAll(),
		toastCmd(fmt.Sprintf("time range: %s", formatRange(m.timeRange))),
	)
}

func (m *Model) keySourceSwitcher(tea.KeyMsg) tea.Cmd {
	// Snap cursor to the active source for clearer UX.
	for i, n := range m.sources {
		if n == m.name {
			m.sourceCursor = i
			break
		}
	}
	m.mode = ModeSourceSwitcher
	return nil
}

func (m *Model) keySavePreset(tea.KeyMsg) tea.Cmd {
	slot := m.presets.NextSlot()
	if slot == "" {
		return errorToastCmd("all preset slots full (1-9): load + delete first")
	}
	preset := FilterToPreset(m.filter)
	if m.presets.Items == nil {
		m.presets.Items = map[string]Preset{}
	}
	m.presets.Items[slot] = preset
	// The write runs in a command on a snapshot, so a later save
	// cannot race it on the shared map.
	snap := Presets{Items: maps.Clone(m.presets.Items)}
	ok := fmt.Sprintf("saved as preset %s: %s", slot, preset.Name)
	return func() tea.Msg {
		if err := snap.Save(); err != nil {
			return toastMsg{"preset save failed: " + err.Error(), toastError}
		}
		return toastMsg{ok, toastOK}
	}
}

// keyLoadPreset loads the preset in the pressed slot, if there is one.
func (m *Model) keyLoadPreset(msg tea.KeyMsg) tea.Cmd {
	slot := msg.String()
	p, ok := m.presets.Items[slot]
	if !ok {
		return nil
	}
	m.filter = PresetToFilter(p, m.filter)
	m.scrollOff = 0
	return toastCmd("preset " + slot + ": " + p.Name)
}
