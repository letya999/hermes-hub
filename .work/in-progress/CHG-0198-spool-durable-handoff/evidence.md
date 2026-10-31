# CHG-0198 implementation evidence

## S198.1 terminal receipt — in progress

Real temporary filesystem fault injection; no provider send or live integration.

- Red: `.work/tmp/terminal-handoff-red.log`: outcome persistence failure previously still marked the source job completed; no worker terminal receipt API.
- Red: `.work/tmp/terminal-handoff-boundary-red.log`: changed delivery intent/stale generation accepted and streamed voice-only payload lost.
- Green: `.work/tmp/terminal-handoff-worker-race-fixed.log`, `go test -race ./internal/communication -run ... -count=1 -timeout=180s`: exit 0, 6.562s. Covers source receipt, mapping, outbox and source-job rename faults; restart recovery without runner resubmission; single delivery; scope/generation fences; voice-only output and worker/goal paths.
- New worker preflight refuses missing/corrupt/mismatched admission before runner execution. Resume retains the original prompt digest before clearing its text. Positive fixtures now carry real admission identity and matching Telegram audience.
- Full communication race suite running. Current full root gate not yet run after S198.1 edits; pre-0198 root gate passed at 85.96%.

Remaining: finish terminal path review; S198.2 bounded source batch intent and S198.3 retry classification; current docs verification, full gates and actual acceptance. CHG-0192/0197 dependency acceptance pending. Docker is deferred by explicit user instruction. No done claim.

## S198.2 bounded source batch — in progress

- Red `.work/tmp/source-batch-red.log`: after eight of ten sink admissions, restart followed by a different source response advanced cursor to 20 with only eight seen IDs; a paused batch still fetched its source.
- `.work/tmp/source-batch-state-race.log`: Communication and routines targeted race checks passed (5.150s / 1.357s), including existing event, stream cursor/source and hook/acquisition protection.
- Exact prepared sink payloads, source/schedule/probe binding, cursor interval and decisions now live in one bounded pending batch inside existing EventState. No new queue/database/framework.
- Sink admission/drop decisions are persisted before cursor advance. Per-batch settled decisions are independent from seen-window eviction. Pending batches are outside terminal compaction.
- A pre-acquisition routines hook drains pending occurrence batches; mandatory settlement/replay errors propagate through the existing bounded proactive pass.
- Extended temporary-filesystem checks added for agent-group remainder, revision rejection, 600 drop decisions and poll occurrence restart. One new agent fixture initially fit one bundle after the existing 1024-rune input projection; expanded to 18 events to actually exercise three bundles.
- Current root `just check` running in `.work/tmp/chg0198-handoff-root-check.log`; started before that last fixture correction and doc updates. Its result must be recorded honestly; current snapshot needs final revalidation.

Remaining code review includes assignment admission/checkpoint crash windows and scope/epoch fences; S198.3 is not implemented. Docker/native acceptance deferred, no provider sends.

## S198.3 retry classification — in progress

- Red `.work/tmp/retry-effect-classification-red.log`: failed message, failed goal and cold-wake observation all admitted a second run despite no proof of an effect-free first attempt.
- Red `.work/tmp/post-dispatch-http-red.log`: bare post-dispatch 503 retried five times; generic 500/invalid 200 were reported as deterministic failures.
- Green `.work/tmp/retry-classification-http-green.log`: targeted race checks passed for Communication/runtime/supervisor (4.352s / 1.212s / 2.254s). A 503 is retryable only with the trusted supervisor pre-dispatch marker; job/idempotency identity stays fixed. Unbound/invalid replies stay uncertain. Existing refusal-before-send and lost-response tests remain covered.
- Removed `-r1` worker jobs and `:wake` runtime readmission; known failed goals pause. Toolhub OAuth retry boundaries from CHG-0197 still require verified pre-effect auth rejection; unknown call completion is not replayed.
- `.work/tmp/source-batch-extended-fixed-race.log` passed Communication/routines (7.088s / 1.792s); `.work/tmp/source-batch-repair-race.log` passed (3.122s), including exact pending-admission repair and rejection of a changed context.
- First root gate `.work/tmp/chg0198-handoff-root-check.log` exited 1: a test was renamed while Go generated `_testmain.go`, causing an undefined old test symbol. Communication itself passed that snapshot (139.112s), but no full-gate PASS is claimed. Repeat the full gate with sources unchanged throughout.

Actual native/container/provider acceptance remains pending. No Docker activity, deployments or real-data migration.

## Stable Windows root snapshot

`.work/tmp/chg0198-stable-root-check.log`: `just check` exited 0 with own Go statement coverage **85.92%**. Communication 150.397s, runtime 33.574s, supervisor 42.750s, Toolhub 118.486s; docs/lint/config and security lint within the root gate passed. No dependencies changed. This is a fixture/Windows gate, not native/container/provider acceptance.

Review follow-ups before claiming S198.1/S198.2 implementation complete: idempotent assignment wake reservation across enqueue failure; explicit terminal generation comparison in the pre-mapping receipt crash window; status-receipt recovery through the same editable-status lifecycle; required source-batch structural fields and replay health visibility. Subsequent fixes invalidate this snapshot for current-code delivery and require a new gate.

## Recovery review corrections

- Red `.work/tmp/handoff-review-windows-red.log`: enqueue failure spent the only assignment wake and stranded its pending batch; a pre-mapping canonical receipt accepted a different runtime generation.
- Green `.work/tmp/handoff-review-recovery-race.log`: Communication/routines targeted race passed (7.551s / 2.632s). Job-keyed assignment reservation is part of the existing checkpoint atomic write, reused across owner-day changes, fenced by pause/generation, and released after durable job mapping. The unresolved receipt count is finite (1024), with admission stop at capacity.
- Recovery reuses editable status upsert and preserves the failed-edit fence. A source-written status receipt whose outbox write failed restores the latest edit after restart.
- Source-batch structural requirements reject missing decisions/source interval. Per-batch decisions protect cursor progress across seen-window eviction.
- Mandatory sink failure is stored as a safe LastError and returned by the existing proactive pass; overflow requeue retains its identifier-only contract. Intent/recovery never reports that failed handoff as successfully settled.
- Subsequent root gate must cover these corrections; the prior 85.92% snapshot is preserved as historical evidence only.

## Elapsed-status compatibility correction

The reviewed root gate `.work/tmp/chg0198-reviewed-root-check.log` exited 1: `TestLiveProgressTickRearmsAndAges` failed. The new stale-position guard suppressed elapsed text, whose caller had not supplied event position. This is an implementation regression, not a baseline/environment failure.

The caller now supplies the current event position. Upsert distinguishes a same-position new text from an exact replay; edit identities bind text, and lifecycle checks inspect the actual coalesced intent so a failed/done edit is not replayed under a new ID. Existing status/unknown/edit checks plus two successive elapsed updates passed under race in `.work/tmp/status-age-recovery-fixed-race.log` (6.551s).

`.work/tmp/handoff-final-boundaries-race.log` also passed (2.732s): missing batch decisions/source interval cannot trigger another fetch, a new effect or cursor progress. Full current root verification is running in `.work/tmp/chg0198-final-root-check.log`; the earlier 85.92% result is historical only until this completes.

## Current verified Windows snapshot

`.work/tmp/chg0198-final-root-check.log`: `just check` completed with exit 0; own Go statement coverage **85.93%**. This includes the elapsed-status correction, durable batch reservations and structural boundary checks. All root packages, docs, lint, config contract and security lint within `check` passed; dependencies are unchanged. Toolhub 104.623s, API 33.414s, runtime 32.042s, supervisor 34.934s. The earlier root failures remain documented above rather than being relabelled as baseline.

S198.1-S198.3 remain in progress. Before claiming implementation complete, verify an uncertain receipt across trusted runtime-generation rebind and review the non-event watcher/check admission caller still using the legacy wake counter. Native Linux filesystem/container handoff and actual dependency acceptance remain outstanding; Docker is deferred by the user. No provider send, deployment, publication or working data migration occurred.

## Trusted rebind and non-event watcher handoff

- Red `.work/tmp/uncertain-rebind-red.log`: trusted generation replacement could not save the next uncertain observation (`terminal receipt cannot change`). Canonical uncertain identity now includes run/session/generation; stale generations are not reapplied during recovery. Immutable source scope and original run/session remain fenced.
- Green `.work/tmp/uncertain-rebind-green.log`: targeted Communication race passed, 24.276s, including original recovery/terminal/uncertain guards.
- Red `.work/tmp/watcher-handoff-red.log`: non-event watcher enqueue failure was hidden, no saved intent existed and the only assignment wake was consumed with no admitted job.
- Prepared watcher job now lives in the existing bounded `probe-result` record before wake reservation/enqueue. Replay uses exact intent without refetching source. Storage reservation failures propagate as errors; quota decisions remain distinct. Job-keyed wake reservations apply here too.
- Common enqueue repairs an exact pending job whose mapping write failed, preserving original metadata; an unmapped running/done/failed job or changed identity is refused. This reuses the same admission verifier as event batches.
- Red `.work/tmp/watcher-source-binding-red.log`: a source-a receipt could enqueue a job from source-b. Replay now strictly checks canonical occurrence/source identity, owner, watch ID and audience before applying it.
- `.work/tmp/watcher-boundary-race.log`: Communication/routines race checks passed (6.334s / 2.524s). Current final targeted checks are `.work/tmp/watcher-final-race.log`; full gate must be repeated after these code changes. These are isolated filesystem/protocol fixtures, not container or provider integration.

The first watcher/rebind root run `.work/tmp/chg0198-watchers-root-check.log` exited 1 in two Communication fixtures: a manually built receipt retained the pre-`PutOccurrence` job identity, and the broken-outbox case expected an ignored mandatory save error. The positive receipt fixture now loads the canonical source record and exact audience; broken outbox explicitly requires an error before recovery. Other packages, including routines, runtime, supervisor and Toolhub, passed that snapshot. No full-gate green is inferred from those partial passes. Current targeted fixture checks: `.work/tmp/watcher-current-fixtures-race.log`.

## Verified watcher/rebind implementation snapshot

`.work/tmp/chg0198-watchers-fixed-root-check.log`: `just check` exited 0, own Go statement coverage **85.91%**. Communication/routines/runtime/supervisor/Toolhub and all root static/docs/config checks passed on the stable current sources. `.work/tmp/watcher-current-fixtures-race.log` also passed Communication/routines (2.522s / 1.110s).

S198.1-S198.3 are implemented pending actual acceptance. The CHG remains in-progress: dependencies' acceptance, native filesystem/container handoff and actual selected rollout are not proved by Windows fixtures. SQLCipher is still prohibited without CHG-0194 PASS. No Docker activity, live send, login, publication or working data migration was performed.
