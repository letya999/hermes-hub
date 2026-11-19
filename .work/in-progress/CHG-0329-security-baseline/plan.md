# CHG-0329: Close the security baseline and remove fail-open paths (M40 stage 1)

Chain: `docs/adr/ADR-0105-v1-topology.md` D8 ->
`specs/active/SPEC-0100-v1-contracts.md` R5, R7 -> this plan -> `state.yaml`.
Issue: #573. Previous stage: CHG-0328. Next stage: CHG-0330.

## Goal

No configuration absence selects a weaker mode. The gateway cannot run
Hermes with user directories, the unmanaged capability mode is gone, the
supervisor fails closed on unreadable runtime env, path-bug directories are
removed and blocked, and the T01-T22 claims match the evidence.

## Read before coding

1. ADR-0105 D8, SPEC-0100 R5/R7, ADR-0031, ADR-0096.
2. `.work/done/CHG-0065-capability-enforcement/acceptance-matrix.md`.
3. `internal/communication/gateway.go` (runner selection), `runner.go`.
4. `internal/stack` render path and `rights.go` (policy-version hash).

## Slices

### S1.1 T01-T22 receipts or downgrade (R7.1)

Anchors: `.work/done/CHG-0065-capability-enforcement/acceptance-matrix.md`,
`README.md`, `docs/architecture.md`, `docs/validation.md`.
Change: re-run the Go-layer evidence the matrix names on current `dev`
(unit, fake-docker, protocol tests) and record receipts in `state.yaml`.
Rows that need the pinned probe, real-Docker canaries or the live dev stack
are not claimed from this run. Owner sign-off is required for "certified";
until it is recorded, README and architecture say "enforced in code,
certification pending owner sign-off".

### S1.2 Moved-CHG links

Anchors: `docs/architecture.md`, `docs/validation.md`.
Change: link `.work/done/CHG-0065-capability-enforcement/acceptance-matrix.md`.

### S1.3 Remove HermesRunner (R5.2)

Anchors: `internal/communication/gateway.go` gateway construction,
`internal/communication/runner.go` `HermesRunner`.
Change: delete `HermesRunner` and its config plumbing; gateway construction
returns a localized error without a runtime/supervisor endpoint, and the
binary exits non-zero.
Tests (red first): `TestGatewayRefusesWithoutRuntimeEndpoint`.

### S1.4 Remove the unmanaged mode (R5.1)

Anchors: `Settings.CapabilityMode` in `internal/stack` (config, compose,
render_env, rights, tools, tokens, hosting, governance, ...),
`internal/supervisor` (ensure, env, execute, http, sandbox, kubernetes_plan,
control), `HUB_CAPABILITY_MODE` readers in `internal/runtime` and
`internal/agenttools`, `cmd/hubctl`.
Change: managed branches become unconditional, unmanaged branches are
deleted; the in-runtime ToolHub autostart and `HUB_TOOLHUB_STORE` runtime env
go with them. The hashed rights view keeps `capability_mode: "managed"` as a
constant so live policy versions do not rotate (CHG-0230 outage class).
Tests (red first): `TestNoUnmanagedRenderPath`,
`TestMinimalNativeStackStillRenders`, a golden policy-version test.

### S1.5 Supervisor fails closed on unreadable runtime env (R5.3)

Anchors: `internal/supervisor/env.go` `materializeHermesConfig`.
Change: return the `ReadSecrets` error (wrapped, no keys or values).
Tests (red first): `TestSupervisorFailsClosedOnUnreadableRuntimeEnv`.

### S1.6 Path-bug directories (R5.4)

Anchors: repo root `C:UsersUserAppDataLocalTempfcstub`, `.work/tmp;C`,
`services/credential-broker;C`, `spaces/local/broker;C`,
`spaces/local/runtime;C`; `internal/devcheck` docs/hygiene check.
Evidence: all five are empty, untracked, created on 2026-10-17..2026-11-18
by ad-hoc Git Bash commands (MSYS converts `a:/b` volume arguments into
`a;C:\b`, and unquoted Windows paths lose backslashes). No tracked script
produces them (`rg` over justfile, scripts/, services/).
Change: delete them; `devcheck` fails when such a name exists in the tree;
AGENTS.md documents `MSYS_NO_PATHCONV=1` for Docker volume flags in Git Bash.
Tests (red first): `TestPathBugDirectoriesRejected`.

## Red-team checklist

- A misconfigured env (empty runtime URL) cannot make the gateway run Hermes
  with user directories: construction fails before any spool read.
- Removing a passport field or adding `capability_mode` cannot re-enable a
  legacy render path.
- An unreadable runtime env never yields a config with empty ToolHub
  endpoint and a running runtime.

## Acceptance

SPEC-0100 A5.1, A5.2, A5.3, A5.4, A7.1 (downgrade branch until owner
sign-off). Live: after merge, `dev-rebuild` of communication-hub and
supervisor on an existing dev space; existing managed spaces keep answering;
record receipts (no message content).

## Out of scope

- Run tokens (CHG-0332). Re-running real-Docker canaries and the pinned
  Hermes probe (CHG-0353 release gate).
