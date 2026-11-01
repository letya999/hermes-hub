# CHG-0215 — implementation evidence (2026-10-28)

## Isolation and baseline

- Source: `fix/m23-live-wake`, HEAD `0a8cbf7`. New branch:
  `codex/storage-reliability-mvp`, managed worktree
  `C:/Users/User/.codex/worktrees/storage-reliability/hermes-hub`.
- The source checkout was not reset, stashed, switched, staged or committed.
  Only the Storage design package and its three index edits were copied.
  CHG-0214 is part of the source history; its code was not changed here.
- Baseline `just check`: FAILED in unchanged
  `internal/mediasvc/TestComfyVideoResumesPersistedHandleWithoutSubmission`,
  Windows sharing violation on its temporary `job.json`. Coverage not accepted.

## B-A1.1 / S191.1

Synthetic grant seq1 + revoke seq2; remove latest anchor, then reopen.
Before the fix both cases (latest-only and all anchors missing) FAILED:
startup succeeded and both source ledgers changed, losing the revoke.
After the fix missing current head refuses; the test compares P/R bytes.
No provider or personal data participates.

## B-A1.2 / S191.2 / S191.3

- Explicit sealed genesis, purpose/store binding, sequence and digest validation.
- Recovery -> primary -> current-head acknowledgement ordering. Current-head
  replacement persists the sealed temporary file and containing directory.
- Both committed prefixes are checked before tail truncation; compaction on
  anchored open is disabled before head validation.
- Missing/corrupt head, wrong format/store/hash, unknown marker and wrong keys
  refuse. Old per-commit files are not recovery fallback.
- Existing interrupted-first-write, uncommitted tails, stale backup,
  same-sequence substitution and primary-loss tests retained.
- Head-write permission failure poisons the journal and never acknowledges
  the tail; reopening preserves the prior revoke (unprivileged Linux fixture).
- Offline migrate-head requires equal P/R/legacy tips and writer locks;
  staged-head resume, idempotent activation, conflicting stage, malformed
  legacy records and byte preservation are covered. Providers are not opened.
  Old initialization code rejects the new sealed marker value.
- Required CLI/app wiring and synthetic tests added. Existing migrate-recovery
  seeds a new v2 head and can resume only its verified staged head.

## Verification levels

- Before the Docker restriction: one disposable Go test container reproduced
  the regression; subsequent `go test -race ./internal/journal -count=1` passed
  (8.126s). Source module only; no deployment/working volumes/providers mounted.
- After the direct user restriction: no new Docker calls, builds or deployments.
  Native Go 1.27.3 was obtained through Go's checksum-verified toolchain module;
  Broker pipeline runs in Ubuntu/WSL without Docker.
- Full Broker `just check` PASS in native WSL: race suite, docs/OpenAPI, vet,
  secret hygiene, CLI builds/version. Coverage 89.54% total; journal 85.74%;
  every production package >=85%. Earlier cross-execution coverage attempts
  were discarded because embedded Windows metadata paths were unsuitable.
- Final root gate: see state.yaml for its final receipt.
  Local diagnostic logs are ignored under `.work/tmp/`; module pipeline outputs
  remain in its existing ignored `.build/quality/` destination.

## Limits

No production migration, account action, provider GC, rollout or SQLCipher.
No power-loss/volume-loss/container proof. Simultaneous rollback of every trusted
freshness copy remains outside the stated guarantee. Full MVP is unfinished;
CHG-0217 and subsequent independent implementation remain outstanding.

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
