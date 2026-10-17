# CHG-0113: Watcher probe pipeline

Chain: ADR-0043 D1-D5 -> SPEC-0057 R1-R4 -> this plan -> state.yaml.
Issue: GitHub #248 (source watchers; probe-side scope — MCP live connector
acceptance stays with #257).

## Goal

Watchers separate fetch from extraction: `probe = fetcher + extract[]`.
HTTP stays the cheap default; script/command probes run deterministic
Starlark inside the existing disposable container, with file-state probes
confined to the owner's `watch/` directory. Recipes snapshot reviewed
presets at create time. Browser rendering is an opt-in external service
outside this change.

## Preconditions and rules

Worktree `hermes-hub-248` on branch `feat/issue-248-watcher-probe-pipeline`.
Red tests before changes (Probity enforces). No arbitrary shell execution in
probes; no network/filesystem/credentials for probe scripts; durable
baseline/dedup/receipt semantics unchanged.

## Slices

### S1: Extract pipeline (R1)

`extract.go`: ordered steps css/html2text/regex/grep/drop_regex/drop_lines/
sort_lines/strip/json_path/rss; create-time validation; empty/failed
pipeline = probe error. `probeValue` delegates; `kind=github_issue` and
legacy `regex` parity preserved. — DONE.

### S2: Isolated probe scripts (R2)

`WatchProbe.Script`, `kind=script` (fetch → optional in-process extract →
`input()` in disposable runtime → exactly one emit) and `kind=command`
(no fetch, `watch/` bind-mounted read-only at `/watch`, file builtins
`read_file`/`list_dir`/`hash_path` confined to root). `ScriptRequest.Input`
+ `MountSpace` through supervisor argv; helper decodes both.
Gateway wires `spool.probeScript = g.runProbeScript`. — DONE.

### S3: Recipes (R3)

Builtin catalog (`rss_feed`); create-time expansion snapshot with
`recipe_version`; explicit fields win; unknown recipe rejected. — DONE.

### S4: Agent surface + validation (R2.3, R3.2)

`routine_create` forwards `recipe`/`extract`/`probe_script`; probe script
syntax checked at create and restricted to script/command kinds. — DONE.

### S5: Verification

`just check`, `just security` (new cascadia dep), docs update. Live
dev-stack acceptance for css/script/command probes remains to run.

## Out of scope

`kind=render` external browser service, per-site recipe catalog growth,
intent-based LLM diff filtering, MCP connector live acceptance (#257).
