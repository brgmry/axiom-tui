# tailboard

A terminal dashboard for any log stream: JSONL files on disk, [pi](https://github.com/badlogic/pi-mono) agent sessions, or an [Axiom](https://axiom.co) dataset.

One screen shows the live stream, counts by level, throughput, the error trend, the noisiest messages and, for agent sessions, a per-agent table of spend, context size and failures. Everything runs locally. Nothing leaves the machine unless you configure an Axiom source.

```
 client health (15m):   ● acme 2/1    range: 1h
┌────────────────────────────────────────────────┐┌─────────────────────────────────┐┌─────────────────────────────────┐
│ JSONL Live [fake] ● tailing                    ││Last 1h ●  120 events  (~2.0/min)││Cost (1h)                        │
│17:09:22 INFO  acme hello route=/x              ││INFO    ██████░░     100   83.3% ││$  19.40 acme                    │
│                                                ││WARN    █░░░░░░░      15   12.5% ││                                 │
│                                                ││ERROR   █░░░░░░░       5    4.2% ││$  19.40 total                   │
│                                                ││durationMs  avg 12ms   p95 40ms  ││                                 │
│                                                │└─────────────────────────────────┘└─────────────────────────────────┘
│                                                │┌────────────────────────────────────────────────────────────────────┐
│                                                ││ Throughput                                                         │
│                                                ││3│                    ⢀⡠⢄                          ⢀⡠⢄              │
│                                                ││ │                ⢀⡠⠔⠊⠁ ⠈⢢                     ⢀⡠⠔⠊⠁ ⠈⢢             │
│                                                ││2│            ⢀⡠⠔⠊⠁       ⠱⡀               ⢀⡠⠔⠊⠁       ⠱⡀           │
│                                                ││ │        ⢀⡠⠔⠊⠁            ⠘⢄          ⢀⡠⠔⠊⠁            ⠘⢄          │
│                                                ││1│    ⣀⠤⠒⠉⠁                 ⠈⢢     ⣀⠤⠒⠉⠁                 ⠈⢢     ⣀⠤⠒⠉│
│                                                ││ │⣀⠤⠒⠉                        ⠱⣀⠤⠒⠉                        ⠱⣀⠤⠒⠉    │
│                                                ││0└──────────────────────────────────────────────────────────────────│
│                                                ││ 16:09:22    16:19:11    16:29:00    16:38:49    16:48:38           │
│                                                ││━━ acme  ━━ beta                                                    │
│                                                │└────────────────────────────────────────────────────────────────────┘
│                                                │┌────────────────────────────────────────────────────────────────────┐
│                                                ││ Errors & Warns (6h)                                                │
│                                                ││3│                  ⢀⣀⠤⠔⠢⡀                       ⢀⣀⠤⠔⠢⡀             │
│                                                ││ │            ⣀⣀⠤⠔⠒⠉⠁    ⠈⠢⡀               ⣀⣀⠤⠔⠒⠉⠁    ⠈⠢⡀           │
│                                                ││2│     ⢀⣀⠤⠔⠒⠊⠉             ⠈⠢⡀      ⢀⣀⠤⠔⠒⠊⠉             ⠈⠢⡀      ⢀⣀⠤│
│                                                ││ │⣀⠤⠔⠒⠉⠁                     ⠈⠢⣀⠤⠔⠒⠉⠁                     ⠈⠢⣀⠤⠔⠒⠉⠁  │
│                                                ││0└──────────────────────────────────────────────────────────────────│
│                                                ││ 11:09:22    12:08:16    13:07:11    14:06:05    15:05:00           │
│                                                ││━━ Errors  ━━ Warns                                                 │
└────────────────────────────────────────────────┘└────────────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────┐┌──────────────────────────────────────┐┌──────────────────────────────────────┐
│ Top Errors & Warns (1h)              ││ Recent Issues                        ││ Top Routes (1h)                      │
│   7  ERROR  boom                     ││17:09:22 WARN  slow                   ││    3  GET /health                    │
│                                      ││                                      ││                                      │
│                                      ││                                      ││                                      │
│                                      ││                                      ││                                      │
│                                      ││                                      ││                                      │
└──────────────────────────────────────┘└──────────────────────────────────────┘└──────────────────────────────────────┘
? help  space pause  / search  T range  D source  esc reset  tab focus  enter expand  q quit
                                                          ▂▅ █  ▂▅█  ▂  errs  focus: logs  source: fake  refresh: 0s ago
```

A 120x40 frame rendered from the view tests' fake source, ANSI stripped; it is the golden in `testdata/view/frame-120x40.golden`. With the pi source the bottom row is Top Errors beside an Agents panel, one row per agent.

## Quickstart

Requires Go 1.26.0 or later.

```bash
go install github.com/brgmry/tailboard@latest
```

The `go install` path resolves once the repository rename to `tailboard` lands. Until then, or to build from a checkout:

```bash
git clone https://github.com/brgmry/tailboard.git
cd tailboard
make install                      # builds and copies to ~/.local/bin/tailboard
```

No config is needed for files or for pi sessions:

```bash
tailboard app.jsonl               # follow one JSONL file
tailboard /var/log/svc/ ~/logs/*.jsonl   # directories (recursive, *.jsonl) and globs
tailboard --source pi             # pi agent sessions in ~/.pi/agent/sessions
tailboard --source pi --check     # headless: print a JSON summary and exit
```

Press `?` for the key map, `T` to widen the time range, `/` to search, `enter` to expand a row, `q` to quit.

Other flags:

```bash
tailboard                         # the only configured source, else pi when its directory exists
tailboard --source NAME           # a source from config (or a path)
tailboard --dataset NAME          # an Axiom dataset
tailboard --interval 5            # refresh the panels every 5 s
tailboard --config FILE           # config path
tailboard --env FILE              # load KEY=value pairs from FILE (nothing is loaded otherwise)
```

Source resolution order: `--dataset`, `--source`, positional paths, `default_source`, `default_dataset`, `$AXIOM_DATASET`, the only configured source, then the built-in `pi` source when `~/.pi/agent/sessions` exists.

## Sources

### Files

A `jsonl` source follows files, directories and globs. Directories are walked recursively for `*.jsonl`, globs are re-evaluated every 5 s, plain files are followed as is. Appends, truncation, rotation (a new file at the same path) and partial trailing lines are handled; lines longer than `max_line_bytes` are skipped and counted.

Events are kept in memory as compact records, about 380 bytes per stored event, so the default `max_events = 500000` is a budget of about 190 MB per source. Lower `max_events` to cap it; the oldest events are dropped first. The full line is re-read from disk only when you expand or copy an event. A 100 MB file loads in well under a second.

Field names are mapped in config (see below). With no mapping, `timestamp`/`ts`/`time`, `level`/`severity`/`lvl` and `message`/`msg` are tried, which covers pino, zap, slog and most structured loggers.

### pi agent sessions

`tailboard --source pi` reads every session file under `~/.pi/agent/sessions`, one event per line, and adds an Agents panel:

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
- No pane status column yet: whether an agent is busy, idle or dead is not shown.
- `om.*` custom entries carry personal content. They are stored as `debug` events with only `customType` retained; expand and copy show the full line, which is local-only behaviour.
- The `cwd` basename is the agent key, so two projects with the same directory name roll up together.

### Axiom

An Axiom dataset is a source of type `axiom`. See [Axiom](#axiom-1) below for auth and the legacy config form.

## Configuration

Config lives at `~/.config/tailboard/config.toml`. The legacy `~/.config/axiom-tui/config.toml` is still read when the new one does not exist, and a legacy `[datasets.<name>]` table still works. Presets are saved to `~/.config/tailboard/presets.toml`. A complete starting point is in [`example.config.toml`](example.config.toml).

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

The built-in `pi` source needs no config entry. A `[sources.pi]` entry replaces it and only needs `type = "pi"` plus the keys you change.

## Axiom

An Axiom source queries the dataset with APL. The legacy form still works unchanged and produces the same queries as before the rename:

```toml
default_dataset = "my-dataset"

[datasets.my-dataset]
# Fields surfaced inline on each log line (dotted keys, Axiom flattens structured logs).
interesting_fields = [
  "fields.userId",
  "fields.route",
  "fields.requestId",
]

# Optional: field used to colour-code logs and segment the throughput chart (top 5 values).
group_by_field = "fields.userId"

# Optional: enables avg/p95/max latency in the stats panel.
duration_field = "fields.durationMs"

# Optional: prefixes that count as HTTP routes for the Top Routes panel.
route_prefixes = ["GET", "POST", "PUT", "DELETE", "PATCH"]
```

The same dataset as a source, with the field mapping under `fields`:

```toml
[sources.prod]
type    = "axiom"
dataset = "prod-logs"

[sources.prod.fields]
interesting = ["fields.userId", "fields.route", "fields.requestId"]
group       = "fields.userId"
duration    = ["fields.latencyMs"]
```

An axiom source with no `fields` keys gets the defaults the tool always had: time `_time`, level `level`, message `message` then `msg`, cost `fields.costDollars` by `fields.clientName`, provider `fields.provider`, operation `fields.operation`, trace `fields.requestId`, display prefix `fields.`.

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

To keep tokens in a file, pass `--env FILE`: its `KEY=value` lines are loaded on startup without overwriting variables already set. No env file is read unless you pass one.

## `--check`

Opens the source, waits for the first full backfill pass (file sources) or runs one stats query (Axiom), prints one JSON object and exits 0. This is the output of `go run . testdata/generic/sample.jsonl --check` from a checkout; only `elapsed_ms` varies. The sample's timestamps are old, so only its one untimed event falls inside `stats_24h`:

```json
{"source":"testdata/generic/sample.jsonl","kind":"jsonl","files":1,"events":199,"skipped_lines":0,"untimed":1,"parse_errors":1,"unreadable_files":0,"elapsed_ms":21,"stats_24h":{"total":1,"info":0,"warn":1,"error":0}}
```

A pi source adds `cost` and `agents`, for example:

```json
"cost":{"today":19.4,"trailing_hour":1.12,"total_24h":21.3},
"agents":[{"name":"pi-os","context_tokens":68224,"compactions":2,"tool_calls":120,"tool_errors":3,"orphan_calls":0,"spend_today":12.1,"top_model":"claude-opus-5-5"}]
```

`agents` is present, possibly empty, exactly when the source is an agent source; `cost` only when a cost field is mapped. Keys are stable.

## Keybindings

| key                       | action                                                   |
|---------------------------|----------------------------------------------------------|
| `?`                       | help overlay                                             |
| `space`                   | pause / resume the live stream                           |
| `j` / `k` / ↓↑            | scroll logs                                              |
| `pgdn` / `pgup`           | page down / up                                           |
| `ctrl+d` / `ctrl+u`       | page down / up                                           |
| `g` / `home`              | jump to the oldest page                                  |
| `G` / `end`               | jump to the tail and resume the stream                   |
| `h` / `l` / ←→            | focus the previous / next panel                          |
| `tab`                     | cycle focus forward                                      |
| `e` / `w` / `i` / `d`     | toggle errors / warns / infos / debug visibility         |
| `/`                       | search messages (substring, case-insensitive)            |
| `c`                       | filter by group (client, agent)                          |
| `p` / `o`                 | provider / operation substring filter (pi: model, tool)  |
| `t`                       | pin the trace field of the focused row                   |
| `R` / `C`                 | reset filters                                            |
| `esc`                     | close a modal; otherwise reset filters and resume tail   |
| `T`                       | cycle the time range (15m / 1h / 6h / 24h)               |
| `D`                       | source switcher                                          |
| `s` / `1-9`               | save the filter as a preset / load a preset              |
| `enter`                   | expand log as JSON (on top errors: drill-in)             |
| `y`                       | copy the selected or expanded event as JSON              |
| `r`                       | force refresh all queries                                |
| `q` / `ctrl+c`            | quit                                                     |

The mouse works too: click a panel to focus it, click a top error to filter the stream by it, right-click a stream row to copy it.

## Adding a source

The UI depends on one interface, `Source` in `source.go`. A backend implements it, registers a constructor under its type name and declares its config defaults:

1. Implement `Source`: `Info()` describes the source (kind, capabilities, group label, filter fields, hidden levels), the query methods return the aggregates for a lookback window, `Stream` pages the live events with an opaque `Cursor`, `Hydrate` returns the full raw object for expand and copy, `Close` releases resources. Methods for a capability the source lacks return their zero value and a nil error; set only the `Caps` bits the source supports and the layout follows them.
2. Optionally implement `AgentSource` (adds the Agents panel) and `ProgressSource` (the loading footer and the `--check` counters).
3. Register the type from an `init()` in the backend's file: `sourceOpeners["mytype"] = func(name string, sc SourceConfig) (Source, error) { ... }`. `axiom.go`, `jsonl.go` and `pi.go` each do this, and the registry's keys are the valid values of `type`.
4. Add a `case "mytype":` to `applyTypeDefaults` in `config.go` that fills the type's defaults for every key left empty. A source that reads files calls `applyFileDefaults` there, as `jsonl` and `pi` do.
5. Add tests: a fixture under `testdata/`, the source's queries against it, and a `--check` run.
6. Document the type here: a section under [Sources](#sources) and its keys in [Configuration](#configuration).

`jsonl.go` is the smallest complete example: it wires the tailer, the store and the field map. `pi.go` shows a source that reuses the file machinery with its own line handler and side table. Nothing in `model.go` or `view.go` needs to change for a new type.

## Development

`make check` runs gofmt, vet, staticcheck and race tests, the same gate as CI. `make test` and `make lint` run the race tests and staticcheck on their own.

## Why

Logs are cheap. Reading them is expensive. Most teams end up in a web UI (good for exploration, slow for monitoring) or shell-tailing JSON (fast, no structure). tailboard is the middle: a single dense view of what is happening right now, with enough interactivity to investigate without leaving the terminal, and the same screen whether the stream comes from a file, an agent or a hosted dataset.

## Licence

MIT, see [LICENSE](LICENSE).
