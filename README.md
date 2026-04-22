# axiom-tui

A terminal dashboard for [Axiom](https://axiom.co) datasets. Live log stream, aggregate charts, and interactive drill-in — all keyboard driven. Plug any dataset in via config.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) + [Lipgloss](https://github.com/charmbracelet/lipgloss).

```
┌──────────────────────────┬──────────────────────────┐
│                          │   Last Hour  [donut]     │
│       LIVE LOGS          │   info / warn / errors   │
│   (filter, search,       ├──────────────────────────┤
│    pause, expand)        │   Throughput chart       │
│                          ├──────────────────────────┤
│                          │   Errors & Warns trend   │
├──────────┬───────────────┴──────────┬───────────────┤
│ Top      │  Recent Issues           │  Top Routes   │
│ Errors   │                          │               │
└──────────┴──────────────────────────┴───────────────┘
 ? help  space pause  e/w/i toggle  / search  c client  tab focus  enter expand  r refresh  q quit
```

## Install

```bash
git clone git@github.com:brgmry/axiom-tui.git
cd axiom-tui
make install        # builds + copies to ~/.local/bin/axiom-tui
```

Requires Go 1.22+.

## Configure

Create `~/.config/axiom-tui/config.toml`:

```toml
default_dataset = "my-dataset"
refresh_seconds = 15
stream_poll_ms  = 2000
log_buffer_size = 2000

[datasets.my-dataset]
# Fields surfaced inline on each log line (dotted keys — Axiom flattens structured logs).
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

## Auth

Set one of these in your env:

| Env var           | Token type                       | Needs org ID?   |
|-------------------|----------------------------------|-----------------|
| `AXIOM_PAT`       | Personal token (`xapt-…`)        | Yes             |
| `AXIOM_TOKEN`     | API token (`xaat-…`)             | No              |

```bash
export AXIOM_PAT="xapt-…"
export AXIOM_ORG_ID="your-org-id"
```

PATs are preferred — they carry full user permissions. API tokens work but must explicitly grant the `Query` permission on your dataset.

`axiom-tui` will also auto-source `~/Documents/.env.shared` and `~/.env` on startup if you keep secrets there.

## Run

```bash
axiom-tui                                 # uses config default
axiom-tui --dataset other-project         # override dataset
axiom-tui --interval 5                    # override refresh
axiom-tui --config /path/to/config.toml   # custom config path
axiom-tui --env /path/to/.env             # source an extra env file
```

## Keybinds

| key            | action                                           |
|----------------|--------------------------------------------------|
| `?`            | help overlay                                     |
| `space`        | pause / resume the live stream                   |
| `j` / `k` / ↓↑ | scroll logs                                      |
| `pgdn` / `pgup`| page                                             |
| `g` / `G`      | jump to top / bottom (G resumes tail)            |
| `h` / `l` / ←→ | cycle focus between panels                       |
| `tab`          | cycle focus forward                              |
| `e` / `w` / `i`| toggle errors / warns / infos visibility         |
| `/`            | search messages (substring, case-insensitive)    |
| `c`            | filter by client (group-by field)                |
| `C`            | clear all filters                                |
| `enter`        | expand log → JSON modal (on top errors: drill-in)|
| `y`            | copy expanded event as JSON                      |
| `r`            | force refresh all queries                        |
| `q` / `ctrl-c` | quit                                             |

## Why

Logs are cheap. Reading them is expensive. Most teams end up in the Axiom web UI (great for exploration, slow for monitoring) or shell tailing JSON (fast, no structure). This is the middle path: a single dense view of what's happening right now with enough interactivity to investigate without leaving the terminal.

## License

MIT — see [LICENSE](LICENSE).
