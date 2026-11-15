# CHG-0217 verification evidence

Status: implementation pending acceptance; no done move. Branch:
`codex/storage-reliability-mvp`, isolated managed worktree. Updated 2026-10-29.

## Regressions reproduced before fixes

- Collector `--tail` lost oldest unread events at 5001; equal timestamps lost
  the later ordinal. Tests now cover both boundaries and replay identity.
- Host truncation discarded prior records; an offset alone skipped replacement
  files of equal/larger size. Size/age rotation and generation checkpoints replace it.
- Unknown trace ownership reached discovery; substring matching admitted job-10
  for job-1. Authority resolution now precedes discovery and structured IDs are exact.
- Legacy diagnostic text exposed private JSON fields and search matched hidden
  content. Projection now precedes filtering; producers emit known metadata only.
- A single-file host bind retained the old inode after rotation. Compose now
  prepares a dedicated diagnostics directory and binds that directory only.
- HostLog accepted a directory alias and changed its permissions; diagnostic
  reading followed a directory alias. Both paths now reject these aliases.
- Free-form metadata, arbitrary level maps and unknown HTTP route segments leaked
  payload/dictionary keys. Typed coded fields and known route families now bound them.

## Implemented checks and behavior

Finite active + three rotated files, seven-day owned segment retention, bounded
line/batch/source scan, file sync before atomic cursor commit. A forced cursor
failure after data persistence replays the same source_position without skipping.
Unreadable stopped sources and corrupt/legacy cursors expose gaps rather than a
fully-drained claim. A writer lock serializes competing collectors.

Trace limits files, lines, events, bytes, sources and time; resolves selected
managed environment/home and rejects foreign principal/context/job. Duplicate
sources are read once. Production defaults to metadata, while explicit content
fixtures are marked in tests. Toolhub diagnostics reads bounded rotations and
returns safe gap/partial evidence. No credential files or environments are read.

## Verification levels

- Targeted Windows race tests passed for diagnostics, Toolhub, stack, hubctl,
  supervisor and agenttools after changes. Fault injection uses real temporary
  files; fake docker executable tests are process-contract tests only.
- Final native Linux/WSL `go test -race ./internal/diagnostics -count=1` PASS,
  2.054s. Includes filesystem alias and streaming tests; this is not a Docker
  volume/power-loss proof.
- Integration-tagged diagnostics suite compiled successfully. Added real-daemon
  backlog5001 scenario is NOT RUN. No image pull/build/run or deployment after
  the user's Docker restriction.
- Earlier root check's Go/coverage leg passed at 85.92%, but staticcheck found
  S1011 in trace discovery. The loop was simplified, then the full check rerun;
  see state.yaml for the final result. No failing gate is relabeled by a targeted pass.
- Diagnostic logs are ignored under `.work/tmp/` (git check-ignore verified).

## Pending acceptance

Real Docker source/rotation behavior, mounted directory permissions and volume
failure behavior remain unverified. Catalog `5.8.0-storage.1` is prepared in code
and documentation; no rebind/respawn has occurred. Existing running services keep
their old mounts/contracts until a separately authorized rollout. Current JSON
and spool architecture remains; no database, queue or workflow dependency added.

CHG-0218 cannot PASS its required native/container proof while Docker is deferred.
Dependent SQLCipher stages remain unstarted. Continue independent CHG-0221 next;
whole Storage reliability MVP remains unfinished.

## Root rerun receipt before S197.1 freeze

Full root check failed again: existing mediasvc Windows sharing violation and
runtime TestModelRelayRecoversOneTransportTimeoutAfterCooldown. It also caught
an own transient compilation failure in the Linux bridge subprocess while
S197.1 imports were being edited. Those unused imports were fixed immediately;
the run is invalid as final-code proof and is not claimed green. A fresh frozen
check is required after the S197.1 slice. No timeout or gate was relaxed.

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
