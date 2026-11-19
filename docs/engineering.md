---
description: Engineering rules — Go conventions, gates, pins, change process, boundaries on dependencies.
last_verified: 2026-11-19
---

# Engineering rules

## Language and layout

Go for hubctl, MCP tooling, runtime supervision and dev checks. Layout:
`cmd/<binary>` entrypoints, `internal/<domain>` packages, leaf packages
with parser-enforced import boundaries (`boundary_test.go`) — the import
direction is fixed by test, not convention. `services/credential-broker` is
a separate copied Go module (MIT) kept out of the flatten.

## Gates

- `just check` before delivery: format, docs (numbering/index/verified/size), dependency gate, vet.
  Run affected packages with `just test <packages>`; concurrency changes also need
  targeted race tests. One repo-wide check at a time across local worktrees.
- CI owns full race/shuffle tests, actionlint, static/security analysis, deployment
  contracts and coverage ≥85% on owned code / ≥75% per internal package.
  `just check-full` is the explicit exhaustive local fallback, not the edit loop.
- `just security` when dependencies change (govulncheck + npm audit).
- CI image/container jobs provide image-side acceptance. `just docker-check`
  consumes exact-HEAD images; `docker-check-build` explicitly builds locally.
- Report an unavailable gate as unavailable — never claim it by proxy.

## Pins

- Hermes `0.21.0` @ `869228cab` (ADR-0012). CLIProxy
  `c5251b57b3fa5dcbc689f514f4aa166daacb4481`. ToolHive v0.48.0 / cellinit
  per ADR-0013. Full pin table: docs/integrations.md.
- Go toolchain pinned uniformly across host and image builds.
- Staticcheck pinned to upstream commit `452d5bb86b45`
  (v0.7.0-0.dev.0.20261009230814-452d5bb86b45): release v0.8.1 cannot
  decode this toolchain's export data — the compatible upstream tool is
  used without disabling checks.

## Change process

- Significant changes: `.work/in-progress/CHG-NNNN-name/{plan.md,
  state.yaml}`; milestones follow `.work/milestone-playbook.md`.
- Accepted ADRs are append-only — amend by a new ADR, never rewrite.
- SPECs are frozen requirements; the code must match them or the spec
  says what is intentionally deferred.
- Regression tests are mandatory for file-isolation, concurrency,
  authorization or HTTP-contract changes.
- Agent-tool contract changes ship in one changeset: tool description,
  prompt-contract guidance (`internal/stack/prompt_contract.go`),
  `docs/tools.md` and `docs/routines.md` entries, and a bumped immutable
  `hub-agent-tools` catalog version — followed by a deliberate repin and
  runtime respawn on managed spaces. The suite fails a cataloged tool
  missing from `docs/tools.md`.
- Red→green where the probity hook applies: behavioral assertion failure
  first, then the implementation.

## Dependency and code hygiene

`devcheck deps` checks every binary's transitive dependency closure from
`go list -deps`, including paths through shared helpers. Runtime, supervisor
and communication cannot reach `internal/toolhub` or its automation server.
Contracts may reach only stdlib, other contracts and the identity primitives;
third-party imports and graph-load failures are errors too.

`internal/devcheck/deps-baseline.txt` records the remaining module-local
closure. New dependencies and stale baseline entries both fail. After a
reviewed change, use `go run ./cmd/devcheck deps-baseline` to regenerate it;
generation refuses hard forbidden edges. `hubctl` owns operator store
migration after render; stack rendering and agent-tools never open ToolHub's
store. The HTTP automation client and read-only legacy-spool bridge live in
`commops`, apart from the automation engine.

The first contracts in `contracts/*/v1` expose DTOs, a client interface,
copying in-process and HTTP transports, typed errors and a shared conformance
suite. Each owning module runs that suite against its real implementation:

| Contract | Owner | Current consumer |
|---|---|---|
| `task/v1` | Supervisor job ledger | ToolHub run authority |
| `identity/v1` | Control-zone identity registry | Gateway channel authority, supervisor templates and automation routing |
| `toolsurface/v1` | ToolHub owner-scoped store catalog | Reference adapter; callers must authenticate the envelope |

The identity read handler runs in control; verified-ingress link mutations have
a separate credential. The toolsurface adapter does not publish an additional
HTTP route. A contract handler's service bearer authenticates its caller; it
does not prove that an arbitrary envelope in the body belongs to that caller.
`RunContext` and `CatalogEntry` compatibility aliases preserve current callers
while the wire DTOs belong to contracts. Remaining `toolhub/api` logic stays on
the dependency baseline until CHG-0337.

`hub serve --zone control|agent|edge [zone arguments]` selects one entrypoint
per process: supervisor, ToolHub or communication. The old binaries call the
same internal `Main(args)` functions and Docker entrypoints stay unchanged.
`just build` also builds `bin/hub`; release packaging includes the Linux hub
binaries and contract sources. Source archives include only Git-tracked
allowlisted inputs, refuse symlinks and cannot include ignored diagnostics.
Supervisor watch re-exec preserves the whole process invocation, including
the hub zone. ToolHub's context-aware `Serve` and `NewAutomationHost` retain
the startup test paths. Automation tests use a test-only external factory for
real ToolHub stores, avoiding a parent/child test import cycle.

- No unsolicited framework/database/queue dependencies; stdlib first.
- Never commit deployments, tokens, browser profiles, transcripts or real
  personal data; secrets materialize only through the broker/secret store.
- Docker hygiene: build via `hubctl build`/`up` or `just docker-check-build`
  (BuildKit-forced paths); `just docker-clean` after other local builds,
  `--deep` only for an intentional cold reset; no broad system/volume
  prunes.
- Optional integrations stay out of the base image; features are opt-in
  env/feature flags.

## Verification of your own edits

Re-skim the file section you touched against the code it describes; bump
`last_verified` only when the content is actually confirmed against the
current tree — the devcheck docs gate rejects stale living docs.
