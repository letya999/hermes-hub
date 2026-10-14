# CHG-0091: Repair Telegram operation contracts

Scope authorized on 2026-10-13: research, audit and concrete design artifacts.
Production implementation, deployment and live external sending are not performed
by this change's design phase. Baseline: dev HEAD e059aa8 plus ongoing CHG-0088/0089/0090
uncommitted work. Re-read dirty source before implementation; never reset that work.

Chain: [ADR-0035](../../../docs/adr/ADR-0035-telegram-operation-contracts.md) (proposed)
-> [SPEC-0052](../../../specs/active/SPEC-0052-telegram-operation-contracts.md) (draft)
-> this plan and [state](state.yaml). Research is [here](research.md);
test cases and independent oracles are [here](verification-plan.md).

## Implementation slices

| Slice | Requirement | Concrete code boundary | Deliverable and exit condition |
|---|---|---|---|
| P0 | R2, SPEC-0051 | stack Config, applyHubCommunicationConfig, hubCommunicationEnv; existing CHG-0090 checks | Reconcile completed versus pending work; define minimal child env including model key/config locator; canary uses pinned Hermes loader, not manually expanded env |
| P1 | R1 | communication handleFile/inbound; media inspectMany/imageFile/sniffImage/imageBounds; agenttools scopedRoots | Byte-based input format, safe photo names, all-input validation, explicit missing-key result; original isolation/limits and no-partial-send tests remain |
| P2 | R3 | agenttools Server/HubToolsDefinition/Routine; communication validateScheduleDraft/settleProbe; runtime run instructions | One reviewed routine description source used on both surfaces; URL diagnostics; real-agent watcher creation and observable tick results |
| P3 | R4 | communication handleRoutines, createSchedule, ValidateSchedule, UpdateSchedule and settleScheduleLocked; agenttools Routine | Normalize relative reminder once; immutable creation receipt; owner-bound locked replay/conflict; no relative rules persisted into recurring settlement |
| P4 | R5 | communication deliverOne, closeApprovalMessages, trackApprovalMessage, RequestApproval/controlOne/stream receipts | Current-request send gate, decision/application distinction, durable send/cleanup progress; no stale or foreign keyboard after restart |
| P5 | R6 | communication HTTPRunner.RunOutcome; agenttools Server descriptions; runtime extractMediaArtifacts/stageMediaArtifact; gateway artifact delivery | Exact buckets/instructions and original-byte attachment tests; no broad scan expansion or implicit private-state exports |
| P6 | R7 | subsequent communication group intake/spool, Job; runtime ExecuteRequest/fetchInbound; agenttools images_inspect | Bounded durable album collection with explicit late/over-limit behavior; own HTTP/identity/recovery tests before enabling |

P0 must precede image integration. P2 needs working routine env but no image work.
P3/P4 use existing durable spool ownership; P5 changes guidance and tests, not the
artifact allowlist. P6 starts only after core P0-P5 acceptance and a separate
implementation checkpoint. Do not add dependencies or patch upstream Hermes.

## Detailed implementation decisions

**Images.** Introduce the smallest shared read/inspect helper inside media using
existing readRegular, sniffImage and imageBounds. inspectMany and imageFile both
consume its actual format/bytes; image_convert output format stays explicit.
Generate synthetic photo names in the bounded ingress path; do not rewrite existing
workspace files. Existing extensionless receipts become readable without migration.
Test misleading MIME/extensions using synthetic fixtures, never private photographs.

**Child env.** Extend the existing allowlist only for the trusted hub child; optional
config override must not leave an unresolved literal replacing the default path.
Required-variable checking must be limited to selected capabilities. Use a temporary
Hermes home and pinned tools.mcp_tool_config._load_mcp_config plus the real upstream
transport (MCPServerTransportMixin/MCPServerTask) to spawn hubctl. The old raw-SDK
canary can remain as a narrow smoke. Both dev/prod images must exercise the loader.
Observe exact synthetic auth/owner headers and multimodal payload at local fixtures;
unrelated fake provider keys set in the parent must not reach the child.

**Metadata.** Extract shared descriptions, not a new tool framework. Keep direct
legacy schemas compatible and retain the projected single-path image_inspect versus
paths-based images_inspect distinction. Descriptions help selection but never grant
rights. Test discoverability via tools/list and a real-agent scenario that validates
the independently read schedule, not an exact assistant sentence.

**Creation.** Use one clock seam for HTTP/direct spool creation. Validate/normalize
after expressions once before persistence; add an optional immutable creation receipt
to Schedule containing requested_expression, resolved_due_at, created_at and
intent_sha256. Hash a canonical typed intent with server-resolved identity/audience,
not the response or changing schedule fields. Read matching creation receipt before
validating a retry deadline. Preserve it on updates and in new deletion tombstones.
On exact replay return receipt + current state, never restore an old enabled state.
Keep legacy records/tombstones conservative. Creation path is one locked atomic
schedule write: no separate dedup database/receipt transaction. HTTP replay and
conflict checks must occur before network work on check/probe requests.

Changing an ID remains a different operation. There is no trusted per-intent/job
ID in AgentExecRequest today; do not invent one from session text or suppress all
second creates in a job (a user may intentionally ask for two reminders). Relative
time and observed-status guidance fix this incident; universal semantic dedup needs
a separate trusted-intent contract if later required.

**Approvals.** Centralize a small actionable-request predicate used at send and
cleanup. Recheck current user/policy, full audience, request/deadline and control
decision under the existing synchronization boundary. Retain immutable run and
generation provenance on new approval deliveries/receipts; reconcile older queued
records before making them actionable. When intent is saved, no new
actionable keyboard may go out. Existing buttons cannot bypass RequestApproval.
Delete resolved messages after applied/reconciled/terminal state; pending/uncertain
state stays visibly unresolved. Known successful send plus failed local persistence
must not enter the normal resend path. Retain enough durable progress to retry only
receipt cleanup, or classify the unresolved send as uncertain. Existing native
approval control and held leases remain intact.

**Albums.** Add media_group_id to the Telegram message contract and persistent group
records to the existing spool. No process-only sleep/debounce. Freeze member list
after the quiet/hard window, store ordered trusted IDs and one stable group job ID,
then admit through the existing queue. Recover interrupted collection/admission;
each member fetch must succeed before the model starts. Reject >8/conflicting
identity/total-byte limits and report late arrivals without re-execution. This is
a separate slice because it changes HTTP contracts, job fingerprints and recovery.

## Migration and rollback

1. Reconcile CHG-0090 completion, deployment revision and current tests before
   editing common files. Its author remains responsible for its existing work;
   CHG-0091 does not change its state to done or re-claim its historical evidence.
2. New tool descriptions/schema capabilities require an immutable successor to
   hub-agent-tools 1.2.0 and reviewed digest/profile/binding renewal. Reuse the
   current registration lifecycle. Do not auto-select images_inspect or add rights.
3. Additive receipt fields need round-trip tests through every schedule writer,
   updater, offline apply path and tombstone writer. Inventory with LSP/ripgrep.
   Older JSON readers ignoring fields does not prove older writers preserve them.
4. Before rollback: stop new admission, drain effects, preserve an operator-private
   spool backup, restore compatible image/config/catalog together. Do not run an
   old mutating writer over new receipts/album state. Never rewrite sent delivery
   history, resurrect retired IDs, or touch another stack/volume.
5. Existing extensionless images require no renaming. Existing legacy schedules
   continue firing unchanged; old create retries conflict rather than guessing intent.

## Delivery gates

Observe failing behavioral regressions before fixes, review their assertions with
test-critic, then run focused packages and one full just check (>=85% own Go coverage,
race/shuffle, static checks, docs, workflow, config matrix). Run just security only
if dependencies change. No dependency addition is planned.

Image-side changes use hubctl build/up or just docker-check; otherwise prefer
just docker-pull + just docker-check-prebuilt. Keep heavy published-image CI gates.
Record synthetic contract PASS separately from pinned-Hermes Docker PASS and
owner-authorized Telegram/model PASS. External sending/publication is a separate
action; use synthetic mock fixtures until its actual instruction exists.

Completion requires all core R1-R6 cases, no unsolicited grant expansion, real
single-photo/no-LLM reminder/watcher/approval/exact-file evidence for the selected
context, and honest BLOCKED outcomes for missing capabilities. P6 has its own
acceptance result. Design delivery itself requires linked artifacts and docs/source
checks; it is not a claim that any planned repair shipped.
