---
description: Frozen v1 contract list - the contracts each v1 service exposes, the shared contract and call requirements, and acceptance IDs per M40 issue (M40.0..M40.16) traced to the milestone exit criteria.
last_verified: "2026-11-20"
---
# SPEC-0100: v1 contracts and M40 acceptance

Frozen: 2026-11-20 (rewritten by CHG-0334, M40.1 #599; the 2026-11-19
version with five hub processes and a reserved CHG table was never merged
to `dev`). Owner review pending.
Decisions: [ADR-0105](../../docs/adr/ADR-0105-v1-topology.md) (service
model), [ADR-0106](../../docs/adr/ADR-0106-identity-layers-run-tokens.md)
(identity, run tokens), [ADR-0107](../../docs/adr/ADR-0107-execution-spawn-task-runtime.md)
(execution), [ADR-0108](../../docs/adr/ADR-0108-proactive-engine.md),
[ADR-0109](../../docs/adr/ADR-0109-action-modes-owner-decisions.md),
[ADR-0110](../../docs/adr/ADR-0110-v1-storage.md),
[ADR-0111](../../docs/adr/ADR-0111-v1-state-machines.md),
[ADR-0112](../../docs/adr/ADR-0112-v1-secrets.md).
Rules: [engineering.md](../../docs/engineering.md) (rule IDs S, V, D, F, X,
T, Z below refer to it). Siblings: [SPEC-0101](SPEC-0101-v1-execution-slos.md)
(SLOs), [SPEC-0102](SPEC-0102-v1-proactive-modes-eval.md) (proactive and
eval, mostly M41).

CHG numbers are allocated when an issue starts; each issue's CHG records
its evidence against the acceptance IDs below. There is no reserved table.

## M40 exit criteria

| ID | Exit criterion (milestone 50) | Acceptance |
|---|---|---|
| EC1 | Every service is a separate binary with its own DB, contract, state machines, README and tests; workers and browser-runtime spawned by spawn-controller | A2.1-A2.4, A3.1..A13.x (per service "README, FSM, DB" rows) |
| EC2 | No service imports another; no service state in `.json`/`.jsonl`; every row tenant-scoped | A2.1, A2.2, per-service "no file state" rows |
| EC3 | The core (spawn-controller + harness-runtime) runs alone with Hermes native tools | A2.6, A4.1 |
| EC4 | Profiles minimal, family and enterprise run on process and docker (kubernetes and firecracker in M40.15) | A2.6, A3.1, A15.1, A16.1 |
| EC5 | Tool catalog snapshot unchanged; one run token per run; secrets only in credential-broker | A2.7, A14.1-A14.2, A13.1 |

## Contract list

Each contract lives in `contracts/<name>/v1` and is created by the issue
that extracts its owner. The name is fixed by that issue; the verbs below
are the scope, not the final route list (the service README holds that).

| Contract | Owner | Scope | Main callers | Issue |
|---|---|---|---|---|
| `identity/v1` | identity-hub | resolve channel identity, principals, orgs, roles, link begin/verify/revoke, run token issue, public keys | channel-hub, task-engine, every token verifier | exists; M40.5 |
| `task/v1` | task-engine | create job (idempotency key), job and run status, conversation queue, cancel, panic stop, run events with cursor, generation and revocation check | channel-hub, proactive-engine, tool-hub (`sessions`), token verifiers | exists; M40.6 |
| `spawn/v1` | spawn-controller | spaces, layer passports and revisions, effective spec slice by digest, runtime and worker request, stop, inspect | task-engine, tool-hub (settings, workers), hubctl | M40.3 |
| `runtime/v1` | harness-runtime | private job contract: start run in a slot, stream events, observe, cancel | task-engine through spawn-controller delivery | M40.4 |
| `tool/v1` | tool-hub | tool list for a run, tool call, OwnerDecision create/approve/deny, connector lifecycle | harness-runtime, channel-hub (approvals), hubctl | M40.7 |
| `channel/v1` | channel-hub | outbound message (`class reply\|notice`), delivery status | task-engine, proactive-engine, model-gateway (media delivery) | M40.8 |
| `memory/v1` | memory-hub | remember, recall, search, forget, read/open, proposals | tool-hub | M40.9 |
| `proactive/v1` | proactive-engine | routines, watches, assignments, goals, occurrences | tool-hub | M40.10 |
| `hosting/v1` | hosting-hub | sites, exposure, DNS records, fleet budget | tool-hub, ingress-gateway | M40.11 |
| `model/v1` | model-gateway | model routes and limits, completions relay, media jobs, STT, TTS, usage | harness-runtime, tool-hub | M40.12 |
| broker contract | credential-broker | credential refs, leases, OAuth (the module's existing `contract` package) | egress-gateway, workers (cred-proxy), model-gateway | M40.13 |

ingress-gateway and egress-gateway are pass-throughs: they expose routes
and a proxy, not a domain contract.

## Requirements

### R1 Contract shape

- R1.1 A contract has DTOs, one client interface, an in-process transport,
  an HTTP transport (client and handler) and `contracts/<name>/v1/fake`.
  A conformance suite runs the same cases against the real handler over
  both transports and against the fake (rule V4).
- R1.2 A contract imports only the standard library, `shared/httpapi` and
  other contracts. DTOs carry no secret values (rule X4).
- R1.3 HTTP errors map to typed errors `not_found`, `conflict`,
  `forbidden`, `invalid`, `unavailable`; an unknown status is
  `unavailable`, never success.
- R1.4 A breaking change adds `v2` beside `v1` (rule V8).

### R2 Calls

- R2.1 Every call carries a token: a run token from runtimes and workers,
  a service token between services (rule V6, X5).
- R2.2 Every call has a timeout; every write carries an idempotency key.
- R2.3 A disabled peer (`HUB_<SVC>_URL` empty) gives the native fallback or
  a localized "feature off" result, never a crash (rule V5).

### R3 Data and state

- R3.1 One database per service, SQLite or Postgres by connection string;
  every row carries `org` and `owner`; unscoped queries are refused (rules
  D1-D4).
- R3.2 Every lifecycle is a table driven through `shared/fsm` with history,
  deadlines and sweepers; `uncertain` is never retried automatically (rules
  F1-F5).

### R5 Fail-closed configuration (kept from the 2026-11-19 version)

These IDs are delivered on this branch by CHG-0329 and keep their meaning.

- R5.1 No `CapabilityMode` and no unmanaged render path; a leftover
  `capability_mode` key cannot re-enable one.
- R5.2 The channel gateway refuses to start without a runtime or
  supervisor endpoint; it never runs Hermes itself.
- R5.3 An unreadable or corrupt runtime env or secret file fails the spawn.
- R5.4 No tool or script creates path-bug directories (`...;C`,
  `C:Users...`) in the repository tree; `devcheck` rejects them.

### R7 Security baseline (kept)

- R7.1 CHG-0065 T01-T22 are re-run on v1 with receipts and signed off by
  the owner (M40.16); until then README and architecture state "enforced
  in code, certification pending".

Retired IDs of the 2026-11-19 version: R1.1-R1.4 (five hub processes),
R2.1-R2.5 (contract pattern, now R1), R3.1-R3.3 (dependency gate, deleted
in M40.2), R4.1-R4.3 (single owners, now ADR-0105 D7 and per-service
acceptance), R6.1-R6.5 (tokens and bundles, now ADR-0106 and A14.x).
CHG-0329..0332 records cite the retired IDs as history.

## Acceptance per issue

An issue's CHG closes only when its rows pass with evidence, its service
README follows the template, the tool snapshot (A2.7) and tenant invariants
(A2.1) are green, `just check` is green, and a live non-destructive smoke
is recorded with owner OK.

| ID | Issue | Check |
|---|---|---|
| A0.1 | M40.0 #598 | Ten named regressions red before, green after: `TestUncertainJobReleasesLaneAfterSupervisorSettles`, `TestLeaseCountDerivedFromLeaseRows`, `TestRunningJobPastDeadlineBecomesUncertain`, `TestScheduleEditDropsPendingBatchAndKeepsOtherStreams`, `TestCorruptMappingSettlesClaimedJob`, `TestGoalClearDuringEvaluationIsNotUndone`, `TestFailedDreamingNightDoesNotBlockNextNight`, `TestRenderMigratesBindingsOnSQLiteStore`, `TestFirstApplyCrashRecoversWithoutIntervention`, `TestApplyRefusesPausedAssignment` |
| A1.1 | M40.1 #599 | `docs/engineering.md`, `docs/architecture.md`, `docs/state-machines.md`, `services/README.template.md` exist and the owner reviewed them |
| A1.2 | M40.1 #599 | `TestServiceReadmeTemplateHeadings`; `go run ./cmd/devcheck docs` green |
| A2.1 | M40.2 #600 | `TestStoreRefusesUnscopedQuery`; tenant invariant runner runs first in `just test` |
| A2.2 | M40.2 #600 | `TestMigrationLockSerializesConcurrentStart`; store suite green on Postgres in CI |
| A2.3 | M40.2 #600 | `TestTransitionRejectsForbiddenAndWritesNothing`, `TestSweepMovesExpiredState` |
| A2.4 | M40.2 #600 | `cmd/hub`, the dependency gate and baseline, `contracts/toolsurface` deleted; nothing imports them; `shared/*` import rule in devcheck |
| A2.5 | M40.2 #600 | `TestProcessModeRefusesUntrusted` |
| A2.6 | M40.2 #600 | `TestMinimalProfileAnswersMessage` (process mode, fake model server) |
| A2.7 | M40.2 #600 | `TestToolCatalogSnapshotUnchanged` |
| A3.1 | M40.3 #601 | Create space and first message on process and docker; backend conformance (only own workspace mounted, no secret in env/argv, network edges applied) |
| A3.2 | M40.3 #601 | `TestRevisionRollbackActivatesOlder`, `TestApplyCrashLeavesPreviousActive`, `TestWarmClaimBindsTenantAndDigest`, `TestRevokeDrainsOldDigest`, `TestUntrustedRefusesProcessBackend`; no `.json`/`.jsonl` state; old spawn code deleted; `settings_edit` end to end |
| A4.1 | M40.4 #602 | Minimal profile answers with native tools; `TestParallelSessionsIsolated`, `TestRunIdentityNotTakenFromRequest`, `TestWorkspaceConfigIgnored`, `TestNarrowerRightsNeedSeparateRuntime` |
| A5.1 | M40.5 #603 | `TestChannelLinkRequiresInChannelProof`, `TestOIDCCallbackCannotLinkForeignSender`, `TestSimilarNamesNeverMerge`, `TestAgentPrincipalCannotImpersonateHuman`, `TestGuestIdOpaque`, `TestLegacySpaceMapsToOwnedAgent`; no YAML or env user list left; live spaces migrated 1:1 after an owner-reviewed dry run |
| A6.1 | M40.6 #604 | `TestUncertainNeverAutoRetried`, `TestDuplicateWakeCollapses`, `TestInterruptCancelsRunningJob`, `TestPanicStopCancelsAllJobsOfUser`, `TestRunEventsResumeFromCursor`; M40.0 wedge regressions as FSM tests; no other package writes job status; `sessions` works through task-engine |
| A7.1 | M40.7 #605 | `TestDeniedToolNotListed`, `TestUnknownToolIsCritical`, `TestApprovalSingleUseAndBoundToArgs`, `TestApprovalTimeoutDenies`, `TestUnpromptedRunCannotWriteExternal`, `TestToolResultCannotApprove`; routing test per family; every tool has exactly one executor; no other process opens `tool.db` |
| A8.1 | M40.8 #606 | `TestGroupWithOneUserAndBotServedByOwnerSpace`, `TestGuestCannotApprove`, `TestDeliveryRecordedAfterResult`, `TestUnknownSendOutcomeNotResent`; Telegram and Slack through ingress in process mode; native gateway fallback |
| A9.1 | M40.9 #607 | Provider conformance; `TestForgetRemovesItem`, `TestDisconnectPurgesConnectorItems`; native memory fallback |
| A10.1 | M40.10 #608 | `TestCatchUpAfterDowntime`, `TestJobsOnlyViaTaskEngine`, `TestPausedAssignmentApplyRefused`; FSM tables; importer on real-shaped data |
| A11.1 | M40.11 #609 | Tool preservation for sites and exposure; publish and unpublish a site in process mode |
| A12.1 | M40.12 #610 | Provider conformance; `TestBudgetAutoPause`, `TestNoProviderKeyInRuntime` |
| A13.1 | M40.13 #611 | `TestNoSecretInRuntimeEnvArgvOrFiles`, `TestMetadataCIDRBlocked`, `TestEgressRequiresRunToken`; broker contract tests at the module's 85% floor; credential-broker README follows the template (exemption removed) |
| A14.1 | M40.14 #612 | `TestForgedRunTokenRefusedBeforeBackend`, `TestRunAliasRefused`, `TestStaleGenerationRefused`, `TestPolicyDigestMismatchRefused`, `TestRevocationCascadesToAssignmentRuns`, `TestForeignSpaceTokenRefused`, `TestHeaderInjectionIgnored`, each with a backend spy at zero calls |
| A14.2 | M40.14 #612 | No `X-Hub-Principal`, control-token principal assertion, header fallback or unsigned admission left |
| A15.1 | M40.15 #613 | Backend conformance on kind and firecracker (nightly); store suites on Postgres; `TestOnlyOneCopyRunsSweeper` |
| A16.1 | M40.16 #614 | Profiles x backends matrix (space, first message, one tool per family, one routine) with receipts; v0 import and rollback rehearsal on a copy of live data; load within SPEC-0101; T01-T22 owner sign-off (R7.1) |

Common to every service row A3..A13: README from the template, state
machines in `docs/state-machines.md`, no `.json`/`.jsonl` service state,
tenant invariants and the tool snapshot green, old code deleted.
