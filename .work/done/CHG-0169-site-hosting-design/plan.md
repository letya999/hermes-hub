# CHG-0169: User-scoped hosting design pass (M18 #147)

Chain: research.md → [ADR-0081](../../../docs/adr/ADR-0081-user-scoped-site-hosting.md)
(Accepted) → [SPEC-0079](../../../specs/active/SPEC-0079-user-scoped-site-hosting.md)
(Frozen) → this completed design plan → state.yaml → CHG-0170–0174.
Expanded source comparison: [ecosystem-research.md](ecosystem-research.md).
Pinned Haloy architecture/adoption review: [haloy-review.md](haloy-review.md).
Base `5c19f3e`; worktree `hermes-hub-m18-hosting`; branch `feat/m18-exposure-hosting`.

## Goal / scope

Resolve dynamic isolation, serving/access, storage, budgets and lifecycle before
code. Read ADR-0079/0078, SPEC-0077/0076 and CHG-0177–0168 first. Keep their
accepted records immutable and their pending live evidence explicit.
Initial pass changed only research/ADR/SPEC/CHG and indexes. On 2026-10-26 the
owner accepted the researched direction and requested the current issue be
carried through ADR/SPEC/CHG/state: accept/freeze, allocate implementation
records and synchronize #147. No Docker actions, implementation, dependency
or runtime changes belong to this design CHG.

## Design slices

1. Research local boundaries and primary upstream docs; distinguish observed
   code from proposed guarantees and live prerequisites.
   Follow-up: Context7 Dokku/Coolify/OpenSandbox; ready-made GitHub solutions;
   provider workflows; X/Substack/DEV/Reddit/HN/Medium/Habr/arXiv/Archive search.
   Record unavailable surfaces rather than claim they were read.
   Haloy follow-up completed: clone at pinned commit, critical-path source
   review, selected unit/race tests and static scan. Prefer existing controller
   extension; no unmodified adapter or lifecycle fork, no code extraction yet.
2. ADR-0081 accepted after owner review: admitted site workload, per-user Go web tier, immutable
   volume snapshots, expiring revocable bearer links, explicit public mode.
3. SPEC-0079 frozen after acceptance: identity, agent surface, transaction/recovery,
   isolation, budget defaults and A1–A11 acceptance design.
4. Synchronize indexes, run docs gate and `just check`; record unavailable gates
   honestly. Design complete; move this CHG to done, leave implementation and
   live acceptance explicitly not_started in the successor records.

## Subsequent implementation sequence (allocated, not started)

Detailed plans and states: [CHG-0170](../../in-progress/CHG-0170-hosting-store/plan.md),
[CHG-0171](../../in-progress/CHG-0171-hosting-web-access/plan.md),
[CHG-0172](../../in-progress/CHG-0172-hosting-static-delivery/plan.md),
[CHG-0173](../../in-progress/CHG-0173-hosting-dynamic/plan.md),
[CHG-0174](../../in-progress/CHG-0174-hosting-acceptance/plan.md).

Each slice is a separate commit; add red regression cases before behavior.

| Stage | Anchors to extend / introduce | Behavior and regression cases |
|---|---|---|
| H1 Slot store / snapshot / quota | new `internal/hosting/{config,store,publish}.go`; existing `internal/identity`, os.Root, audit ledger | Trusted owner/env binding, reserved endpoint namespace, strict manifest, atomic revision and cross-process reservations; A1–A4 (`TestPublishCrossOwnerDeniedBeforeIO`, `TestPublishQuotaReservationAcrossProcesses`, `TestSnapshotSymlinkSwap`, `TestPublishCrashRecovery`) |
| H2 Static / access gate | new `cmd/hosting`, `internal/hosting/{http,access}.go`; private authenticated control/public data listener pattern from communication | Immutable static, exact Host, fragment bootstrap, epoch cookies and HTTP limits; A5/A6/A10 (`TestServeRevokedShareRangeDenied`, `TestUnknownHostNoRead`, browser sibling-cookie case) |
| H3 Tools / routing / recovery — complete MVP-S | `internal/agenttools/exposure.go` pattern; new hosting tools; `internal/exposure/{config,service,sish}.go`; `internal/stack` feature/render/mount wiring | Trusted derived endpoint delegation, shared name/cap reservation, durable reconnect and cleanup fences, Hermes-independent service; A9/A11 static (`TestUnpublishFencesReconnect`, `TestSiteExpirySurvivesRestart`) |
| H4 Dynamic admission — MVP-D | `internal/toolhub/cell_lifecycle.go:createCell`, `cell.go:runtimeFlag` and profile/inspect/finalizer patterns; new hosting workload request/lifecycle in `internal/toolhub/api` | Separate persistent site class, one python-http stdlib profile, explicit runsc/Kata, per-site ingress relay, no install hooks/egress/secret defaults; A7/A8 (`TestHostingInspectDriftDenied`, `TestHostingMissingSecureRuntimeDenied`, runtime-specific limits/isolation canaries); Broker revocation case when opt-in env lands |
| H5 Acceptance / documentation | `docs/{architecture,operations,threat-model,tools,spaces}.md`, new `docs/services/hosting.md` | Deploy only by later instruction; real sish, browser and executor A6/A8/A11 evidence, full checks; optional images stay out of base |

H1 defines delegation and store contracts before H2/H3. H3 includes actual
static agent/stack/exposure wiring, browser and sish acceptance; an unconnected
handler is not MVP-S. H4 follows a working static delivery and still gates
completion of #147. Select one writer/replica per user/env, no DB/Redis,
multi-replica counters, dashboard or autoscale. Build bytes in workspace only
through the admitted execution boundary; hosting runs no install/build hooks.
Broker opt-in env delivery follows existing contracts; no new provider API.

## Red-team / live procedure for later implementation

- Use two synthetic users with distinct file/credential sentinels, same site
  label and independent env stores. Run A1/A3/A7/A8, observe both denied effects
  and untouched other-user bytes/routes. Never save real personal state.
- Run a static multi-asset page and an approved stateless HTTP app over real
  sish with per-user wildcard TLS. Confirm original Host or enforce a per-slot
  listener; check unknown host, TLS, bearer revocation and relay logging.
- Stop Hermes, restart hosting and interrupt publish/cleanup. Observe site
  continuity while valid, no resurrection after unpublish/expiry, finalizers
  reach zero without broad Docker/volume pruning.
- Exercise hard CPU/mem/PID/tmpfs caps, aggregate overlap, full audit queue and
  storage reservation crash. No success claims from mocks.
- Verify exclusive writer lock and simultaneous publish requests separately;
  a second daemon must refuse before mutation, API concurrency still enforces
  quota. Missing runsc/Kata denies dynamic instead of silently choosing runc.

## Design acceptance / handoff

Owner accepted the direction on 2026-10-26 following Haloy research: existing
controller/ToolHub plus sish/DNS; no Haloy fork, static first then one stateless
dynamic profile, bearer-default/public-opt-in, finite site lease and secure
runtime prerequisite. ADR accepted and SPEC frozen; every successor CHG has
ordered anchors, red-test scenarios, requirements, dependencies and explicit
unexecuted acceptance. This completes design only, not #147 or live guarantees.
The final completion/verification record is state.yaml; implementation starts
at CHG-0170 and issue stays open until required static+dynamic acceptance.

No visitor identity provider, arbitrary image/Dockerfile builds, persistent
writable app disk, websocket/streaming, domain registration, new DNS provider,
shared multi-user daemon, or upstream Hermes changes.
