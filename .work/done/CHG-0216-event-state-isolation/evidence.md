# CHG-0216 — implementation evidence (2026-10-28)

## H-A1.1 / retention

The synthetic paused-source/unknown-schema test first FAILED: Compact removed
both old cursor JSON and unknown JSON under age/count pressure. The predicate
now admits only recognized run receipts with terminal authority mappings,
closed approvals and no pending delivery. Cursor/provider data has separate
owners and directories. A count fix excludes already archived records from the
remaining-count eviction calculation.

## H-A1.2 / layout

- A source with a durable cursor first FAILED typed-layout assertions; a
  conflicting alpha/beta destination was also silently accepted before the fix.
- v2 layout: event-state, run-events, provider-dedup. Strict recognized schema,
  canonical filename/identity, bounded reads and digests precede all moves.
- A durable manifest under the existing exclusive writer fence resumes after
  plan, duplicate copy, rename and old-writer fence stages. Foreign/conflicting,
  unknown, oversized and unsafe records fail closed instead of being removed.
- Typed-directory loss on an initialized store refuses recreation. Cursor
  initialization evidence makes missing state a visible degraded recovery gap.
- The completed events format file makes the old binary's MkdirAll fail.
  Canary and supervisor contract readers were updated with the receipt owner.
  Normal receipt recovery no longer skips foreign schema in run-events.

## H-A1.3 / archive

- Injected file-sync failure initially FAILED: the old code removed source.
- Repeated archive of the same source initially FAILED with two logical records.
- Immutable gzip chunks have a stable payload digest independent of retry day.
  Write -> gzip close -> file sync -> gzip verification -> rename -> directory
  sync precedes source removal and source-directory sync. Existing chunks are
  verified and synced before reuse. Corrupt chunks refuse; temporary chunks
  never count as committed archives. Archive retention policy remains existing.
- The sync fault is injected only at the filesystem boundary; compression,
  source handling and recovery paths stay real.

## Verification

- Targeted Windows regression/migration/compaction/canary tests passed.
- `go test -race ./internal/communication ./internal/routines` passed:
  119.395s / 2.865s. Final root check and Linux filesystem receipt are in state.
- Windows directory sync is the existing no-op, not proof of Linux durability.
  Native Linux/WSL targeted race tests also passed (2.040s) after the final
  archive path confinement and migration destination-sync changes.
- Docker use/volume tests are deferred by the user's direct instruction.
  No deployment, working-data migration, live delivery or provider operation.

## Outstanding

Final root gate and actual acceptance remain required. Targeted native tests
prove Linux temporary-filesystem behavior, not Docker-volume/power-loss behavior. The
archive now uses individually committed chunks; complete footprint and file
budgets remain CHG-0223, not a claim of completed bounded Storage MVP.

## Final root gate receipt

Baseline failed at unchanged mediasvc Windows file sharing. A later full root
test/coverage leg passed at 86.03%, while lint exposed three archive-path G703
findings. Those paths now use os.Root confinement with traversal/type rejection;
the G703 findings are fixed without suppression. Targeted archive/migration tests
passed again. The final `just check` passed lint/gosec/docs/config but FAILED in
unchanged internal/runtime TestConcurrentJobsWaitForOneBootstrap (context deadline
exceeded). The full gate and final coverage acceptance therefore remain failed,
not green. No timeout was relaxed. No CHG was moved to done.

Isolated repeat `go test -race ./internal/runtime -run
'^TestConcurrentJobsWaitForOneBootstrap$' -count=1` passed (2.269s).
This does not turn the failed full just check green.

## Frozen S197.1 root check receipt (2026-10-29)

`just check` was rerun with process-only GOFLAGS=-p=2. Docs and configuration
legs passed. Every package eventually printed PASS, including mediasvc and
runtime; nevertheless the supervising go test process did not complete or emit
its aggregate coverage footer. It was stopped by its verified process identity
only; the gate is FAILED/interrupted, not accepted and no final coverage number
is claimed. Lint separately found three G115 int-to-uint64 counter conversions.
The bounded record counter now uses uint64 throughout, without suppressions;
Windows audit race tests passed again (4.911s). See state for the standalone lint
receipt. No unrelated process, Docker service, provider or source worktree was
stopped or modified.

Standalone `just lint` PASS after unsigned-counter fix: formatting, vet,
module tidy diff, staticcheck and root/Broker gosec. Final native Linux audit
race repeat PASS (2.526s); final Windows audit race repeat PASS (4.911s).
Documentation check and git diff --check PASS after evidence/state updates.
This does not replace the failed/interrupted full just check or establish live
acceptance. No new dependency manifests changed.

## S197.2 core/exposure final receipts (2026-10-29)

- Final Windows full race suites PASS: audit 12.611s/89.1%, exposure
  5.501s/80.4%, secrets 5.698s/86.0%, SSH 7.375s/86.7% own coverage.
  SSH passing here is a regression check, NOT a claim that SSH uses the new API.
- Final native Linux/WSL audit and exposure full race suites PASS:
  2.905s / 1.292s, including the strengthened high-water reservation test.
- The high-water test was reviewed and strengthened: completion now carries
  a valid 12 KiB field so it cannot fit merely by chance into filler remainder.
  A command-local Go overlay disabling completionReserve made this exact test
  FAIL (reserved completion lost). Normal code PASS. Overlay/mutant live only
  under verified ignored .work/tmp; no runtime source or global settings changed.
- Targeted agenttools race suites PASS (9.036s), including uncertainty projection.
- Standalone Windows just lint PASS: format, vet, tidy diff, staticcheck, and
  root/Broker gosec. No dependency manifests changed. Docs/format were checked
  again after evidence/state updates.
- Native root just check FAILED at its Go leg. Unmodified test cases failed:
  runtime TestBrowserStartsAfterReady and TestDeferredBrowserStopsSpawningAfterShutdown
  attempted cleanup of read-only /tmp/.X11-unix/X99; stack
  TestOrganizationOverlayAndMembership did not inherit MCP; Toolhub
  TestStopFailureSurfacesOnEveryCaller/Reload did not surface its expected error.
  These are untriaged failures, not a claim of independently proven baseline bugs.
  No full aggregate coverage or successful full gate is claimed. Native Unix
  just aborts subsequent lint/docs/config legs after this Go failure.
- A separate WSL process-state observation failed with 0x8007274c while the
  original check handle stayed live. The same handle was polled until actual
  exit 1; no duplicate/restarted native full gate, Docker/WSL restart, socket
  deletion workaround, or permissions relaxation was performed.
- Existing CRLF style was retained in four edited tracked files to reduce diff
  noise. devcheck Format explicitly normalizes CRLF. Whitespace check uses
  command-local core.whitespace=blank-at-eol,blank-at-eof,space-before-tab,cr-at-eol;
  no repository/global Git configuration was changed.

Full MVP remains active and incomplete. No CHG was moved to done. No real
provider, working-data migration, Docker deployment or catalog rollout occurred.

## Latest root gate — SSH producer snapshot (2026-10-29)

Full Windows `just check` PASS, exit 0. Go test/race/coverage, lint (format, vet,
tidy diff, staticcheck, root/Broker gosec), docs/actionlint and configuration legs
all completed. Root statement coverage **85.95%**, package floor accepted. This
is a new successful run; earlier Windows hangs/flakes and native full-root failures
remain historical evidence, not silently relabeled. It does not establish actual
Docker/provider/power-loss acceptance or supersede the blocked SQLCipher gate.

Final native Linux SSH full race PASS (2.383s). Full Windows SSH and agenttools
race/coverage checks passed (87.7% / 86.2%); canonicalization and producer/contract
regressions passed again afterward. Only a certificate-receipt comment and task
state/evidence were updated after the successful full root run; runtime code was
frozen throughout. Final docs/format/whitespace checks were repeated.

Prepared hub-agent-tools@5.8.0-storage.1 includes the SSH uncertainty description,
prompt contract and docs in this same unpublished changeset. No rebind/respawn,
Docker deployment, external host login, provider mutation or data migration.
Toolhub wiring, lifecycle reservations and verified resolution remain required;
no CHG was moved to done and the whole Storage reliability MVP is unfinished.
