---
description: Frozen v1 contracts - topology and zone crossings, contract pattern, dependency gate, single owners, fail-closed configuration, run tokens and policy bundles, security baseline; master trace from M40 exit criteria to requirements, acceptance IDs and CHGs.
last_verified: "2026-11-19"
---
# SPEC-0100: v1 contracts

Frozen: 2026-11-19. Implementation: CHG-0329, CHG-0330, CHG-0331, CHG-0332
and the service CHGs in the trace table.
Decisions: [ADR-0105](../../docs/adr/ADR-0105-v1-topology.md),
[ADR-0106](../../docs/adr/ADR-0106-identity-layers-run-tokens.md).
Siblings: [SPEC-0101](SPEC-0101-v1-execution-slos.md) (execution and
provisioning), [SPEC-0102](SPEC-0102-v1-proactive-modes-eval.md)
(proactive-engine, action modes, eval).

## M40 exit criteria trace

Every exit criterion of milestone M40 maps to requirements and acceptance
IDs. A criterion is met only when all its acceptance IDs pass with recorded
evidence in the named CHG `state.yaml`.

| Exit criterion | Requirements | Acceptance | CHG |
|---|---|---|---|
| EC1 five hub processes plus on-demand runtimes; per-binary allowlist green; cmd/runtime does not link tool-hub | SPEC-0100 R1.1, R1.3, R3.1-R3.3 | A1.1, A1.3, A3.1-A3.3 | CHG-0330, CHG-0353 |
| EC2 one job ledger, one secret authority, one registry owner; no unmanaged mode, no HermesRunner fallback; minimal stack works | SPEC-0100 R4.1-R4.3, R5.1, R5.2, R1.2 | A4.1-A4.3, A5.1, A5.2, A1.2 | CHG-0329, CHG-0335, CHG-0339, CHG-0337 |
| EC3 every agent call carries a verified run token; forged, aliased and stale-generation calls refused before any backend | SPEC-0100 R6.1, R6.2 | A6.1, A6.2 | CHG-0332 |
| EC4 new space from identity plus six template passports; creation renders only the runtime spec; first message within SLO | SPEC-0101 R2.1-R2.3, P1, P2 | SPEC-0101 A2.1-A2.3, AP1, AP2 | CHG-0334 |
| EC5 Jira and Slack assignment scenarios live with receipts; proactivity eval meets thresholds in CI; p95 event-to-notice within SLO | SPEC-0102 R6.1-R6.3, R7.1-R7.3; SPEC-0101 P5 | SPEC-0102 A6.1-A6.3, A7.1-A7.3; SPEC-0101 AP5 | CHG-0346, CHG-0352 |
| EC6 3 interactive sessions and 20 active assignments on one VPS without interactive p95 regression | SPEC-0101 P4, R7.1 | SPEC-0101 AP4, A7.1 | CHG-0353 |
| EC7 CHG-0065 T01-T22 accepted by the owner | SPEC-0100 R7.1 | A7.1 | CHG-0329, CHG-0353 |

Reserved CHG numbers (one per M40 issue, created when the issue starts):
M40.0 CHG-0328, M40.1 CHG-0329, M40.2 CHG-0330, M40.3 CHG-0331, M40.4
CHG-0332, M40.5 CHG-0333, M40.6 CHG-0334, M40.7 CHG-0335, M40.8 CHG-0336,
M40.9 CHG-0337, M40.10 CHG-0338, M40.11 CHG-0339, M40.12 CHG-0340, M40.13
CHG-0341, M40.14 CHG-0342, M40.15 CHG-0343, M40.16 CHG-0344, M40.17
CHG-0345, M40.18 CHG-0346, M40.19 CHG-0347, M40.20 CHG-0348, M40.21
CHG-0349, M40.22 CHG-0350, M40.23 CHG-0351, M40.24 CHG-0352, M40.25
CHG-0353.

## R1 Topology

- R1.1 Production topology runs exactly five hub processes: `hub-control`,
  `hub-agent`, `hub-edge` (one `hub` binary with `serve --zone`),
  `credential-broker`, `egress-gateway`; space runtimes and workers are
  spawned on demand.
- R1.2 Minimal topology: one `hub` process serving `control` (optionally
  `control,agent,edge`) plus one `harness-runtime` whose tools are all
  `via: native` renders and answers a message.
- R1.3 `cmd/runtime`, `cmd/supervisor` and `cmd/communication` do not link
  `internal/toolhub` (the ToolHub server package and its subpackages other
  than DTO packages scheduled in the baseline); after CHG-0337 they link no
  `internal/toolhub/...` package at all.
- R1.4 Zone crossings follow the ADR-0105 table; each refused crossing has a
  test.

## R2 Contract pattern

- R2.1 A contract lives in `contracts/<name>/v1` with: DTO types, one
  `Client` interface, an in-process transport, an HTTP transport (client
  and `http.Handler`), and a `conformance` package exporting `Run(t, client)`.
- R2.2 The conformance suite runs against both transports in the same test;
  results must be equal, including typed errors.
- R2.3 A contract package imports only the standard library, other
  `contracts/*` packages and `internal/identity` (a stdlib-only leaf holding
  the envelope primitives, ADR-0105 D6). DTOs carry no secret values.
- R2.4 The first contracts are `identity`, `task` and `toolsurface`
  (CHG-0330). Each later service CHG adds its own contract.
- R2.5 HTTP transport errors map to typed errors: `not_found`, `conflict`,
  `forbidden`, `invalid`, `unavailable`; unknown status is `unavailable`,
  never success.

## R3 Dependency gate

- R3.1 `devcheck` computes `go list -deps` per `cmd/*` binary and compares
  each module edge with an allowlist.
- R3.2 Shrink-only baseline: a baseline entry that no longer occurs must be
  deleted from the baseline (stale entries fail); a new edge fails.
- R3.3 The check uses the full transitive closure; a forbidden package
  reached through a shared helper is reported with the path that reaches it.

## R4 Single owners

- R4.1 One job ledger: task-engine (CHG-0335).
- R4.2 One secret authority for user and connector credentials and OAuth:
  credential-broker (CHG-0339).
- R4.3 One owner per store: no process opens a store file that belongs to
  another module (first removal: agenttools and stack open of the ToolHub
  store, CHG-0330 S2.3).

## R5 Fail-closed configuration

- R5.1 No `CapabilityMode` and no unmanaged render path in production code;
  a leftover `capability_mode` key cannot re-enable one.
- R5.2 The channel gateway refuses to start without a runtime or supervisor
  endpoint and exits non-zero with a localized message; it never runs Hermes
  itself.
- R5.3 An unreadable or corrupt runtime env or secret file fails the spawn;
  only an absent optional file may be skipped, and only where the spec of
  that file says it is optional.
- R5.4 No tool or script creates path-bug directories (`...;C`,
  `C:Users...`) in the repository tree; `devcheck` rejects them.

## R6 Identity, run tokens, policy bundles

- R6.1 Every tool, memory, model and egress call from a runtime carries a
  run token signed by task-engine; verifiers check signature, audience,
  expiry and current generation.
- R6.2 Missing, forged, expired, wrong-audience, cross-space, run-aliased
  and stale-generation tokens are refused before any backend is contacted.
- R6.3 Policy is loaded only from signed bundles by digest.
- R6.4 Layers narrow only: platform -> org -> user -> agent profile.
- R6.5 System principals hold no user credentials; work for a user records
  `initiator`.

## R7 Security baseline

- R7.1 CHG-0065 T01-T22 are re-run on current `dev` with receipts and signed
  off by the owner; until then README and architecture state the claims as
  "enforced in code, certification pending" instead of "certified".

## Acceptance

| ID | Check |
|---|---|
| A1.1 | Deployment contract test lists exactly the five hub processes for the full topology |
| A1.2 | `TestMinimalNativeStackStillRenders` plus a live minimal-stack message receipt |
| A1.3 | `TestRuntimeDoesNotLinkToolHub` (go list closure of the three binaries) |
| A1.4 | One refusal test per refused crossing in the ADR-0105 table, added by the CHG that builds the crossing |
| A2.1 | `TestContractConformanceInProcAndHTTP` for identity, task, toolsurface |
| A2.2 | A contract import of a non-contract package fails the gate |
| A3.1 | `TestModuleImportAllowlist` |
| A3.2 | Adding a forbidden import fails `just check`; a stale baseline entry fails |
| A3.3 | A forbidden package reached transitively is reported with its path |
| A4.1-A4.3 | Each owner CHG proves the old writers are gone (`rg` and gate evidence) |
| A5.1 | `TestNoUnmanagedRenderPath` |
| A5.2 | `TestGatewayRefusesWithoutRuntimeEndpoint` |
| A5.3 | `TestSupervisorFailsClosedOnUnreadableRuntimeEnv` |
| A5.4 | devcheck path-hygiene test |
| A6.1 | Every agent-zone handler has a token-required test |
| A6.2 | Forge, alias, stale-generation and cross-space tests reach no backend (backend spy records zero calls) |
| A6.3 | Unsigned or mismatched bundle denies load |
| A7.1 | Owner sign-off recorded in CHG-0353 or downgraded claims verified in README and docs/architecture.md |
