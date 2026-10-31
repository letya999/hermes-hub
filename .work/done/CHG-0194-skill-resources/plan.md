# CHG-0194: M24 #362 S7 — resources and script execution

Chain: `docs/adr/ADR-0085-agent-skills-platform.md` D1/D7/D13 ->
`specs/active/SPEC-0084-agent-skills-platform.md` R7 -> this plan -> `state.yaml`.
Previous stage: `CHG-0193`. Next stage: `CHG-0195` (secrets via broker).

## Goal

Skill resources are servable and scripts are bounded. `skill_view` returns
bounded text from `references/`/`templates/`/`examples/`; lint enforces
one-level references and load-time-exec absence. The effective view mounts
read-only into CLI cells (`/skills`), scripts run only from a scratch copy
under /work. A `skills:` read-only logical root serves hub file/document/
image executors. Dependency declarations are validated against the
operator-allowed registries; `skill()` reports execution readiness.

## Read before coding

1. SPEC-0084 R7.*, ADR-0085 D1/D7, `.work/done/CHG-0193/plan.md`.
2. `internal/agenttools/skills.go` (skillView/skillInvoke),
   `internal/agenttools/tools_fs.go` (rootOf/fileScoped),
   `internal/skillstore/scan/scan.go` + `skillstore.go` publish gate,
   `internal/toolhub/cell_init.go` cellArgv + cellWorkspaceBind +
   cellProfileProblem, `internal/stack/compose.go` cliControllerSection.

## Preconditions and rules

- Red test first, then fix. No Docker this session.
- Deps/env/bins stay readiness signals — they never grant or execute.
- Nothing executes at load; the ro mount and lint make that structural.

## Slices

### S7.1 Bounded skill_view + resource dirs (R7.1)

- `skill_view path=` restricted to `references/`, `templates/`, `examples/`;
  text output capped (~32 KiB, `truncated` flag). Listing stays tree-wide.
- `skill()` output already lists `resources` — keep, plus `has_scripts`.

### S7.2 Lint: one-level refs + no load-exec (R7.1, R7.6)

- Publish/draft lint (`skillstore`): markdown links in SKILL.md must
  resolve to tree files under the resource dirs (or external/anchor);
  files under `references/`/`templates/`/`examples/` may not link to
  other tree files.
- Scanner rule: backticked `!cmd` load-time execution marker -> finding.

### S7.3 Deps validation (R7.4)

- Parse `metadata.deps` (`pip:`/`npm:`/`system:`) into `Requires.Deps`;
  malformed/injection-shaped entries dropped (lint finding).
- Render stamps `deps_ok`/`missing` against `s.CLIRegistries` scheme map
  (pip->pypi, npm->npm, system->toolbox); unmet deps are reported by
  `skill()` as a readiness signal, not catalog-ineligible.

### S7.4 skill() readiness reporting (R7.5)

- `skill()` returns `readiness`: `{missing, deps_missing, instructions_only,
  has_scripts}`. `instructions_only` when scripts exist but no exec-capable
  grant (`terminal` toolset or CLI bash cell) — stamped at render.

### S7.5 `skills:` ro root (R7.3)

- `rootOf`/`fileScoped` + document/image executor roots accept `skills`;
  list/search/read allowed, write rejects "skills is read-only". Copying
  to workspace stays explicit via file_write.

### S7.6 `/skills` ro mount into CLI cells (R7.2)

- `cliCell.skillsHost` bound ro at `/skills` from
  `<SpacesRoot>/<principal>/generated/skills-effective` when the request
  carries a principal scope and the dir exists; cellProfileProblem admits
  the mount. Scripts execute only from a copied path under /work.

### S7.7 Docs + state

- `internal/systemguide/SKILL.md`, `docs/spaces.md` (`/skills` in cells,
  `skills:` root), `docs/tools.md` if the file/doc/image root arg changes,
  state.yaml, index row.

## Verification

- `go test ./internal/skillstore ./internal/agenttools ./internal/stack
  ./internal/toolhub ./internal/communication`
- `just check` (>=85% overall, package floors)
