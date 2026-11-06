# CHG-0284: M32 bounded assignments

Current contract: [SPEC-0099](../../../specs/active/SPEC-0099-m32-automation-slices.md).
Delivery decision: ADR-0097. This plan replaces the old stage-wide assignment;
dated evidence stays in state.yaml. No new runtime work is claimed here.

## Read and execute

Read AGENTS.md, README.md, docs/index.md, the assigned GitHub issue and the
contract's invariants plus its row. Historical ADR/spec/audit chains are optional
reference, not prerequisites for every worker.
Owner update 2026-11-06: one persistent worktree/branch per track; execute
small stages sequentially, with an issue commit after its coherent contract passes.
Use the current parallel-work plan and CHG-0289 Devin handoff; no new foundation.

## Assigned slices

- A1 / #521: deliverable, acceptance and exclusions are frozen in the contract row.
- A2 / #522: deliverable, acceptance and exclusions are frozen in the contract row.
- A3 / #523: deliverable, acceptance and exclusions are frozen in the contract row.

#521 is committed on the existing track; #522 after #521; #523 after #522.
The old CRUD-first split is retired. Only #523 switches all live ownership.

For #523 the single cutover must: bind supported per-owner file-backed
automation roots into ToolHub and reject incompatible layouts before
listening; expose the host through authenticated service calls; route
routine, assignment and watch HTTP/chat callers through it; stop communication's
automation clock, writer and dispatch startup in the same commit. Keep
communication's channel intake, delivery queue and receipts. Verify that an
existing record is read in place, a stale owner never executes, and a failed
delivery leaves completion intact. The container acceptance remains pending
until the isolated #526 project.

The service RPC takes its caller envelope from the trusted communication
service, while rights are decided on ToolHub's enrolled identity for that
principal; using the channel envelope for the PDP would bypass a managed
profile ceiling. Schedule create/replay/list/destinations/card/history/preview,
edit and lifecycle controls, watch sample and complete result handoff now cross
this boundary.
The RPC now also covers revision-fenced schedule pause/snooze/delete,
assignment create/list/load/lifecycle/plan/grants/progress, terminal result
handoff and owned hook intake. Progress accepts the job only during its
ToolHub-owned live supervisor call, after checking assignment/generation and
owner context; a repeat of the already recorded job is read-only/idempotent.
The occurrence authorization callback now reads an operator-rendered,
credentials-free channel identity view and the gateway's read-only task/link
records. ToolHub checks them after the owner occurrence and live schedule,
immediately before supervisor admission; a rebound channel leaves the durable
dispatch intent pending. Communication HTTP is not needed for this check, so
an outage delays delivery rather than due execution. The same local fence
applies to probes/scripts.
The ToolHub entrypoint now constructs/recover-checks Host before opening its
control listener when the automation root is configured, mounts its
service-only operations under `/v1/automation/`, and starts the sole routine
tick loop. The old communication routine and stream loops are removed from
Run; its independent night rollup stays until #524/#525. Communication's
interactive run-event receipt recovery remains. Public assignment, routine
and watch callers use the service RPC. Communication
exposes a safe owner home chat/language response for notice routing; it is not
on the due execution authorization path.
The static runtime exposes `/v1/execute`, not supervisor `/v1/jobs` or
`/v1/script`. ToolHub now selects the existing `/v1/execute` contract for
single-principal static deployments and refuses a multi-principal static
topology. Isolated script execution remains unsupported there, as it was in
communication; only the supervisor path offers `/v1/script`.
The ToolHub engine now reads communication-owned task, topic and interactive
origin mapping records in place from the read-only legacy mount. Its create
RPC binds a destination using the operator-rendered current owner and can
carry the creation-time check baseline; its destinations RPC lists the same
live bindings. An owner change or topic rebind refuses a new schedule.
The communication fixtures now exercise a real ToolHub Host through service
HTTP. ToolHub owns occurrence compaction: it keeps pending channel sends in
the hot set, archives settled detail and retains compact owner-bound receipts.

The production communication spool is a single Docker volume shared by all
principals, with automation records at its top level. That is not the A2
per-owner root. Production startup detects populated shared legacy automation
records, activated SQLite and pending/running old cron jobs, then refuses
cutover without writing to the volume. The per-owner in-place tests prove
only the supported A2 file layout, not migration of that shared spool.
Render now creates `spaces/<user>/automation/<env>` and Compose binds existing
owner directories into ToolHub at `/automation-owners/<user>`; dev/prod are
distinct. The old shared communication volume is mounted read-only at
`/legacy-communication` for preflight. Host startup now checks them before
opening its listener.
Production startup must pass the enrolled principal list into the preflight:
a missing sibling mount is a failure before any owner classes are created.
The file cutover also refuses nonempty legacy event-state records: the new
event checkpoint has a different layout, so accepting them would silently
restart a cursor. Validate every owner's layout before creating any owner
classes. Tool probes use ToolHub's projected read-effect call fence in the
same process; the HTTP probe path keeps its existing response contract.

Code starting points: internal/routines/engine.go; internal/communication/spool.go; internal/communication/assignments.go; internal/communication/occurrences.go; internal/toolhub/server.go.
Follow callers before edits; listed anchors do not limit required caller fixes.

## Verification and boundaries

Each contract row is separately reviewable; record checked/pending acceptance
items, exact SHA, commands/exit codes and remaining risks in this CHG evidence.
Run scoped regressions and fast just check; full race/coverage (own Go >=85%)
remain CI gates under CHG-0289. Apply the current
AGENTS.md image/security gates. Mocks are not integration proof.
Tool contract changes ship description/prompt/docs/catalog together; localized
text and safe audit rules apply. Preserve user data; no compatibility fallback,
new service/database/queue or upstream Hermes patch.

Shared-file/merge order and ID dependencies:
[parallel-work](../CHG-0281-m32-per-space-memory-plane/parallel-work.md).
GitHub is the current issue-status authority. Do not mark the whole CHG done
when only one slice passes; continue the owner-authorized track sequence after
recording the completed stage and actual dependency blockers.

