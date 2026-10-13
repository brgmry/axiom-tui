# Changelog

## Unreleased

### tailboard (renamed from axiom-tui)

The project is now `tailboard`: a source-agnostic terminal dashboard. The module path is `github.com/brgmry/tailboard` and the binary is `tailboard`.

Added

- A `Source` interface. The UI depends only on it; Axiom is one backend behind it.
- A `jsonl` source that follows files, directories and globs with an in-memory store: appends, truncation, rotation, partial lines, long lines and a backfill window are handled. All field names are mapped in config.
- A built-in `pi` source for pi agent sessions with an Agents panel: spend today and per hour, context size, compactions, tool errors, orphaned tool calls, model mix and router fallback share.
- `[sources.<name>]` config with `type`, `paths`, `backfill`, `max_events`, `max_line_bytes`, `orphan_after`, `route_prefixes`, `hide_levels` and a `[sources.<name>.fields]` mapping.
- `--source NAME`, positional paths (`tailboard app.jsonl`), and `--check` for a headless JSON summary.
- The `d` key toggles debug events; panel titles and rates follow the active time range; the Cost panel shows today, trailing hour and the split by model when the source has them.
- Golden tests for the Axiom query strings, an oracle test for the store, and ingest and query benchmarks.

Changed

- Config is read from `~/.config/tailboard/config.toml`; the legacy `~/.config/axiom-tui/config.toml` is still read when the new one does not exist. Presets are written to the new directory.
- The `D` switcher lists sources instead of datasets. The footer shows `source:`.
- Panels for dimensions a source does not map (cost, group, routes) are not drawn.
- Env files are loaded only from `--env FILE`. `~/dev/.env.shared`, `~/Documents/.env.shared` and `~/.env` are no longer sourced implicitly; pass one with `--env` or export the variables.

Fixed

- The legend under the segmented throughput chart is cut by display width, so three or more series no longer corrupt the panel row.
- Panels refresh on the first stream tick after a backfill that finished before any tick saw it loading, instead of showing zero for a full refresh interval.
- The agents panel budgets its header row, so five or more agents no longer lose the lowest-spend row; past the panel's maximum height it says how many are not shown.
- Messages with newlines render on one row in the stream, top errors, recent issues and top routes.
- A lock-order inversion between the tailer and the pi handler, reachable when a session file reappeared at its path during an Agents refresh.
- `--check` prints `agents` as an empty array for an agent source with no agents, and rounds money to four decimals.
- The stream's scroll offset follows the filtered list, so pinning a trace while scrolled up shows the pinned row; `g` shows the oldest page.
- Config errors no longer carry a doubled `config:` prefix.

Migration

- Nothing is required. An existing `[datasets.<name>]` table and `default_dataset` keep working and produce the same Axiom queries. To use the new keys, move the entry to `[sources.<name>]` with `type = "axiom"` and the field mapping under `[sources.<name>.fields]`.
- Rename the config directory to `~/.config/tailboard/` when convenient; nothing breaks if you do not.
