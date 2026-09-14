# Issues 16–18: stream, approval and reconciliation

Implement the full acceptance contracts for GitHub issues 16, 17 and 18 using
the existing Go runtime, supervisor and file spool. Preserve upstream Hermes.

1. Verify pinned SSE reconnect, terminal recovery, stop and active approval contracts.
2. Persist admission and bounded normalized events; recover event/outbox handoff.
3. Route durable cancellation and approval with original identity and generation checks.
4. Reconcile current work, leases, operator pins and routine wake against container state.
5. Bound crash recovery, identify owned orphan generations and preserve uncertain results.
6. Add negative, concurrency, restart and real pinned-Hermes/Docker checks.
7. Update specifications and operations; run just check and Docker gates.

Completion requires evidence for every acceptance criterion, not mock-only success.

## Sequential issue17 completion

Issue16 is verified; issue18 remains deferred. Audit existing cancellation in
queued/start/ready/run states and uncertain native STOP, full original authority
and current policy negatives, approval holds and expiry after lost response or
replacement. Enable the upstream native ask mode for Hub-routed responses, prove
positive approval and expiry against pinned Hermes, preserve issue16 invariants,
then run current repository and Docker gates and refresh the acceptance matrix.

Completed issue17: repository69592 exit0 (own Go85.15%) and Docker46212 exit0,
image7504d0da928b. Original five criteria are recorded in acceptance-audit.md.
Proceed to issue18 only as a separate sequential delegation.

## Sequential issue18 completion

Audit all original lifecycle criteria against existing desired-work, pin, routine,
health and ownership code. Close authoritative missing-idle capacity handoff,
lost Docker-start acknowledgements, restored in-flight generation adoption and
lifecycle-only recovery. Preserve opaque lifecycle holds across infrastructure
replacement; unknown ownership and observed orphans block unsafe new capacity.
Prove automatic concurrent recovery, missing-idle no-wake, pin and due-routine
wake/final/handoff/sleep against the pinned image with synthetic persistent-data
canaries. Run current repository and Docker gates, then refresh the full audit.


## Remaining completion plan (2026-09-12)

Execute in order; no issue closes until its full acceptance evidence is recorded.

1. #17 authority: persist exact identity schema/external account; reject mismatched and legacy unverified control bindings; verify current policy/membership and provider restrictions.
2. #17 lifecycle: cancellation during queued/starting/admission races; reconcile uncertain nonterminal control outcomes without repeating unknown approval mutations; verify expiry after lost responses/replacement.
3. #16 delivery: complete durable terminal handoff and uncertain transport outcome handling; enforce bounded chat delivery and slow receiver behavior.
4. #18 desired sources: due routine occurrence to durable job/wake and explicit operator pins; connector health; ownership-safe stale cleanup and restored global capacity accounting.
5. Real pinned Hermes/Docker probes: positive approval, expiry, stream disconnect/restart/generation fence, idle/missing recovery and preservation canaries. Mock regressions cannot replace these probes.
6. Finalize current docs/specs and refresh acceptance-audit.md from actual evidence; run just check (>=85% own coverage) and latest just docker-check; resolve failures and audit every original criterion.

## Issue18 completion evidence

All original issue18 criteria are complete. Final `just check`56498 passed exit0
with own Go85.18% and all race/static/documentation gates. Final `just docker-check`
42032 passed exit0 on production image32d074110c29 and pinned Hermes0.21.0,
including real automatic once recovery, idle no-wake, new-job/reap race, native
300s crashed-turn expiry and same-run resume, orphan isolation, operator pin,
due wake/final/handoff/sleep and persistent canaries. No upstream patch, native
lock/data deletion or prompt replay was used. No delegated gate remains live.
Only root final review and global task-state closure remain.
