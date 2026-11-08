# CHG-0288: M32 integrated acceptance

## 2026-11-07 contract correction and finite regressions

The owner's current instruction supersedes the old background-memory denial and
the separate dreaming tick. Finish three product fixes before claiming #526:
system dreaming must materialize and execute through routine schedules and
occurrences; verified owner assignments may use currently selected and granted
memory tools; terminal_exec and terminal_job must have an isolated real executor
with bounded foreground and background process control.

Focused regressions: (1) two owners and timezone/DST produce one applied
dreaming result per local day through a durable routine occurrence, with restart,
disable and revoke before commit; (2) an admitted assignment run can search,
read and remember, while group/foreign/forged/finished runs and revoked writes
leave the vault unchanged; (3) terminal command/cwd/stdin/timeout and job
poll/send/kill/list work in the agent environment, with path, owner and current
authority fences. Use isolated temporary data for fault cases and the existing
two spaces for reversible real-model smoke. The final criterion table counts
only observed PASS rows; unavailable human Telegram input and unpublished CI
remain explicit non-PASS rows.

Current contracts: SPEC-0097, SPEC-0098, SPEC-0099.
This plan replaces the old stage-wide assignment. Prior audits are dated evidence.
Root AGENTS/README/docs index remain required. Acceptance remains open; dated
implementation and verification evidence is recorded in evidence-526.md.

## #526 — fault and isolation acceptance

Depends on #508–#525 integrated into one SHA. Test the actual managed lifecycle,
not hand-built profile fixtures alone. Use two synthetic users and real containers:
- six authored inputs, no effective legacy/self-settings override;
- tools.configure/use independent; selected memory tools inert when disabled;
- two concurrent private/group runs, exact-run denial and no cross-user access;
- durable capture under lost acknowledgment/restart and bounded per-user work;
- remember and confirmed exact edit, stale base/replay/revoke before commit;
- routine/assignment/watch CRUD, current revoke/pause fences, one dispatch after
  competing claims/restart, execution with gateway down and later delivery;
- per-user local-day dreaming, recovery/revoke before commit and digest drafts.

Record source/image identity, commands, effects and boundaries. External provider
delivery exactly once is not inferred from hub idempotency. Failed criteria remain
open under their implementation issues. No provider login/sends without authority.
The existing gateway and managed-supervisor canaries use ToolHub stubs and cannot
substitute for this matrix. `internal/devcheck/m32_fixture_integration.go` now
prepares two isolated six-authored-passport users and derives their projections.
`cmd/toolhub/m32_*_integration_test.go` now exercises real service startup and
recovery, and adds a CI container leg for concurrent managed private runs,
memory isolation/remember replay, runtime mounts and capture lost acknowledgement
across producer/ToolHub restarts. Bootstrap, startup/recovery and the real
reminder clock/outage/deferred-delivery path have run locally without Docker,
including pending-result pause/revoke fences after restart. Assignment source
reconcile, CAS, pause/resume and removal recovery also passed without Docker.
The container leg also exercises agent due work and deterministic file-source
watch/assignment agent wakes with isolated keep/drop filters, owner delivery
and restart deduplication; that leg remains unrun. Group/memory-revoke/crash,
assignment/watch execution and the remaining dreaming faults still must pass
before #526 can. Native dreaming acceptance now connects the production ToolHub,
automation, model relay and vault with controlled upstream/channel fixtures:
two timezones, spring DST, revoke before commit, independent owner progress,
host restart, draft-only intents and deferred delivery passed. Process-crash,
memory-disable/config-edit and autumn overlap still require acceptance.
The dreaming schedule now uses the same active-profile/published-revision fence
as ordinary tool calls. Missing/mismatched profile revisions or an incomplete
apply deny; a model-response barrier regression verifies refusal before write.
Memory search/card dispatch now waits for its owner's vault before taking the
registry fence, then repeats complete admission. This removes the reproduced
blocked-vault/registry-writer stall while preserving run and revocation checks.
The container matrix now includes a dedicated group home alongside a private
run, checks actual provider prompts for private-memory isolation and denies
group/borrowed/missing run claims. Two private runs also exercise issued
tools.use/memory.search/memory.remember revoke/regrant in already-open sessions.
These new container paths compile but remain unrun. The exact-source preflight
now rejects modified, staged and untracked source before checking image labels;
its clean/dirty-source regression and the six-passport group fixture test passed.
Confirmation notice/return calls now use service authority and preserve the
issued managed binding through the verified channel. Runtime notice forgery,
foreign Origin, metadata loss and primary runtime header impersonation have
regressions; the native gateway/ToolHub/storage principal fence passed. The
native MCP/channel/writer chain now passes owner/foreign/self/stale/replay/revoke,
gateway reconstruction and startup recovery after content-before-commit failure.
Telegram and lifecycle authority remain HTTP fixtures in that native run; the
same helper is wired into the actual container leg, still unrun. Abrupt process
termination and the remaining edit fault matrix remain required.
For local edits, reuse the existing dev spaces for non-destructive smoke and
`dev-rebuild` only the changed service. Do not create another Compose project
for routine diagnosis. Run disruptive fault/isolation cases in a dedicated CI
canary or one deliberately scheduled isolated acceptance run; never use
personal memory or credentials as fixtures.

## #527 — final cutover and projection

Depends on #526. Inventory and verify absence of obsolete config readers/writers,
shared run-scope, runtime/OpenKnowledge vault access and communication-owned clock.
Verify immutable catalog descriptions, prompt_contract, tools/routines docs, locale
parity and actual synthetic managed rebind/respawn. Fix omissions; this is not
permission to postpone per-issue tool contract bundles until the end.
The model-registry legacy layer reader has been removed; missing/unloadable
published revisions now deny without consulting root models.yaml. Model doctor
and onboarding guidance use passports. Replay config evidence now hashes the
complete published bundle, checks activation coherence and rejects symlinks;
focused regressions passed. The broader retired-path audit and integrated
acceptance still must establish full absence.
Run fast just check plus affected tests locally; require CI own Go coverage >=85%,
applicable image gates and security for
dependency changes. Report unavailable/live boundaries honestly.
Update current docs and CHG evidence; move completed CHGs to done with index rows.
No deploy, data migration, fallback, broad cleanup or automatic dev/main merge.

## Traceability

SPEC-0095 R1/R2/R3 and A1–A5 → SPEC-0097 C1–C5 + these two issues.
SPEC-0094 R1/R2 and A1/A2 → SPEC-0098 M1–M8 + #526.
SPEC-0094 R3/R4 and A3/A4 → SPEC-0099 A1–A5 + #526.
SPEC-0094 A5 → required per-issue gates and #526/#527.
Retention, citation and routine behavior invariants remain in the current contracts.
The granular split does not waive any original acceptance requirement.

Cold agent-tools dispatch now acquires a supervisor lifecycle lease outside the
registry fence and repeats authorization before business execution. Unit cases
cover revoke during startup, generation/owner mismatch, cancellation cleanup and
redirect denial. The container leg now starts with file probes before any model
job warms either runtime; its execution and full fault matrix remain required.

## Current live continuation handoff, 2026-11-07

C1 reader/writer audit, C3 live scopes/selection/revocation, and the A1 model-tool
origin plus real Telegram due delivery now have evidence. All scope/grant changes
were restored and their files/routine/message removed. Native legacy readers for
organization policy are a distinct host ceiling, not user config fallbacks.

The direct terminal probe exposed an I2 callable-catalog gap: terminal_exec and
terminal_job reach the explicit unavailable branch at internal/agenttools/call.go.
Do not claim a complete callable catalog from names/schemas alone. Next fix must
wire the declared bounded isolated executor and job contract using existing
execution infrastructure; do not enable native shell, special-case sleep, claim
foreground-only support as the full contract, or weaken authorization/isolation.
The controlled grant test was completed with sequential file tools instead.

Background assignment memory still requires an owner contract decision requested
asynchronously: SPEC-0098 private-interactive admission conflicts with the requested
successful background memory chain. Other remaining rows retain their original
scope; positive live HTTP needs an owned mutable public source because fetchWatch
uses PublicHTTPClient. No endpoint-policy relaxation or new full stack was made.

## 2026-11-07 correction and current acceptance boundary

The owner's current instruction supersedes the prior interactive-only memory
limit. Verified private owner assignments may use current memory rights. A real
two-owner assignment smoke exposed an additional dispatch fault: the automation
client dropped assignment/untrusted flags and used aggregate JSON execution,
which left the supervisor without a run ID during model tool calls. Preserve
these flags and use the existing run stream. The successful retry used both
owners' existing dev stacks and exact synthetic Telegram receipts. Terminal
direct MCP execution and job control passed; the model-selected command and
full dreaming/Telegram/group/fault gates remain separate acceptance rows.

