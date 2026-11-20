---
description: The v1 rulebook - simplicity, service, data, state-machine, security, tool, size and test rules, each with the test or gate that enforces it; plus gates, pins and the change process.
last_verified: 2026-11-20
---

# Engineering rules

This page is the rulebook for v1 (milestone M40). Every later issue follows
it; a change that breaks a rule changes this page first. The architecture
these rules produce is in [architecture.md](architecture.md); the state
machines in [state-machines.md](state-machines.md); the decisions behind them
in [ADR-0105](adr/ADR-0105-v1-topology.md),
[ADR-0106](adr/ADR-0106-identity-layers-run-tokens.md) and
[ADR-0110](adr/ADR-0110-v1-storage.md)..[ADR-0112](adr/ADR-0112-v1-secrets.md).

## How to read a rule

Each rule has an ID, the rule, and the gate that enforces it.

- A test name with no tag exists today and runs in `just check`,
  `just test` or CI.
- M40.2 infrastructure tests link to their source. The minimal message
  topology runs real spawn-controller and harness-runtime binaries.
- A tag such as "(M40.6)" names the milestone issue that adds the gate.
- "Review" means no automated check exists; the reviewer of the CHG checks
  it. A review-only rule is a candidate for a gate, never an exception.

## Simplicity rules

| ID | Rule | Gate |
|---|---|---|
| S1 | No abstraction without a second real implementation. The only exceptions are the six extension points, each with an existing first provider: launch backends, connectors and skills, channel adapters, model and media providers, memory providers, identity providers (OIDC). | Review; the six points are fixed in [architecture.md](architecture.md#extension-points). |
| S2 | Reuse what exists: credential refs, broker leases and cred-proxy, the passport compiler, the OpenKnowledge vault, ToolHive and cells, `shared/locale`. Move code; never rewrite it beside the original. | Review; each extraction CHG lists the moved files. |
| S3 | A concept enters the code only when a feature in the current milestone uses it. Later milestones add their own fields. | staticcheck unused-code checks (`just lint`, CI); review against the issue Work list. |
| S4 | Delete before add. Every extraction removes the old code in the same change. | Per-issue acceptance in [SPEC-0100](../specs/active/SPEC-0100-v1-contracts.md) ("old code deleted", `rg` evidence in the CHG). |
| S5 | Reliability comes from few mechanisms used everywhere: one DB per service, one `Transition`, deadlines and sweepers, idempotency keys, `uncertain` never retried automatically. | The data and state-machine rules below. |

## Service rules

| ID | Rule | Gate |
|---|---|---|
| V1 | A service lives in `services/<svc>/` with its code under `services/<svc>/internal`. | Go `internal` visibility (compiler); M40.2 skeletons keep `package main` at the service root — extraction under `internal/` is M40.3 onward. |
| V2 | A service imports only `shared/*` (infrastructure, no domain rules) and `contracts/*` (DTO, client, fake). Never another service. `shared/*` never imports `services/*` or a domain package. | `devcheck boundaries` / [TestSharedImportBoundary](../internal/devcheck/boundaries_test.go); Go `internal` visibility for service internals. |
| V3 | One binary per service with its own build and tests. It builds and tests in seconds without Docker. No build on the hot path; images are prebuilt and pinned. | `just test <service package>`; [TestServiceDescriptorIsConsistent](../cmd/hubctl/process_test.go) and [TestProcessModeStartsProfileWithDeclaredEnv](../cmd/hubctl/process_test.go); actual service builds remain per extraction. |
| V4 | Services talk HTTP+JSON through `contracts/<svc>/v1`: DTOs, one client interface, an in-process and an HTTP transport, and `contracts/<svc>/v1/fake`. The fake passes the same conformance suite as the real handler. | `TestContractConformanceInProcAndHTTP` (task, identity today); [TestFakePassesConformance](../contracts/task/v1/fake/fake_test.go), using [shared/testkit.Transports](../shared/testkit/transport.go). |
| V5 | A peer address comes from `HUB_<SVC>_URL`. Empty means the service is disabled: the feature falls back to the Hermes native implementation or switches off with a localized message. It never breaks. | [TestMinimalProfileAnswersMessage](../cmd/hubctl/process_test.go) (real binaries, fake model server); one "dependency disabled" service test per service (M40.3..M40.13). |
| V6 | Every call carries a token. Every call has a timeout. Every write carries an idempotency key. | `TestBearerSchemeRequired` ([shared/httpapi](../shared/httpapi/httpapi_test.go)); `TestServiceSurfaceRequiresTokenAndRefusesBrowser` ([spawn-controller](../services/spawn-controller/internal/api/controller_test.go)); run tokens (M40.14); `TestDuplicateWakeCollapses` (M40.6). |
| V7 | No shared files between services and no message broker. A service reads another service's state only through its contract. | `TestExecutorNeverOpensToolHubStore`, `TestRenderDoesNotOpenToolHubStore` today; "no other process opens `<svc>.db`" acceptance per service (M40.3..M40.13). |
| V8 | A breaking contract change adds `v2` beside `v1`. `v1` is deleted only when no caller links it. | Conformance suite per version; review. |
| V9 | Missing or unreadable configuration never selects a weaker mode; the process exits non-zero with a localized message. An unknown `HUB_*` variable fails startup. | `TestNoUnmanagedRenderPath`, `TestGatewayRefusesWithoutRuntimeEndpoint`, `TestSupervisorFailsClosedOnUnreadableRuntimeEnv` today; unknown-env refusal per service (M40.4 first). |
| V10 | Every `services/<svc>/README.md` follows [services/README.template.md](../services/README.template.md): responsibility, owns, does not own, API, state machines, tables, environment, extension point, behaviour when a dependency is disabled, tools it executes. | `go run ./cmd/devcheck docs` (`TestServiceReadmeTemplateHeadings`). `services/credential-broker` is exempt until M40.13; the exemption is shrink-only. |

## Data rules

| ID | Rule | Gate |
|---|---|---|
| D1 | One database per service. SQLite is the default; Postgres is selected by the same connection string (`sqlite://...`, `postgres://...` after importing `shared/store/postgres`). SQL stays portable across both. | Store suite on SQLite every PR; the same suite on Postgres when `HUB_TEST_POSTGRES_URL` is set — green on a disposable postgres:17 container locally, [postgres-store CI job](../.github/workflows/ci.yml) pending publication. |
| D2 | Migrations are embedded, numbered SQL files applied under a migration lock, so N copies can start at once. | [TestMigrationLockSerializesConcurrentStart](../shared/store/store_test.go). |
| D3 | No service state in `.json` or `.jsonl` files. Files hold only user data (vault, workspace, sites), input YAML passports, harness session files inside the workspace, and logs go to stdout. | Per-service acceptance "no `.json`/`.jsonl` state written" (M40.3..M40.13). |
| D4 | Every row carries `org` and `owner`. `shared/store` refuses a query without both predicates (fail closed). A cross-tenant read returns `not_found`. | [TestStoreRefusesUnscopedQuery](../shared/store/store_test.go); [storetest.TenantInvariants](../shared/store/storetest/storetest.go), fresh store per table, runs first in `just test`. |
| D5 | v0 data enters through an importer on first start, with a dry-run report; importers are deleted one release after v1. | Importer tests on real-shaped data per service; rollback rehearsal (M40.16). |

## State-machine rules

| ID | Rule | Gate |
|---|---|---|
| F1 | Every state change goes through one `Transition(tx, machine, id, event)` in `shared/fsm`, validated against the service's transition table. A forbidden transition writes nothing. | [TestTransitionRejectsForbiddenAndWritesNothing](../shared/fsm/fsm_test.go); one table test per machine (allowed and forbidden). |
| F2 | The history row is written in the same transaction as the state. | [TestTransitionWritesStateAndHistory](../shared/fsm/fsm_test.go). |
| F3 | Every non-final state has a deadline and a sweeper, and names who acts next (runtime, owner or sweeper). Nothing waits forever. | [TestSweepMovesExpiredState](../shared/fsm/fsm_test.go); per-machine deadline tests in each service. |
| F4 | `uncertain` is never retried automatically. It leaves only by observation or by the owner. | `TestUncertainNeverAutoRetried` (M40.6). |
| F5 | No stored flag beside a state. Counts and booleans that follow from rows are derived, never stored. | `TestLeaseCountDerivedFromLeaseRows` (M40.0) is the first instance; review of every new column. |
| F6 | Every machine is a table in its service README and in [state-machines.md](state-machines.md). | `## State machines` heading via devcheck docs; review of the table. |

## Security rules

| ID | Rule | Gate |
|---|---|---|
| X1 | Zones are network and secret rules: control, agent, edge, secret, egress, space. Edges are default-deny; only declared edges open. Each launch backend implements them natively (Docker networks, Kubernetes NetworkPolicy, process mode only when trusted). | Backend conformance "network edges applied" (M40.3; kubernetes and firecracker M40.15). |
| X2 | Runtimes and workers are untrusted. They never receive the Docker socket or another space's mounts. A runtime mounts only its own space's workspace. | Backend conformance "only own workspace mounted" (M40.3); T01-T22 re-run on v1 (M40.16). |
| X3 | Metadata and private address ranges are blocked from spaces and workers. | `TestMetadataCIDRBlocked` (M40.13). |
| X4 | Secrets live only in credential-broker. Runtimes and workers hold credential refs; the broker leases values to cred-proxy or egress-gateway, which inject them at the boundary. No secret in env, argv, DTOs, rows, logs or tool results. | `TestNoSecretInRuntimeEnvArgvOrFiles` (M40.13); backend conformance "no secret in env/argv" (M40.3); gitleaks (CI). |
| X5 | One run token per run, signed by identity-hub, with an `aud` list. Each verifier checks signature, its own name in `aud` and expiry, then generation and revocation at task-engine. No header identity. A subagent run gets its own token with `parent_run`. | `TestForgedRunTokenRefusedBeforeBackend`, `TestRunAliasRefused`, `TestStaleGenerationRefused`, `TestForeignSpaceTokenRefused`, `TestHeaderInjectionIgnored` (M40.14, backend spy at zero calls). |
| X6 | Installation flag `trusted: true\|false`. With `false`, process mode and runc without gVisor are refused and egress-gateway is mandatory. | [TestProcessModeRefusesUntrusted](../cmd/hubctl/process_test.go); `TestUntrustedRefusesProcessBackend` (M40.3). |
| X7 | Untrusted input (email, web, webhooks, tool results, other agents, group guests) is data. It can never raise a mode or approve. | `TestToolResultCannotApprove` (M40.7); `TestGuestCannotApprove` (M40.8). |
| X8 | Unprompted runs (`origin=unprompted`) are read-only by default; anything beyond write-local needs an OwnerDecision. Enforced in tool-hub by run origin, not by prompt. | `TestUnpromptedRunCannotWriteExternal` (M40.7). |
| X9 | Different users never share a runtime. One runtime serves one (space, agent, policy digest); narrower rights get a separate runtime. | `TestParallelSessionsIsolated`, `TestNarrowerRightsNeedSeparateRuntime` (M40.4). |
| X10 | Diagnostic output allowlists safe fields; auth files, container environments and credential stores are never dumped (AGENTS.md). | gosec and gitleaks (CI); review. |

## Tool rules

| ID | Rule | Gate |
|---|---|---|
| T1 | Every tool in the `hub-agent-tools` catalog keeps its name, schema, effect and result. A move changes only who executes it. | [TestToolCatalogSnapshotUnchanged](../internal/devcheck/tool_snapshot_test.go): the snapshot changes only together with a catalog version bump. |
| T2 | One executor per tool family; the call path is runtime -> tool-hub -> owner service. | Routing test per family (M40.7). |
| T3 | Agent-tool contract changes ship in one changeset: tool description, prompt-contract guidance (`internal/stack/prompt_contract.go`), `docs/tools.md` and `docs/routines.md`, a bumped immutable `hub-agent-tools` catalog version, then rebind and respawn of managed spaces. | `TestCatalogToolsDocumented`, `TestPlatformCatalogParityAndPinnedToolset`. |
| T4 | One effect vocabulary: `read`, `write-local`, `write-external`, `critical`. An unknown tool is `critical`. Decisions are `allow`, `ask`, `deny`; deny wins. | `TestUnknownToolIsCritical` (M40.7). |
| T5 | Tools the effective spec denies are not listed to the agent; tool-hub re-checks at call time and after an approval. | `TestDeniedToolNotListed` (M40.7). |
| T6 | Every user-visible string goes through `shared/locale` with keys in all four catalogs (en, ru, kk, es). | [TestCatalogParity](../shared/locale/locale_test.go). |

## Size and dependency rules

| ID | Rule | Gate |
|---|---|---|
| Z1 | A file stays under about 800 lines; a service under about 8k lines. | Review (no automated check yet). |
| Z2 | No frameworks, DI containers or code generation. Embedded SQL is fine. Standard library first. | Review of `go.mod` diffs; `go mod tidy -diff` (`just lint`). |
| Z3 | A new dependency needs one line in an ADR naming why the standard library is not enough. | Review; `just security` (govulncheck, npm audit) on every dependency change. |
| Z4 | A service README is one page from the template (V10). | devcheck docs. |

## Test base

Levels in the order they run. Tenant invariants run first and block.

| Level | Checks | When | Enforced by |
|---|---|---|---|
| Tenant invariants | fresh store per table; cross-tenant read = `not_found` | every PR | [storetest.TenantInvariants](../shared/store/storetest/storetest.go) |
| Domain | pure logic; every state machine: allowed and forbidden transitions; injected clock | every PR | per-service FSM table tests |
| Store | real SQLite in a temp dir; the same suite on Postgres in CI | every PR | [store/FSM suites](../shared/store/store_test.go), [Postgres CI](../.github/workflows/ci.yml) |
| Contract | conformance against the real handler, in-process and HTTP; the fake passes the same suite | every PR | `TestContractConformanceInProcAndHTTP` |
| Service | black box over HTTP with fakes; behaviour with each dependency disabled | every PR | per service (M40.3..M40.13) |
| Tool preservation | catalog snapshot: name, schema, effect unchanged; routing test per family | every PR | [TestToolCatalogSnapshotUnchanged](../internal/devcheck/tool_snapshot_test.go), routing (M40.7) |
| Security | token forge, alias, stale, foreign space; foreign tenant; header injection; workspace isolation; untrusted input cannot approve | every PR | X-rules tests (M40.4..M40.14) |
| Topology | minimal and family profiles in process mode | every PR | [TestMinimalProfileAnswersMessage](../cmd/hubctl/process_test.go) (real spawn-controller + harness-runtime, fake model server) |
| Backends | one conformance suite: process and docker every PR; kubernetes (kind) and firecracker nightly | PR / nightly | backend conformance (M40.3, M40.15) |
| Load | N users, parallel sessions, SLOs | release | load harness (M40.16) |
| Eval | proactive and memory evals | M41 | eval gate (M41) |

Test rules:

- No mocks of a service's own code. Fakes only for foreign contracts.
- A [fake model server](../shared/testkit/model.go) (OpenAI-compatible SSE) for
  runtime tests; recorded MCP replays for connectors.
- A flaky test is a bug. Tests of deleted internals are deleted with them.
- Red first: a behavioural assertion fails before the fix and passes after
  it; the CHG records both.
- Do not claim integration success from mock tests.

## Gates and commands

- `just check`: format, docs (`devcheck docs`: numbering, index sync,
  `last_verified`, size, links, service README headings), shared import
  boundaries (`devcheck boundaries`), vet. Run before delivery. One
  repo-wide check at a time across local worktrees.
- `just test <packages>` for affected packages; concurrency changes add
  targeted race tests. During edits: `go vet -p=2 <packages>` and
  `go test -short -run '<Test>' <package>`.
- CI owns the full race and shuffle suite, coverage (85% overall, 75% per
  internal package), staticcheck, gosec, gitleaks, deployment contracts and
  Docker image and container acceptance. Local green is not CI acceptance.
- `just security` when dependencies change. `just check-full` is the
  explicit exhaustive local fallback, not the edit loop.
- `just docker-check` verifies exact-HEAD images; `just docker-check-build`
  builds them locally.
- Report an unavailable gate as unavailable; never claim it by proxy.

## Pins

- Hermes `0.21.0` @ `869228cab` (ADR-0012). CLIProxy
  `c5251b57b3fa5dcbc689f514f4aa166daacb4481`. ToolHive v0.48.0, cellinit per
  ADR-0013. Full table: [integrations.md](integrations.md).
- One Go toolchain across host and image builds.
- staticcheck pinned to upstream commit `452d5bb86b45`
  (v0.7.0-0.dev.0.20261009230814-452d5bb86b45): release v0.8.1 cannot
  decode this toolchain's export data.

## Change process

- Significant changes use `.work/in-progress/CHG-NNNN-name/{plan.md,
  state.yaml}`; milestones follow `.work/milestone-playbook.md`.
- A service CHG closes only with: its README from the template, its state
  machines in [state-machines.md](state-machines.md), the tool snapshot and
  tenant invariants green, `just check` green, and a live non-destructive
  smoke on a dev space recorded (owner OK).
- Accepted ADRs are append-only; amend by a new ADR.
- SPECs are frozen requirements; the code matches them or the spec says what
  is deferred.
- Regression tests are mandatory for file-isolation, concurrency,
  authorization and HTTP-contract changes.
- Never commit deployments, tokens, browser profiles, transcripts or real
  personal data.
- Docker: build through `hubctl build`/`up` or `just docker-check-build`;
  run `just docker-clean` after other local builds; never broad system or
  volume prunes.

## Skeleton and remaining v0 owners

The [shared skeleton](shared.md) replaces the old dependency baseline, cmd/hub
zone multiplexer and toolsurface contract. task/v1 and identity/v1 use
shared/httpapi; ToolHub keeps its catalog DTOs locally. Existing v0 owners still
run until their service extraction issues land. shared/auth preserves the old
claim format; the target aud list and live run authority arrive in M40.14.
The minimal message path is real: `hubctl run --mode process --profile minimal`
starts spawn-controller, which spawns harness-runtime per (space, agent) and
answers a message through the fake model server. The full spawn/runtime
contracts and richer topologies arrive with the extraction issues.
