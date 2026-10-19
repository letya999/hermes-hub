# SPEC-0059: Watcher probe pipeline (fetch → extract → compare → deliver)

Frozen: 2026-10-18. Amendment to SPEC-0048/0054.
Decision: [ADR-0045](../../docs/adr/ADR-0045-watcher-probe-pipeline.md).

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

## Amendment 2026-10-18 (CHG-0117): probe source spectrum and delivery modes

- R5.1: `probe.kind=tool` is accepted as an alias and normalized to
  `mcp_tool` at create time. The ToolHub `/v1/tool-call` path already
  restricts probes to read-effect tools under the owner's grants, so any
  projected read tool (file_read, knowledge_search, web_search connectors,
  MCP such as job_ftch) is a lawful probe source without new plumbing.
- R5.2: `probe.needs` is a JSON array of at most 4 `{tool, args}` read-tool
  calls, lawful only on `script`/`command` kinds. The hub prefetches them
  under the owner binding before spawning the sandbox; each result is
  truncated to 64 KiB and exposed inside Starlark via `needs()` in declared
  order. A failed need aborts the run and settles as a probe error; the
  script container itself stays networkless.
- R5.3: `schedule.deliver_mode` is `notify` (default: old/new on change),
  `content` (the probed value is delivered every successful tick) or
  `artifact` (the value is staged as a durable outbox blob and delivered as
  a file each tick; `artifact_name` sets the filename and rejects path
  traversal). Non-notify modes require a watcher/check context; unknown
  modes are rejected at create.
- R5.4: `probe.kind=render` ships the *contract*, not a renderer: the hub
  POSTs `{kind, url, args, steps, wait_for}` to an operator-registered
  endpoint and feeds the bounded (512 KiB) response through the normal
  extract chain. `steps` is a declarative browser-action array (≤8 entries;
  actions click/fill/scroll/wait/wait_for/evaluate) replayed by
  the service before extraction. The render endpoint stays opt-in and out
  of the base image.
- R5.5: `probe.kind=service` addresses internal services (job_ftch API,
  custom crawlers) the same way, without requiring a target URL. Both
  kinds resolve the target exclusively from the operator-registered
  `service_endpoints` catalog (YAML or `HUB_SERVICE_ENDPOINTS` env JSON:
  `name → url | {url, auth}`); names match `^[a-z0-9_-]{1,64}$`, URLs are
  http(s) with a host and no embedded credentials, and a schedule can never
  supply the endpoint itself. Unregistered names, non-2xx and oversized
  responses are probe errors. The same service path seeds the baseline for
  `job_kind=check` creation.
