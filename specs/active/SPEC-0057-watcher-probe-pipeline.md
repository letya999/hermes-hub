# SPEC-0057: Watcher probe pipeline (fetch → extract → compare → deliver)

Frozen: 2026-10-18. Amendment to SPEC-0048/0054.
Decision: [ADR-0043](../../docs/adr/ADR-0043-watcher-probe-pipeline.md).

## Requirements

- R1.1: A watcher probe is a fetcher plus an ordered deterministic extract
  pipeline. Fetchers: `http` (default, unchanged SSRF/redirect/credential
  protections), `mcp_tool` (existing ToolHub path), `script`, `command`.
  Fetch and extraction are separate stages; the compared value is the last
  stage's canonical output.
- R1.2: `extract` is a JSON array of at most 8 steps, applied in order.
  Steps: `css`, `html2text`, `regex` (first capture group), `grep`,
  `drop_regex`, `drop_lines`, `sort_lines`, `strip`, `json_path`, `rss`.
  Steps are pure functions over the previous output — no network, execution,
  randomness or wall clock. Unknown kinds, selector-less css, non-compiling
  or capture-less regex, invalid json_path and malformed feeds are rejected
  at create time; a step failing or producing empty output at probe time is
  a probe error, never silently "unchanged".
- R1.3: Legacy `probe.regex` without `extract` keeps exact prior semantics;
  `probe.kind=github_issue` is preserved.
- R2.1: `kind=script` fetches the URL first, optionally shrinks the body
  through the in-process `extract` chain, then runs `probe.script` in the
  isolated disposable runtime with the fragment as `input()`. The script
  must emit exactly one value, which is the compared watch value.
- R2.2: `kind=command` never fetches a URL. It runs `probe.script` in the
  same isolated runtime with the owner's `spaces/<user>/watch` directory
  bind-mounted read-only at `/watch`. Builtins `read_file`, `list_dir` and
  `hash_path` are confined to that root; traversal and absolute paths are
  rejected. The mount source is derived from the authenticated owner
  binding, never from request text.
- R2.3: The isolated runtime keeps its existing bounds: no network,
  read-only root, unprivileged user, all capabilities dropped, bounded
  memory/CPU/pids/wall time, bounded source (16 KiB), input (~64 KiB) and
  emitted output. Script errors and runtime unavailability settle as probe
  failures (`failed` + `LastError`), distinguishable from "no change", and
  share the durable receipt/replay/baseline/dedup path of every probe.
- R3.1: `probe.recipe` names a reviewed builtin preset. At create time the
  recipe expands into concrete probe fields stored on the schedule
  (`recipe_version` recorded); catalog changes never silently alter existing
  schedules. Explicit `extract` wins over the recipe default. Unknown recipes
  are rejected.
- R3.2: `probe.script` is required and syntax-checked at create for
  `script`/`command` kinds and rejected on other kinds.
- R4.1: Browser rendering (`kind=render`) is a separate opt-in external
  service, not part of the base image; this spec does not ship it. LLM
  evaluation of diffs stays opt-in escalation (`notify_mode=agent`) and is
  never the deterministic change detector.
