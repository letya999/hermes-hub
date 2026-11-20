# CHG-0336: Extract spawn-controller (M40.3)

Issue: #601. Milestone: M40 - v1 Service Split. Rules: docs/engineering.md.
Chain: ADR-0105 (topology), ADR-0106 (layers), ADR-0110 (storage) ->
SPEC-0100 -> this plan -> state.yaml. Previous stage: CHG-0335 (M40.2).
Next stage: CHG-0337 (M40.4, #602).

## Goal

`services/spawn-controller` owns spaces, the six passports per layer, runtimes,
workers, mounts, image pins and the warm pool in one `spawn-controller.db`,
with `process` and `docker` launch backends. The v0 supervisor spawn code,
`generated/passports/current`, `apply-journal.json`, `.policy/<hex>` and
config-view files are deleted; everything listed under "Keep working" in #601
keeps working through the new service.

## Decisions (fixed for every slice)

1. Layout. `services/spawn-controller/main.go` stays thin. Code lives in
   `services/spawn-controller/internal/`: `spawn` (store, migrations, machines,
   spaces, revisions, runtimes, workers, warm pool, importer), `backend`
   (interface, `process`, `docker`, conformance suite; kubernetes/firecracker
   moved here in S8 compile-only), `api` (HTTP handlers). Compiler and render
   code moves under `internal/passport` and `internal/render` of the service in
   S8 once no outside caller remains. No other packages without a reason in
   the CHG.
2. Contract. `contracts/spawn/v1`: DTOs, one `Client` interface, in-process and
   HTTP transports on `shared/httpapi`, `contracts/spawn/v1/fake`, conformance
   run through `shared/testkit.Transports`. Template: `contracts/task/v1`.
   hubctl, ToolHub and runtimes talk to spawn-controller only through it.
3. Digest. The policy digest is `sha256:` of the canonical policy bundle
   (`internal/passport/policy.go` `PolicyBundle().Canonical()`). The
   provenance-based `r-<16hex>` id (`passport.go:418-445`) is a label only and
   never a key.
4. Layers. `passport.Inputs` (`passport.go:43`) gains an `Agent` layer beside
   platform ceiling/template, org ceiling/template and user; `Compile`
   (`passport.go:191`) merges platform -> org -> user -> agent and a lower
   layer only narrows. Same six documents, no new format. One revision row
   per (space, layer); the effective spec is computed from the active row of
   each layer and has its own digest.
5. Tenancy. Every row carries `org` and `owner` (D4). A space has an owner
   (the user) and an org; until identity-hub (M40.5) the installation org is
   `local`, set by the importer and by space creation, never taken from a
   request header.
6. Transitional imports. Until S8 the service may import `internal/passport`,
   `internal/reconcile` and `internal/stack` read-only; each such import is
   listed in state.yaml `remaining` and removed by S8. No new code is added to
   `internal/supervisor`.
7. Docker backend shells out to the docker CLI like v0
   (`internal/supervisor/backend.go:37` `dockerBackend`, `ensure.go:521`
   `runArgs`, `ensure.go:574` `managedRunArgs`). No Docker SDK dependency.
   OCI runtime from config (`runc`|`runsc`), moved from `supervisor.go:472-478`.
8. trusted=false refuses `process` and refuses `docker` unless the OCI runtime
   is `runsc` (X6), before touching disk.
9. Lease count, warm/busy and "apply in progress" are derived from rows, never
   stored flags (F5).
10. Warm pool (decided at S4). Mounts are fixed when a container starts and a
    runtime mounts only its own space's workspace (X2), so an unbound
    pre-started pool is impossible. The pool is the set of `warm` runtime
    rows keyed by (org, owner, space, agent, policy digest, image digest):
    the claim that starts a runtime binds tenant and digests for that
    generation, and a later claim reuses a warm row only on an exact key
    match. Nothing warm is ever handed to another tenant, space, policy or
    image. A new activation drains every runtime bound to the old digest.
12. Workers (decided at S6). spawn-controller is the only container
    authority for cells: create, exec, stop all go through it, so the
    backend gains `Exec(ctx, id, ExecSpec)` (docker: `docker exec`; process:
    trusted-only child in the worker dir). Workers follow the Worker table
    (no warm): a worker is bound to (org, owner, space, cell key, image
    digest); ToolHub holds and renews a lease while it wants the cell kept,
    release drains it. The v0 per-image CLI pool that hands cells across
    users is deleted: no worker is reused across tenants or spaces. Cell
    sidecars (squid egress, cred-proxy) are part of the worker Spec; the
    docker backend creates them on the worker's network, the process backend
    refuses specs with sidecars. ToolHive MCP workloads stay in ToolHub until
    M40.7.
11. Effective digest pointer (decided at S4). Activation stores the effective
    digest on the space row in the same transaction; readers and claims use
    the stored digest and never recompile on the hot path.

13. Supervisor scope in M40.3 (decided at S7). The v0 supervisor also hosts
    jobs/execute/resume, wake, holds, control, artifact/usage/compact/goal,
    self-env, skill-*, session-read and identity signers; those leave with
    M40.6 (jobs) and M40.5 (identity). In M40.3 the supervisor stays in the
    compose stack as that host and loses only container and passport
    ownership: it claims runtimes through `contracts/spawn/v1` and forwards
    to the returned endpoint; its spawn code is deleted in S8. S7 is split so
    the dev stack works after every commit: S7a spawn-controller in the
    stack + runtime claim/lease routes in the contract; S7b passport writer
    cutover (apply, settings) with a transitional v0 file mirror; S7c
    supervisor Ensure/respawn through spawn/v1, Hermes config render and
    mount plan at spawn inside spawn-controller; S7d readers by digest over
    HTTP, mirror/journal/`current`/config-view writers deleted.

## Slices

Every slice: tests red first, delete what it replaces, `just check` and
`just test` for affected packages, commit. One slice per delegated run.

### S1 store-and-machines

Anchors: `shared/store`, `shared/fsm`, `shared/store/storetest`;
`internal/supervisor/lease.go`, `internal/supervisor/lease_registry_test.go:137`;
`docs/state-machines.md` Runtime, PassportRevision.

Change:
1. Move the skeleton code from `services/spawn-controller/*.go` into
   `internal/backend` (process backend) and `internal/api` (handlers);
   `main.go` wires them. Behaviour unchanged; TestMinimalProfileAnswersMessage
   stays green.
2. `internal/spawn` migrations `0001_init.sql`: `spaces`, `passport_revisions`
   (+ history), `runtimes` (+ history, `generation`), `leases`, `workers`
   (+ history), `image_pins`. All with `org`, `owner`.
3. Machines on `shared/fsm` exactly as the v1 target tables in
   docs/state-machines.md: PassportRevision (draft, validated, rejected,
   active, superseded), Runtime (absent, starting, warm, busy, draining,
   failed, quarantined, missing), Worker (Runtime without warm). Deadline and
   sweeper for every non-final state.

Tests (red first): FSM table tests per machine (allowed and forbidden edges),
deadline per non-final state, `TestLeaseCountDerivedFromLeaseRows` (spawn
version), `TestTenantInvariants` for every table.

### S2 revisions-and-contract

Anchors: `internal/passport/passport.go:31,43,191,418`, `authored.go:109,123`,
`policy.go:15,57`, `publish.go:34,121,184,206`; `contracts/task/v1`.

Change:
1. Agent layer in `passport.Inputs` and `Compile` (Decision 4).
2. `spawn` revision service: submit layer docs -> draft; compile -> validated
   or rejected; activate is compare-and-set on the current active row of that
   layer; the previous active becomes superseded; rollback = activate an older
   superseded revision. Effective spec + digest computed from active rows.
3. `contracts/spawn/v1` + fake + conformance; endpoints for submit, validate,
   activate, rollback, get effective, and
   `GET /v1/spaces/{space}/policy/{service}?digest=` (immutable slice, 404 on
   unknown digest, cacheable headers).

Tests: `TestRevisionRollbackActivatesOlder`, `TestApplyCrashLeavesPreviousActive`
(crash between validate and activate leaves the old active serving),
`TestEffectiveSpecIntersectsLayers` (agent cannot widen user),
`TestPolicySliceByDigestImmutable`, contract conformance in-proc and HTTP,
fake passes the same suite.

### S3 backends

Anchors: skeleton `processBackend`; `internal/supervisor/backend.go:15,37`,
`ensure.go:521,574,589`, `scripts.go:83`, `sandbox.go:13`, `supervisor.go:472`;
`internal/stack/mount_plan.go:27`.

Change:
1. `backend.Spec` fully resolved by spawn-controller: image digest, mounts
   (only this space's workspace + its config slice), env without secrets,
   network edges, limits, OCI runtime. Backends never decide policy.
2. `process` backend from the skeleton on the new Spec; `docker` backend moved
   from v0 (docker CLI). Trust guard (Decision 8).
3. One conformance suite in `internal/backend/backendtest` run against both;
   docker leg skips only when the docker CLI or daemon is unavailable, with a
   named reason.

Tests: conformance start/stop/inspect/list, `only own workspace mounted`,
`no secret in env/argv`, `network edges applied` (docker: network args;
process: refused when edges require isolation),
`TestUntrustedRefusesProcessBackend`, `TestUntrustedDockerRequiresRunsc`.

### S4 runtime-lifecycle-and-warm-pool

Anchors: `internal/supervisor/ensure.go:23`, `lease.go` (Acquire, Release,
Reap :235), `execute.go:299`, `http.go:213`; S1 machines.

Change:
1. Replace the in-memory registry with Runtime rows: claim -> starting
   (generation+1) -> warm on readiness; lease rows move warm<->busy; idle TTL
   -> draining -> absent; inspect-nothing -> missing; stop not confirmed ->
   quarantined. Sweeper drives deadlines.
2. Warm pool keyed by image digest; tenant (org, owner, space, agent) and
   policy digest bound at claim; no cache or volume shared across tenants.
3. Activating a new revision drains runtimes bound to the old digest.
4. `POST /v1/messages` path uses claim/lease/release.

Tests: `TestWarmClaimBindsTenantAndDigest`, `TestRevokeDrainsOldDigest`,
`TestStartingDeadlineFails`, `TestInspectNothingMarksMissing`,
`TestControllerRestartRecoversFromRows`.

### S5 importer-explain-doctor

Anchors: supervisor state files (`supervisor.go:618,745`, `status.go:384,412`,
`incidents.go:113`, `backups.go:41,91`, `rollout.go:43,64`),
`generated/passports/current`, `internal/passportview/view.go:31,43`.

Change:
1. Importer on first start (empty DB): runtime part of `supervisor.json`,
   `transitions.jsonl`, `signals.jsonl`, `backups.json`, `image-pins.json`,
   current passport revision (as active user layer). Dry-run report; never
   rewrites the source files.
2. `hubctl explain <space>`: effective value and source layer per field
   (through the contract). `hubctl doctor --fix`: migrate passport/config
   shapes, no aliases.

Tests: importer on real-shaped fixtures (copied shapes, no personal data),
`TestImporterDryRunWritesNothing`, `TestImporterIdempotent`,
`hubctl explain` golden.

### S6 workers-and-cells

Anchors: `internal/toolhub/generic_controller.go:42,165,327`,
`generic_handler.go:22`, `cell.go`, `cell_exec.go`, `cell_lifecycle.go`,
`cell_init.go`, `cell_net.go`, `api/local_controller.go`,
`api/workload_budget.go`.

Change: cell creation (terminal/CLI/MCP, cellinit seeding, cred-proxy and
egress sidecars) moves behind `backend` as Worker with Worker rows; ToolHub
asks spawn-controller for a worker through `contracts/spawn/v1`. The CLI
cell pool becomes the worker warm pool keyed by image digest. ToolHub keeps
ToolHive orchestration and admission.

Tests: Worker FSM through the API, cell conformance on docker,
`TestWorkerMountsOnlyOwnSpace`, existing cell tests moved, not duplicated.

### S7 consumers-cutover

Anchors: `cmd/hubctl/space_apply.go:53,197,394,533`,
`space_apply_recovery.go`, `space_settings.go:26`, `main.go:494-527`;
`internal/toolhub/settings.go:22,87`; `internal/supervisor/http.go:133`,
`settings.go:21`; passportview callers (runtime, toolhub, stack, supervisor,
hubctl); config-view readers (`agenttools/boundary.go:35`,
`runtime/runtime.go:207,330`, `server_observe.go:436`); compose
`stack/compose.go:499,537,686`; `dev_rebuild.go:43`; `backups.go:158`.

Change (may be split into S7a apply+settings, S7b readers, S7c compose):
1. `hubctl space apply` submits layer docs to spawn-controller, which
   validates, activates and runs apply classes (`apply`: live, respawn,
   recreate, rebuild) from `internal/reconcile`. The apply journal and the
   `current` pointer are gone; a crash leaves the previous active revision.
2. `settings_read`/`settings_edit`: ToolHub -> spawn-controller contract.
3. Every config-view reader fetches its slice by digest over HTTP; delete
   config-view writers and `passportview` use outside the service.
4. Compose stack runs spawn-controller in place of `hubctl supervisor`;
   image pins, backups and dev-rebuild go through it. Hermes config render
   and the mount plan run at spawn inside the service.

Tests: `settings_edit` end to end against the real service,
`TestApplyCrashLeavesPreviousActive` through hubctl, existing apply tests
adapted (journal tests deleted with the journal),
TestToolCatalogSnapshotUnchanged untouched.

### S8 delete-v0

Change: delete moved code from `internal/supervisor`, `cmd/supervisor`,
`internal/stack`, `internal/passport*`, ToolHub workload controller; move
kubernetes/firecracker under `internal/backend` compiling behind the
interface (not selectable until M40.15). `rg` evidence of zero callers.

### S9 docs-and-acceptance

Change: README from template, docs/state-machines.md v1 tables marked live,
docs/architecture.md and docs/engineering.md links, SPEC-0100 acceptance.
Acceptance tests: create space + first message in `process` and `docker`
(current runtime image). Live non-destructive smoke on a dev space only with
owner OK.

## Red-team checklist

- A request for space B with space A's digest gets 404, never A's slice.
- An agent layer that lists a tool the user layer denies does not enable it.
- A warm runtime claimed by tenant A is never handed to tenant B, even with
  the same image digest.
- trusted=false never starts a process or a runc container.
- No secret appears in Spec env, argv, rows, logs or contract DTOs.

## Acceptance

#601 Acceptance list: process and docker first message; no `.json`/`.jsonl`
state written by the service; old supervisor spawn code deleted; tool
snapshot unchanged; `settings_edit` end to end; README; live smoke (owner OK).

## Out of scope

kubernetes/firecracker backends as selectable (M40.15), jobs (M40.6),
identity and run tokens (M40.5, M40.14), model/egress relays (M40.12/13).

## Done checklist

- [ ] Every slice red then green; `just check`; affected `just test`.
- [ ] Red-team cases covered by tests.
- [ ] README, state-machines.md, architecture.md updated.
- [ ] Live smoke recorded with owner OK; CHG moved to `.work/done/`.
