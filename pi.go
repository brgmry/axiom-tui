package main

import (
	"context"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buger/jsonparser"
)

func init() {
	registerSourceType("pi", sourceType{
		open: func(name string, sc SourceConfig, opts OpenOptions) (Source, error) {
			return NewPiSource(name, sc, opts)
		},
		defaults: piDefaults,
	})
}

// piDefaults points at the pi sessions directory and hides debug entries.
func piDefaults(name string, sc *SourceConfig) {
	sc.applyFileDefaults()
	if len(sc.Paths) == 0 {
		sc.Paths = StringList{defaultPiPaths}
	}
	if sc.OrphanAfter == 0 {
		sc.OrphanAfter = Duration(defaultOrphanAfter)
	}
	if sc.HideLevels == nil {
		sc.HideLevels = StringList{"debug"}
	}
}

// Entry kinds, stored in event.kind so Agents can classify events without
// re-reading fields. Zero is any entry Agents does not count.
const (
	piKindOther uint8 = iota
	piKindSession
	piKindAssistant
	piKindToolResult
	piKindUser
	piKindCompaction
	piKindUsage
	piKindRouter
)

// Bits of event.flags set by the pi handler.
const (
	piFlagFallback  uint8 = 1 << iota // router entry chose by fallback
	piFlagToolError                   // tool result with isError
)

// Duration slots of the pi source: assistant turns and tool results.
var piDurationFields = []string{"llm", "tool"}

// PiSource reads pi agent session files. It is a FileSource with a
// normaliser that knows the session entry shapes, plus per-agent stats.
type PiSource struct {
	*FileSource
	h *piHandler
}

// NewPiSource opens the pi session directory (default ~/.pi/agent/sessions).
// sc.Type must be "pi"; the registry only routes that type here.
func NewPiSource(name string, sc SourceConfig, opts OpenOptions) (*PiSource, error) {
	return newPiSource(name, sc, opts, true)
}

func newPiSource(name string, sc SourceConfig, opts OpenOptions, start bool) (*PiSource, error) {
	sc.applyTypeDefaults(name)
	st := newStore(sc.MaxEvents)
	st.durFields = piDurationFields
	st.costDimRole = costDimModel
	c := &counters{}
	h := newPiHandler(st, c, sc.OrphanAfter.D())
	t := newTailer(sc, st, h, c, opts.PollEvery)
	h.tailer = t
	fs := &FileSource{name: name, sc: sc, store: st, tailer: t, counters: c}
	fs.info = SourceInfo{
		Name:              name,
		Kind:              "pi",
		Caps:              CapGroup | CapCost | CapDuration,
		GroupLabel:        "agent",
		InterestingFields: []string{"model", "tool", "cost", "tokens"},
		DisplayPrefix:     "",
		Filters:           FilterFields{Provider: "model", Operation: "tool", Trace: "session"},
		HideLevels:        []string(sc.HideLevels),
	}
	if start {
		t.start()
	}
	return &PiSource{FileSource: fs, h: h}, nil
}

// ─── Handler ─────────────────────────────────────────────────────────────────

// piFile is the side table of one session file. session, agent and bad
// are set once when the entry is created; every other field is written
// and read under piHandler.mu.
type piFile struct {
	session       string
	agent         string
	bad           bool             // first line was not a session header
	orphans       map[string]int64 // unmatched tool call id to its event time
	newestTs      int64            // newest event time in the file
	contextTokens int64            // usage.totalTokens of the newest assistant message
}

type piHandler struct {
	st          *store
	counters    *counters
	orphanAfter time.Duration
	tailer      *tailer
	intern      interner

	mu    sync.Mutex
	files map[int32]*piFile
}

func newPiHandler(st *store, c *counters, orphanAfter time.Duration) *piHandler {
	return &piHandler{st: st, counters: c, orphanAfter: orphanAfter, intern: interner{}, files: map[int32]*piFile{}}
}

func (h *piHandler) reset(file int32) {
	h.mu.Lock()
	delete(h.files, file)
	h.mu.Unlock()
}

// fileState returns the side table entry for a file, creating it from the
// header line when this is the first line.
func (h *piHandler) fileState(file int32, offset int64, line []byte) *piFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.files[file]; ok {
		return f
	}
	f := &piFile{orphans: map[string]int64{}}
	h.files[file] = f
	typ, _ := jsonparser.GetString(line, "type")
	cwd, cwdErr := jsonparser.GetString(line, "cwd")
	if offset != 0 || typ != "session" || cwdErr != nil {
		f.bad = true
		h.counters.unreadableFiles.Add(1)
		return f
	}
	f.agent = h.intern.get(filepath.Base(filepath.Clean(cwd)))
	id, _ := jsonparser.GetString(line, "id")
	f.session = h.intern.get(id)
	return f
}

// contentBlocks returns the first text block of a content array and the
// names and ids of its toolCall blocks.
func contentBlocks(line []byte, path ...string) (text string, hasText bool, toolNames []string, toolIDs []string) {
	_, _ = jsonparser.ArrayEach(line, func(block []byte, _ jsonparser.ValueType, _ int, _ error) {
		typ, _ := jsonparser.GetString(block, "type")
		switch typ {
		case "text":
			if !hasText {
				if s, err := jsonparser.GetString(block, "text"); err == nil {
					text, hasText = s, true
				}
			}
		case "toolCall":
			name, _ := jsonparser.GetString(block, "name")
			id, _ := jsonparser.GetString(block, "id")
			toolNames = append(toolNames, name)
			toolIDs = append(toolIDs, id)
		}
	}, path...)
	return
}

func firstRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func fmtCost(c float64) string { return "$" + strconv.FormatFloat(c, 'f', 4, 64) }

func (h *piHandler) handle(file int32, gen uint32, offset int64, line []byte, ingest time.Time) (event, string, bool) {
	f := h.fileState(file, offset, line)
	if f.bad {
		return event{}, "", false
	}
	ev := event{cost: math.NaN(), duration: []float64{math.NaN(), math.NaN()}}
	ev.client = f.agent

	timed := false
	if v, t, _, err := jsonparser.Get(line, "timestamp"); err == nil {
		if ts, ok := parseTime(v, t, ""); ok {
			ev.ts, timed = ts.UnixNano(), true
		}
	}
	if !timed {
		ev.ts = ingest.UnixNano()
		h.counters.untimed.Add(1)
	}

	// Side-table updates are collected here and applied under h.mu at the
	// end, since Agents reads the table from the UI goroutine.
	var (
		newCalls     []string
		resolvedCall string
		ctxTokens    int64 = -1
	)

	typ, _ := jsonparser.GetString(line, "type")
	level := "debug"
	kind := piKindOther
	fields := []kv{}
	addField := func(k, v string) {
		if v != "" {
			fields = append(fields, kv{k: k, v: v})
		}
	}
	getStr := func(path ...string) string {
		s, _ := jsonparser.GetString(line, path...)
		return s
	}
	getNum := func(path ...string) (float64, bool) {
		v, t, _, err := jsonparser.Get(line, path...)
		if err != nil {
			return 0, false
		}
		return valueFloat(v, t)
	}
	numField := func(k string, path ...string) {
		if v, ok := getNum(path...); ok {
			fields = append(fields, kv{k: k, v: strconv.FormatFloat(v, 'f', -1, 64)})
		}
	}

	// Event ID = "<file index>:<entry id>"; the header line has no entry id
	// and uses "session".
	id := getStr("id")
	if typ == "session" {
		id = "session"
	}

	switch typ {
	case "session":
		kind = piKindSession
		ev.message = "session " + getStr("id") + " cwd " + getStr("cwd")

	case "message":
		role := getStr("message", "role")
		switch role {
		case "assistant":
			kind = piKindAssistant
			stop := getStr("message", "stopReason")
			text, hasText, toolNames, toolIDs := contentBlocks(line, "message", "content")
			if stop == "error" || stop == "aborted" {
				level = "error"
				ev.message = getStr("message", "errorMessage")
				if ev.message == "" {
					ev.message = "assistant " + stop
				}
			} else {
				level = "info"
				if hasText {
					ev.message = text
				} else if len(toolNames) > 0 {
					ev.message = "-> " + strings.Join(toolNames, ", ")
				} else {
					ev.message = "assistant " + stop
				}
			}
			model := h.intern.get(getStr("message", "model"))
			ev.costDim = model
			addField("model", model)
			addField("provider", h.intern.get(getStr("message", "provider")))
			addField("stopReason", h.intern.get(stop))
			if c, ok := getNum("message", "usage", "cost", "total"); ok {
				ev.cost = c
				addField("cost", fmtCost(c))
			}
			numField("tokens", "message", "usage", "totalTokens")
			numField("in", "message", "usage", "input")
			numField("out", "message", "usage", "output")
			numField("cacheRead", "message", "usage", "cacheRead")
			numField("cacheWrite", "message", "usage", "cacheWrite")
			addField("toolCalls", strconv.Itoa(len(toolNames)))
			ev.toolCalls = uint16(min(len(toolNames), math.MaxUint16))
			addField("responseId", getStr("message", "responseId"))
			if d, ok := getNum("message", "durationMs"); ok {
				ev.duration[0] = d
			}
			if tokens, ok := getNum("message", "usage", "totalTokens"); ok {
				ctxTokens = int64(tokens)
			}
			newCalls = toolIDs

		case "toolResult":
			kind = piKindToolResult
			tool := h.intern.get(getStr("message", "toolName"))
			isErr, _ := jsonparser.GetBoolean(line, "message", "isError")
			text, _, _, _ := contentBlocks(line, "message", "content")
			if isErr {
				level = "error"
				ev.message = tool + " error: " + firstRunes(text, 120)
			} else {
				level = "info"
				ev.message = tool + " ok: " + firstRunes(text, 120)
			}
			callID := getStr("message", "toolCallId")
			addField("tool", tool)
			addField("toolCallId", callID)
			addField("isError", strconv.FormatBool(isErr))
			if isErr {
				ev.flags |= piFlagToolError
			}
			if d, ok := getNum("message", "durationMs"); ok {
				ev.duration[1] = d
			}
			resolvedCall = callID

		case "user":
			kind = piKindUser
			level = "info"
			text, _, _, _ := contentBlocks(line, "message", "content")
			if text == "" {
				// Plain string content.
				text = getStr("message", "content")
			}
			ev.message = firstRunes(text, 200)
			addField("role", "user")

		case "system":
			level = "debug"
			ev.message = "system prompt"

		default:
			level = "debug"
			ev.message = "message " + role
		}

	case "compaction":
		kind = piKindCompaction
		level = "warn"
		before, _ := getNum("tokensBefore")
		ev.message = "compaction, " + strconv.FormatFloat(before, 'f', -1, 64) + " tokens before"
		numField("tokensBefore", "tokensBefore")
		if b, err := jsonparser.GetBoolean(line, "fromHook"); err == nil {
			addField("fromHook", strconv.FormatBool(b))
		}
		if c, ok := getNum("usage", "cost", "total"); ok {
			ev.cost = c
			addField("cost", fmtCost(c))
		}

	case "usage":
		kind = piKindUsage
		level = "info"
		model := h.intern.get(getStr("model"))
		ev.costDim = model
		usageKind := h.intern.get(getStr("kind"))
		c, hasCost := getNum("usage", "cost", "total")
		if hasCost {
			ev.cost = c
			addField("cost", fmtCost(c))
		}
		ev.message = usageKind + " " + model + " " + fmtCost(c)
		addField("model", model)
		addField("provider", h.intern.get(getStr("provider")))
		addField("kind", usageKind)

	case "custom":
		customType := getStr("customType")
		if customType == "pi.virtual-model-state" {
			kind = piKindRouter
			tier := h.intern.get(getStr("data", "state", "tier"))
			by := h.intern.get(getStr("data", "state", "by"))
			if by == "fallback" {
				level = "warn"
				ev.flags |= piFlagFallback
			} else {
				level = "info"
			}
			ev.message = "router " + tier + " by " + by
			addField("tier", tier)
			addField("by", by)
			addField("router", "true")
		} else {
			level = "debug"
			ev.message = customType
			addField("customType", h.intern.get(customType))
		}

	case "custom_message":
		level = "debug"
		ev.message = getStr("customType")

	case "model_change":
		level = "info"
		provider := h.intern.get(getStr("provider"))
		modelID := h.intern.get(getStr("modelId"))
		ev.message = "model " + provider + "/" + modelID
		addField("provider", provider)
		addField("modelId", modelID)

	case "session_info":
		level = "info"
		name := getStr("name")
		ev.message = "session named " + name
		addField("name", name)

	default:
		level = "debug"
		ev.message = typ
	}

	addField("session", f.session)
	ev.message = capMessage(ev.message, maxMessageBytes)
	ev.fields = fields
	ev.kind = kind
	ev.level = h.st.levelIndex(level)

	h.mu.Lock()
	if ev.ts > f.newestTs {
		f.newestTs = ev.ts
	}
	if ctxTokens >= 0 {
		f.contextTokens = ctxTokens
	}
	for _, id := range newCalls {
		if id != "" {
			f.orphans[id] = ev.ts
		}
	}
	if resolvedCall != "" {
		delete(f.orphans, resolvedCall)
	}
	h.mu.Unlock()
	return ev, id, true
}

// ─── Agents ──────────────────────────────────────────────────────────────────

// Agents implements AgentSource: one row per cwd basename seen in the store.
func (p *PiSource) Agents(ctx context.Context, lookback time.Duration) ([]AgentStat, error) {
	now := time.Now()
	after := windowStart(now, lookback)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixNano()
	hourAgo := now.Add(-time.Hour).UnixNano()

	type agg struct {
		stat   AgentStat
		models map[string]float64
	}
	agents := map[string]*agg{}
	get := func(name string) *agg {
		a := agents[name]
		if a == nil {
			a = &agg{stat: AgentStat{Name: name}, models: map[string]float64{}}
			agents[name] = a
		}
		return a
	}

	p.store.scan(func(e *event) {
		if e.client == "" {
			return
		}
		a := get(e.client)
		if e.ts > a.stat.LastSeen.UnixNano() {
			a.stat.LastSeen = time.Unix(0, e.ts)
		}
		if !math.IsNaN(e.cost) {
			if e.ts >= midnight && e.ts <= now.UnixNano() {
				a.stat.SpendToday += e.cost
			}
			if e.ts >= hourAgo && e.ts <= now.UnixNano() {
				a.stat.SpendHour += e.cost
			}
		}
		if !inWindow(e.ts, after) {
			return
		}
		switch e.kind {
		case piKindCompaction:
			a.stat.Compactions++
		case piKindToolResult:
			if e.level == levelError {
				a.stat.ToolErrors++
			}
		case piKindAssistant:
			a.stat.ToolCalls += int(e.toolCalls)
		case piKindRouter:
			a.stat.FallbackD++
			if e.flags&piFlagFallback != 0 {
				a.stat.FallbackN++
			}
		}
		if !math.IsNaN(e.cost) && e.costDim != "" {
			a.models[e.costDim] += e.cost
		}
	})

	// Side table: orphans (any age) and context tokens from the newest file.
	cutoff := now.Add(-p.h.orphanAfter).UnixNano()
	type newest struct {
		ts   int64
		file int32
	}
	newestFile := map[string]newest{}
	p.h.mu.Lock()
	for idx, f := range p.h.files {
		if f.bad || f.agent == "" {
			continue
		}
		a := get(f.agent)
		for _, ts := range f.orphans {
			if ts < cutoff {
				a.stat.OrphanCalls++
			}
		}
		if n, ok := newestFile[f.agent]; !ok || f.newestTs > n.ts {
			newestFile[f.agent] = newest{ts: f.newestTs, file: idx}
		}
	}
	for name, n := range newestFile {
		get(name).stat.ContextTokens = p.h.files[n.file].contextTokens
	}
	p.h.mu.Unlock()
	// The tailer has its own lock and calls reset on this handler; never
	// hold h.mu while asking it anything.
	for name, n := range newestFile {
		get(name).stat.SessionFile = filepath.Base(p.tailer.filePath(n.file))
	}

	out := make([]AgentStat, 0, len(agents))
	for _, a := range agents {
		a.stat.ModelMix = sortedCosts(a.models)
		if len(a.stat.ModelMix) > 0 {
			a.stat.TopModel = a.stat.ModelMix[0].Client
		}
		out = append(out, a.stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpendToday != out[j].SpendToday {
			return out[i].SpendToday > out[j].SpendToday
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
