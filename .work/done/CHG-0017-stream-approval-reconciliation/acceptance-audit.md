# Acceptance audit — issues 16–18

Checked against the GitHub issue bodies on 2026-09-12. Original acceptance
criteria for issues16,17 and18 are implemented and verified within the documented
transport boundary. Root final review and task-state closure are complete. Unit fixtures prove only the exercised Go boundary. The real pinned
Hermes probes below use an isolated local model provider, not an external account.

| Requirement | Current evidence | Completion gap |
|---|---|---|
| [16](https://github.com/letya999/hermes-hub/issues/16): one final delivery | Real supervisor/Hermes tool stream produces exactly one final outbox delivery; spool restart and cached terminal recovery produce no duplicate. Receipt repair and cached generation rebind regression tests pass | Complete internally; uncertain Telegram acknowledgement remains inspection-only, no live Telegram send tested |
| 16: reconnect across generation | Real active stream disconnect, verified replacement and original-run recovery to interrupted, without resubmitting input; exactly one durable error delivery | Complete |
| 16: stream holds runtime | Real acquired stream remains busy past reap TTL; disconnect preserves unresolved hold, recovered terminal releases it; NDJSON >2s regression includes concurrent reap | Complete |
| 16: private information stays private | Real terminal tool returns secret canary to Hermes, absent from durable deliveries; allowlisted decoder and approval/private-field projection regressions pass | Complete; active native approval round trip belongs to issue17 |
| 16: bounded flow | SSE/NDJSON frame and connection bounds, synchronous consumption, stalled queue cancellation, poll/progress cap and input/output bounds regressions; Telegram 4096/4097 Unicode and 2MiB ceiling tested with local HTTP fixture | Complete; external Telegram API not exercised |
| 16: obsolete generation rejected | Real replacement recovery rejects old-generation final; supervisor registered-generation guard, full stream identity and immutable terminal result regressions pass | Complete |
| [17](https://github.com/letya999/hermes-hub/issues/17): cancellation throughout lifecycle | Queued restart, cold startup admission race, warm-ready pre-admission cancellation, correct native run/STOP and uncertain STOP observation-before-retry regressions; real native approval expiry confirms STOP terminal | Complete |
| 17: original authority only | Approve/cancel denial matrix for fifteen binding fields; private command ownership/schema/external identity checks; current host policy, selected organization and membership regressions; real original-audience dispatcher approval and obsolete-generation rejection | Complete |
| 17: approval hold and safe expiry | Dedicated approval lease/atomic persistence tests; real pending native approval survives reap TTL, explicit deadline-clock sweep cancels idempotently and terminal releases all holds; lost STOP response survives supervisor restart; real active-approval replacement observes the original run as interrupted without input replay | Complete |
| 17: late approval denied | Runtime deadline/request/choice checks, immutable saved deadline/restart, expired pending duplicate denial; real expired native request and replaced native approval reject positive replies | Complete |
| 17: approval does not grant provider rights | New mutations require current effective host policy/member checks; native once approval leaves host settings byte-identical; only exact native request choice is dispatched, no provider/org permission mutation; existing tool policy enforcement is unchanged | Complete within existing tool/provider authorization boundary; no external provider action tested |
| [18](https://github.com/letya999/hermes-hub/issues/18): missing worker recovered once | Concurrent keyed recovery, original-run observation and backoff regressions; lifecycle-only restart recovery retains original opaque lease; Docker42032 verifies four concurrent recoverers produce exactly one replacement and recover the original interrupted run | Complete; Docker42032 exit0 |
| 18: idle registration does not wake | Authoritative missing-idle handoff/write-failure regressions; stopped registration never acquires compute; Docker42032 confirms missing idle does not wake and hands capacity off durably | Complete; Docker42032 exit0 |
| 18: new work during reap | Desired-state recheck plus keyed start/stop; Docker42032 real HTTP job versus reap race permits at most one replacement; a native crashed-turn lease wait retains desired hold and resumes the same admitted run to final without input replay | Complete; Docker42032 exit0 |
| 18: owned stale/orphan detection | Name/context/generation owner-filtered inventory bound256; immutable cleanup only known-terminal old ledger; unknown/uncertain and foreign generations preserved; restored Starting adoption and lost Docker ACK regressions; actual /scope bind source must match selected context; Docker42032 detects a copied-generation own clone and preserves both unknown and foreign fixtures | Complete; Docker42032 exit0 |
| 18: bounded observable failures | Once-per-generation 2/4/8-second retry and three-failure ten-minute budget persist; restored entries reserve capacity; failed rm/lost ACK preserve unknown slots; observed orphan inventory blocks new cold starts | Complete; Docker42032 confirms crash count1 and exactly one replacement |
| 18: persisted user state preserved | Lifecycle code never removes volumes/context paths; real recovery and routine probe checks synthetic home/session/credential/schedule canaries and durable job/final ledgers | Complete; Docker42032 exit0 |
| 18: all desired-state sources | Queued/running jobs, pending approvals, lifecycle holds, persisted pins and durable due-occurrence handoff; unit authorization/overlap/idempotency/restart regressions pass | Complete; Docker42032 pin and due wake/final/handoff/sleep; full routine CRUD/DST is separate issue33 |
| 18: health layers | Separate process, authenticated Hermes readiness and aggregate native platform health; external lazy connectors explicitly not_probed; real probe compares against actual native detailed readiness, including degraded | Complete; Docker42032 exit0 |
| Delivery gates | Final Docker42032 exit0, current production image32d074110c29: standalone smoke and real pinned Hermes0.21.0 contract, production HTTP runner/spool/control approval and expiry, automatic replacement, same-run native lease recovery, pin/routine/reap and preservation canaries | Passed on final current source |
| Repository gate | Final session56498 exit0: full race/shuffle tests, own Go coverage85.18%, format/vet/staticcheck/docs/actionlint | Passed on final current source |

Issue18 implementation and targeted race checks are complete; repository56498
passed own Go85.18% and all checks on final mount-fenced production. Native41003
passed every added real lifecycle probe, including native300s crashed-turn expiry
and original-run resume across normal request timeouts. Mandatory Docker42032
also passed exit0 with all standalone and real native stages on current source.
The local model fixture selects the latest explicit task across native continuation
notes and emits tools only for streaming execution; non-streaming housekeeping
receives ordinary text. Readiness is compared with actual native detailed health,
including degraded. No live provider account or Telegram sending was exercised.

Earlier short-settlement probes82716/61776/39137 failed because a reused Docker
PID kept a native session-turn lease conservatively live until its 300s expiry.
The stack established that policy; the corrected real probe keeps the existing
run and hold, resumes without input replay and confirms final settlement.

There are no outstanding implementation or acceptance gaps for issues16–18.
Root review is the final handoff; full routine CRUD/DST migration is separate
issue33. Live provider accounts and external Telegram sending were not exercised;
uncertain external acknowledgements remain inspection-only without blind replay.
No dependency was added, so the dependency-change security gate was not required.
All delegated test/build handles are complete; user contexts were not switched,
and no login, external sending, commit, push or publication was performed.

Owner explicitly requested GitHub closure: #16, #17 and #18 closed on 2026-09-12.
Root independent just check68821 passed (own Go85.20%) and just docker-check78075
passed all original native lifecycle probes on image32d074110c29. New issue19
implementation and its full real five-minute gateway proof are tracked separately.
