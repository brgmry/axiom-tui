# tailboard

A terminal dashboard for any log stream. Point it at JSONL files, at your [pi](https://github.com/badlogic/pi-mono) agent sessions, or at an [Axiom](https://axiom.co) dataset, and get a live stream with filtering, aggregate charts and interactive drill-in, all keyboard driven. Everything runs locally; nothing leaves the machine unless you configure an Axiom source.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lipgloss](https://github.com/charmbracelet/lipgloss).

```
┌──────────────────────────┬──────────────────────────┐
│                          │  Last 1h   │  Cost (1h)  │
│       LIVE STREAM        │  info / warn / errors     │
│   (filter, search,       ├──────────────────────────┤
│    pause, expand)        │   Throughput chart       │
│                          ├──────────────────────────┤
│                          │   Errors & Warns trend   │
├──────────┬───────────────┴──────────┬───────────────┤
│ Top      │  Recent Issues           │  Top Routes   │
│ Errors   │  (or the Agents panel)   │               │
└──────────┴──────────────────────────┴───────────────┘
 ? help  space pause  / search  T range  D source  esc reset  tab focus  enter expand  q quit
```

## Install

```bash
git clone git@github.com:brgmry/tailboard.git
cd tailboard
make install        # builds + copies to ~/.local/bin/tailboard
```

Requires Go 1.26.2 or later.

## Run

No config needed for files or for pi sessions:

```bash
tailboard app.jsonl                       # follow one file
tailboard /var/log/svc/ ~/logs/*.jsonl    # directories (recursive, *.jsonl) and globs
tailboard --source pi                     # pi agent sessions in ~/.pi/agent/sessions
tailboard                                 # the only configured source, else pi when its directory exists
tailboard --source NAME                   # a source from config (or a path)
tailboard --dataset NAME                  # an Axiom dataset
tailboard --interval 5                    # refresh every 5 s
tailboard --config /path/to/config.toml   # custom config path
tailboard --env /path/to/.env             # source an extra env file
tailboard app.jsonl --check               # headless: print a JSON summary and exit
```

Source resolution order: `--dataset`, `--source`, positional paths, `default_source`, `default_dataset`, `$AXIOM_DATASET`, the only configured source, then the built-in `pi` source when `~/.pi/agent/sessions` exists.

### `--check`

Opens the source, waits for the first full backfill pass (file sources) or runs one stats query (Axiom), prints one JSON object and exits:

```json
{"source":"pi","kind":"pi","files":16,"events":2811,"skipped_lines":0,"untimed":0,"parse_errors":0,
 "unreadable_files":0,"elapsed_ms":212,
 "stats_24h":{"total":1203,"info":1100,"warn":4,"error":9},
 "cost":{"today":19.40,"trailing_hour":1.12,"total_24h":21.3},
 "agents":[{"name":"pi-os","context_tokens":68224,"compactions":2,"tool_calls":120,"tool_errors":3,"orphan_calls":0,"spend_today":12.1,"top_model":"claude-opus-5-5"}]}
```

`agents` is present only for the pi source; `cost` only when a cost field is mapped.

## The pi source

`tailboard --source pi` reads every session file under `~/.pi/agent/sessions`, one event per line, and adds an Agents panel in the bottom right:

| column     | meaning                                                                 |
|------------|-------------------------------------------------------------------------|
| `agent`    | basename of the session's `cwd`; several sessions in one cwd roll up    |
| `$today`   | spend since local midnight                                              |
| `$/h`      | spend in the trailing 60 minutes                                        |
| `ctx`      | `usage.totalTokens` of the newest assistant turn in the newest session  |
| `cmp`      | compactions in the active window                                        |
| `err`      | tool results with `isError` in the window                               |
| `orphan`   | tool calls older than `orphan_after` with no result                     |
| `model`    | the model with the highest cost in the window                           |
| `fallback` | router tier changes that were fallbacks, over all tier changes          |
| `last`     | time since the agent's newest event                                     |

The Cost panel shows today's spend and the hourly rate, plus the split by model. The stream shows assistant turns, tool results, compactions (as warns), router state changes (fallbacks as warns) and the rest as `debug`, hidden by default (`d` shows them). `p` filters by model, `o` by tool, `t` pins the session of the focused row.

Event time is the entry's write time (`timestamp`), so an assistant turn's cost lands when the response finished.

Known limits:

- A tool call still running for longer than `orphan_after` (default 120 s) counts as orphaned until its result lands. Long `bash` results above that exist.
- No pane status: busy, idle or dead is not shown. A later phase joins `herdr agent list` on the session path and would add a `status` column.
- `om.*` custom entries carry personal content. They are stored as `debug` events with only `customType` retained; expand and copy show the full line, which is local-only behaviour.
- The `cwd` basename is the agent key, so two projects with the same directory name roll up together until the herdr join lands.

## Configure

`~/.config/tailboard/config.toml` is read first; the legacy `~/.config/axiom-tui/config.toml` is still read when the new one does not exist, and a legacy `[datasets.<name>]` table still works. Presets are saved to `~/.config/tailboard/presets.toml`.

```toml
# root (all existing keys unchanged)
default_source  = "pi"      # new; wins over default_dataset when both set
default_dataset = "name"    # legacy, still honoured
refresh_seconds = 15
stream_poll_ms  = 2000
log_buffer_size = 2000

[sources.<name>]
type           = "axiom" | "jsonl" | "pi"   # required
dataset        = "<name>"                   # axiom only; defaults to the source name
paths          = ["~/logs/*.jsonl", "/var/log/app/"]  # jsonl: required. pi: default ["~/.pi/agent/sessions"]
backfill       = "48h"                      # jsonl and pi: skip files whose mtime is older; default 48h
max_events     = 500000                     # jsonl and pi: store cap, oldest dropped; default 500000
max_line_bytes = 16777216                   # jsonl and pi: longer lines are skipped and counted; default 16 MiB
orphan_after   = "120s"                     # pi: age before a tool call with no result counts as orphaned; default 120s
route_prefixes = ["GET", "POST"]            # all types; default GET/POST/PUT/DELETE/PATCH for axiom, none otherwise
hide_levels    = ["debug"]                  # all types; stream filter at startup; default [] (pi: ["debug"])

[sources.<name>.fields]                     # jsonl and axiom; ignored for pi
time           = ["timestamp", "ts", "_time", "time"]  # first present wins
time_format    = ""                         # Go layout; empty = RFC3339Nano, RFC3339, epoch s, epoch ms auto
level          = ["level", "severity", "lvl"]
level_map      = { "WARNING" = "warn", "50" = "error" }  # applied after the built-in table
message        = ["message", "msg"]
group          = "fields.clientName"
cost           = "fields.costDollars"
cost_group     = ""                         # defaults to group
duration       = ["fields.durationMs"]
duration_unit  = "ms"                       # ms | s | us | ns; default ms
provider       = "fields.provider"
operation      = "fields.operation"
trace          = "fields.requestId"
interesting    = ["fields.userId", "fields.route"]
display_prefix = ""                         # stripped for display; default "fields." for axiom
```

Every key documented as a list also accepts a single string (`time = "ts"`). Durations are Go duration strings. `~` is expanded in `paths`.

Dot paths: `a.b.c` is tried first as a literal top-level key (Axiom flattens to `fields.x`), then as nested object navigation. Array indices are not supported.

Built-in level table, applied before `level_map`, case-insensitive: `error, err, fatal, critical, crit, panic, emerg, alert, 50, 60` to `error`; `warn, warning, 40` to `warn`; `info, notice, 30` to `info`; `debug, trace, verbose, 10, 20` to `debug`. Anything else is kept lowercased.

Time parsing without `time_format`: RFC3339Nano, RFC3339, then a number (integer or float): `>= 1e12` is epoch milliseconds, else epoch seconds. A missing or unparseable time uses ingestion time and increments the source's `untimed` counter, shown in the footer as `untimed N` when non-zero.

Capabilities follow the mapping: a `group` field unlocks the segmented throughput chart, the health bar and the `c` filter; `cost` unlocks the Cost panel; `duration` the avg/p95/max rows; `route_prefixes` the Top Routes panel. Panels for unmapped dimensions are not drawn.

### Files

Directories are walked recursively for `*.jsonl`, globs are re-evaluated every 5 s, and plain files are followed as is. Appends, truncation, rotation (a new file at the same path) and partial trailing lines are handled; lines longer than `max_line_bytes` are skipped and counted. Everything is kept in memory as compact events; the full line is re-read from disk only when you expand or copy an event.

## Axiom

An Axiom dataset is a source of type `axiom`. The legacy form still works unchanged:

```toml
default_dataset = "my-dataset"

[datasets.my-dataset]
# Fields surfaced inline on each log line (dotted keys, Axiom flattens structured logs).
interesting_fields = [
  "fields.userId",
  "fields.route",
  "fields.requestId",
]

# Optional: field used to color-code logs and segment the throughput chart (top 5 values).
group_by_field = "fields.userId"

# Optional: enables avg/p95/max latency in the stats panel.
duration_field = "fields.durationMs"

# Optional: prefixes that count as HTTP routes for the Top Routes panel.
route_prefixes = ["GET", "POST", "PUT", "DELETE", "PATCH"]
```

### Auth

Set one of these in your env:

| Env var           | Token type                       | Needs org ID?   |
|-------------------|----------------------------------|-----------------|
| `AXIOM_PAT`       | Personal token (`xapt-…`)        | Yes             |
| `AXIOM_TOKEN`     | API token (`xaat-…`)             | No              |

```bash
export AXIOM_PAT="xapt-…"
export AXIOM_ORG_ID="your-org-id"
```

PATs are preferred, they carry full user permissions. API tokens work but must explicitly grant the `Query` permission on your dataset.

`tailboard` will also auto-source `~/dev/.env.shared`, `~/Documents/.env.shared` and `~/.env` on startup if you keep secrets there.

## Keybinds

| key                   | action                                               |
|-----------------------|------------------------------------------------------|
| `?`                   | help overlay                                         |
| `space`               | pause / resume the live stream                       |
| `j` / `k` / ↓↑        | scroll logs                                          |
| `pgdn` / `pgup`       | page                                                 |
| `g` / `G`             | jump to top / bottom (G resumes tail)                |
| `h` / `l` / ←→        | cycle focus between panels                           |
| `tab`                 | cycle focus forward                                  |
| `e` / `w` / `i` / `d` | toggle errors / warns / infos / debug visibility     |
| `/`                   | search messages (substring, case-insensitive)        |
| `c`                   | filter by group (client, agent)                      |
| `p` / `o`             | filter by provider / operation (model / tool for pi) |
| `t`                   | pin the trace field of the focused row               |
| `T`                   | cycle the time range (15m / 1h / 6h / 24h)           |
| `D`                   | source switcher                                      |
| `s` / `1-9`           | save the filter as a preset / load a preset          |
| `esc` / `R`           | reset filters and resume tail                        |
| `enter`               | expand log as JSON (on top errors: drill-in)         |
| `y`                   | copy expanded event as JSON                          |
| `r`                   | force refresh all queries                            |
| `q` / `ctrl-c`        | quit                                                 |

## Why

Logs are cheap. Reading them is expensive. Most teams end up in a web UI (great for exploration, slow for monitoring) or shell tailing JSON (fast, no structure). This is the middle path: a single dense view of what's happening right now with enough interactivity to investigate without leaving the terminal.

## License

MIT, see [LICENSE](LICENSE).
