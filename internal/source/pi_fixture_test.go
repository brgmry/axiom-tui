package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic pi session entries. Shapes mirror the real files (type, id,
// parentId, timestamp at the top level; message.* for messages) so the
// normaliser is tested against the same layout it sees in production.
// No real session content is committed.

type piEntry map[string]any

func piLine(e piEntry) string {
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func piTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func piHeader(id, cwd string, t time.Time) piEntry {
	return piEntry{"type": "session", "version": 3, "id": id, "timestamp": piTS(t), "cwd": cwd}
}

func piSessionInfo(name string, t time.Time) piEntry {
	return piEntry{"type": "session_info", "id": "si1", "parentId": nil, "timestamp": piTS(t), "name": name}
}

func piModelChange(provider, model string, t time.Time) piEntry {
	return piEntry{"type": "model_change", "id": "mc1", "parentId": "si1", "timestamp": piTS(t), "provider": provider, "modelId": model}
}

func piThinking(t time.Time) piEntry {
	return piEntry{"type": "thinking_level_change", "id": "tl1", "parentId": "mc1", "timestamp": piTS(t), "thinkingLevel": "medium"}
}

func piSystem(t time.Time) piEntry {
	return piEntry{"type": "message", "id": "sys1", "parentId": "tl1", "timestamp": piTS(t),
		"message": piEntry{"role": "system", "content": "", "sections": piEntry{"preamble": "You are a coding assistant."}, "timestamp": t.UnixMilli()}}
}

func piUser(id, text string, t time.Time) piEntry {
	return piEntry{"type": "message", "id": id, "parentId": "sys1", "timestamp": piTS(t),
		"message": piEntry{"role": "user", "content": []any{piEntry{"type": "text", "text": text}}, "timestamp": t.UnixMilli()}}
}

type piUsage struct {
	in, out, cacheRead, cacheWrite, total int
	cost                                  float64
}

func (u piUsage) entry() piEntry {
	return piEntry{"input": u.in, "output": u.out, "cacheRead": u.cacheRead, "cacheWrite": u.cacheWrite,
		"totalTokens": u.total, "cost": piEntry{"input": 0.0, "output": 0.0, "cacheRead": 0.0, "cacheWrite": 0.0, "total": u.cost},
		"cacheWrite1h": 0, "reasoning": 0}
}

type piToolCall struct{ id, name string }

// piAssistant builds an assistant message with optional text and tool calls.
func piAssistant(id, model, text string, calls []piToolCall, u piUsage, stop string, durationMs int, t time.Time) piEntry {
	content := []any{}
	if text != "" {
		content = append(content, piEntry{"type": "text", "text": text})
	}
	for _, c := range calls {
		content = append(content, piEntry{"type": "toolCall", "id": c.id, "name": c.name, "arguments": piEntry{"command": "ls"}})
	}
	msg := piEntry{"role": "assistant", "content": content, "api": "anthropic-messages", "provider": "anthropic",
		"model": model, "providerThinkingLevel": "medium", "usage": u.entry(), "stopReason": stop,
		"timestamp": t.UnixMilli(), "responseId": "msg_" + id, "rawStopReason": stop, "durationMs": durationMs, "thinkingLevel": "medium"}
	if stop == "error" || stop == "aborted" {
		msg["errorMessage"] = "This operation was aborted"
	}
	return piEntry{"type": "message", "id": id, "parentId": "x", "timestamp": piTS(t), "message": msg}
}

func piToolResult(id, callID, tool, text string, isError bool, durationMs int, t time.Time) piEntry {
	return piEntry{"type": "message", "id": id, "parentId": "x", "timestamp": piTS(t),
		"message": piEntry{"role": "toolResult", "toolCallId": callID, "toolName": tool,
			"content": []any{piEntry{"type": "text", "text": text}}, "isError": isError, "durationMs": durationMs, "timestamp": t.UnixMilli()}}
}

func piCompaction(id string, tokensBefore int, withUsage bool, cost float64, t time.Time) piEntry {
	e := piEntry{"type": "compaction", "id": id, "parentId": "x", "timestamp": piTS(t), "summary": "condensed", "firstKeptEntryId": "y",
		"tokensBefore": tokensBefore, "details": piEntry{}, "fromHook": true, "systemMessage": "s"}
	if withUsage {
		e["usage"] = piUsage{total: 10, cost: cost}.entry()
	}
	return e
}

func piUsageEntry(id, kind, model string, cost float64, t time.Time) piEntry {
	return piEntry{"type": "usage", "id": id, "parentId": "x", "timestamp": piTS(t), "kind": kind, "provider": "anthropic", "model": model,
		"usage": piUsage{total: 100, cost: cost}.entry()}
}

func piRouter(id, tier, by string, t time.Time) piEntry {
	return piEntry{"type": "custom", "customType": "pi.virtual-model-state", "data": piEntry{"provider": "pi-os", "modelId": "auto", "state": piEntry{"tier": tier, "by": by}},
		"id": id, "parentId": "x", "timestamp": piTS(t)}
}

func piCustom(id, customType string, t time.Time) piEntry {
	return piEntry{"type": "custom", "customType": customType, "data": piEntry{"observations": []any{piEntry{"content": "private"}}}, "id": id, "parentId": "x", "timestamp": piTS(t)}
}

func piCustomMessage(id string, t time.Time) piEntry {
	return piEntry{"type": "custom_message", "customType": "shell-function", "content": "$ os", "display": true, "id": id, "parentId": "x", "timestamp": piTS(t)}
}

func piUnknown(id, typ string, t time.Time) piEntry {
	return piEntry{"type": typ, "id": id, "parentId": "x", "timestamp": piTS(t), "label": "x"}
}

// writePiFile writes entries as one JSONL file and returns its path.
func writePiFile(t *testing.T, dir, name string, entries []piEntry) string {
	t.Helper()
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, piLine(e))
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendPiEntries(t *testing.T, path string, entries []piEntry) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		fmt.Fprintln(f, piLine(e))
	}
}

// normalSession is a session with every entry type, ending a minute ago.
func normalSession(base time.Time) []piEntry {
	t := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	return []piEntry{
		piHeader("sess-normal", "/Users/me/dev/coreOS/life/crm", t(0)),
		piSessionInfo("crm", t(1)),
		piModelChange("pi-os", "auto", t(2)),
		piThinking(t(3)),
		piSystem(t(4)),
		piUser("u1", "please list the files", t(5)),
		piRouter("r1", "deep", "haiku", t(6)),
		piAssistant("a1", "claude-opus-5-5", "", []piToolCall{{"call-1", "bash"}, {"call-2", "read"}}, piUsage{in: 4, out: 200, cacheRead: 4000, cacheWrite: 20000, total: 25850, cost: 0.10}, "toolUse", 4829, t(10)),
		piToolResult("t1", "call-1", "bash", "file-a\nfile-b", false, 16, t(11)),
		piToolResult("t2", "call-2", "read", "permission denied", true, 62, t(12)),
		piAssistant("a2", "claude-sonnet-5-5", "Here are the files.", nil, piUsage{in: 10, out: 50, total: 26000, cost: 0.02}, "stop", 1500, t(20)),
		piCompaction("c1", 180000, true, 0.03, t(30)),
		piUsageEntry("us1", "cache_warm", "claude-opus-5-5", 0.02, t(40)),
		piCustom("om1", "om.observations.recorded", t(41)),
		piCustomMessage("cm1", t(42)),
		piUnknown("lb1", "label", t(43)),
		piAssistant("a3", "claude-opus-5-5", "Done.", nil, piUsage{in: 1, out: 1, total: 30000, cost: 0.05}, "stop", 900, t(50)),
	}
}

// abortedSession has an aborted turn that still carries cost, an orphaned
// tool call and a fallback router entry.
func abortedSession(base time.Time) []piEntry {
	t := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	return []piEntry{
		piHeader("sess-aborted", "/Users/me/dev/pi-os", t(0)),
		piUser("u1", "do the thing", t(1)),
		piRouter("r1", "deep", "fallback", t(2)),
		piAssistant("a1", "claude-opus-5-5", "", []piToolCall{{"call-orphan", "bash"}}, piUsage{total: 5000, cost: 0.07}, "aborted", 3000, t(5)),
	}
}

// headerOnlySession is a session that started and did nothing.
func headerOnlySession(base time.Time) []piEntry {
	return []piEntry{piHeader("sess-empty", "/Users/me/Documents", base)}
}

// bigToolResult is a toolResult line with about 1.5 MB of content, with
// isError and durationMs after the content as in real files.
func bigToolResult(callID string, t time.Time) piEntry {
	return piToolResult("big", callID, "bash", strings.Repeat("x", 1500*1024), true, 77, t)
}
