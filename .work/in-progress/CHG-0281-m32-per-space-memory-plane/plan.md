# CHG-0281 — M32: delivery plan

Current: [milestone 43](https://github.com/letya999/hermes-hub/milestone/43)
and [atomic assignments and dependencies](parallel-work.md).
On 2026-11-05 the owner removed the separate foundation stage: integrate the
reviewed current changes into `dev`, then branch three lanes from that SHA.

## Scope and current facts

One user = agent = spaces/<user>. Six passports are sole authored agent config.
Tools selection and issued rights are independent. ToolHub owns per-user memory
and automation; communication owns trusted transport/delivery.
No new service, compatibility path or automatic config/data conversion.

Authored compile/publication, legacy reader removals, bound-profile rollback and
tool projection have progressed. Selected path-scope code exists. Self-settings
still overrides effective config; complete activation/recovery, ToolHub vault
startup, exact-run memory and automation ownership remain unverified/incomplete.
These are source observations, not new acceptance results. Historical gate passes
in CHG-0283 apply only to their snapshots.

## Read set

Common base: review and commit current changes, run gates, merge into `dev`.
Worker: those root rules + assigned issue + invariants/own row of SPEC below.
Do not require all 20 issues, audit transcripts or old ADR/spec chains.
[ADR-0097](../../../docs/adr/ADR-0097-m32-bounded-delivery.md) records the split.
ADR-0095/0096 and SPEC-0094/0095 remain historical invariant references.

| Work | Current contract | Evidence owner |
|---|---|---|
| Common base integration into dev | repository gates and reviewed change set | CHG-0281 |
| #508–#512 configuration | SPEC-0097 | CHG-0283 |
| #513/#514/#516/#517 vault/events/run binding | SPEC-0098 | CHG-0285 |
| #515/#518/#519/#520 memory APIs | SPEC-0098 | CHG-0286 |
| #521/#522/#523 engine/host/owner switch | SPEC-0099 A1–A3 | CHG-0284 |
| #524/#525 dreaming | SPEC-0099 A4–A5 | CHG-0287 |
| #526/#527 integration/cutover | SPEC-0097/0098/0099 gates | CHG-0288 |

## First wave and boundaries

After a verified `dev` SHA: #509 (scopes), #513 (vault binding), #521 (engine
decoupling), each in one reusable lane worktree and its own branch/PR.
#508 now waits for #511/#512. #522 waits for #521/#514; #523 switches the whole
automation owner once. Preparing a host is not permission to run a second clock.
No separate foundation issue or generic shared-interface assignment exists.

## Acceptance and evidence

Follow each contract's atomic row and the runbook graph. No issue closes merely
because implementation exists. Scope tests + just check + applicable image gates;
#526 additionally verifies two users, concurrent private/group calls, fault/replay/
revoke and gateway outage in real containers; #527 verifies cutover/projection.
Status is in GitHub; state.yaml keeps dated evidence and limitations.
Old execution-plan/handoff/audits remain dated history, not the current read set.
