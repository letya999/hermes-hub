# CHG-0170: Hosting slots, immutable releases and quota (M18 #147, H1)

Chain: ADR-0081 D1/D5/D6/D9 → SPEC-0079 R1–R4/R8–R10 → this plan → state.yaml.
Evidence: [design research](../../done/CHG-0169-site-hosting-design/research.md),
[Haloy review](../../done/CHG-0169-site-hosting-design/haloy-review.md).
Previous: CHG-0169. Next: CHG-0171. Branch: feat/m18-exposure-hosting.

## Goal

An owner-bound, crash-recoverable store can stage a complete release, reserve
old+new storage, and commit a revision without exposing partial bytes. This
stage is an internal prerequisite, not a published hosting feature.

## Read before coding

README.md, docs/index.md, docs/architecture.md, docs/testing.md; ADR-0079–0079,
SPEC-0077–0077; design research and haloy-review.md; following stage plans.
Use Go/stdlib and existing patterns; no Haloy dependency, DB, queue or Docker.

## Preconditions and implementation boundary

Bind one store to trusted principal/environment/zone before request decoding.
One exclusive process lock for its lifetime; CLI must not open a second writer.
Use root-confined descriptor-relative filesystem operations, not a lexical
prefix check. Review os.Root behavior for hardlinks and rename races; a source
regular file is copied, never linked. Do not claim os.Root alone rejects links.
Publisher sees only selected workspace and dedicated hosting store. R4 metadata
and retired bytes remain charged until verified cleanup. No raw filesystem
error/path is returned to callers.

## Slices (ordered; one commit each, red before implementation)

### H1.1 Binding, policy and persistent single-writer state

Existing anchors: `internal/identity/identity.go:Envelope/ValidID`,
`internal/exposure/config.go:Config/Load`, `internal/audit/audit.go:Event/Append`.
New files: `internal/hosting/config.go`, `types.go`, `store.go`, `lock_*.go`.
Introduce strict manifest/policy, finite site slots, expected revision,
operation input digest and replay receipt, hashed access metadata only.
Use platform file locking supported by existing dependencies, with no fallback
that leaves stale lockfiles blocking recovery or permits two writers. Journal
checksummed/bounded versioned state, atomic rename plus file/dir durability
where supported; unsupported durability fails explicitly. Deny malformed state.
Reserve bounded audit admission before mutation; generic audit.Append alone
does not provide R10 retention/reservation. Keep additions backward compatible
with other audit callers, or put the hosting bounded journal in this package.

ADD `TestStoreCrossOwnerDeniedBeforeIO`: alice/bob same label, foreign binding
denied before open; bob state untouched. ADD `TestStoreSecondWriterDenied`:
child process competes for same store; only one writer, reopening after crash
works. ADD `TestPublishRequestReplayConflict`: same id/input returns same
operation/revision without token; same id/different input conflicts, including
after restart. Real temp stores and subprocess; no fake filesystem oracle.

### H1.2 Confined staging and quota reservations

New `snapshot.go`, `quota.go`, `publish.go`; source/release roots fixed at service
construction. Reject escapes, symlinks, special/hidden/credential files; copy
with per-file and total byte/file accounting, limits during streaming, stable
metadata checks against source replacement. Trusted profile allowlist only.
Reserve staging+existing+retired bytes and metadata before promoting. Failure
cleans owned staging but leaves committed revision unchanged. No execution.

ADD `TestSnapshotSymlinkSwap`: race rename/symlink to a distinct foreign sentinel
while copying; never commit sentinel bytes. ADD `TestSnapshotRejectsUnsafeFiles`
table for traversal/absolute/dotfile/symlink/FIFO and approved asset positive
control (platform-native special-file cases explicitly gated). ADD
`TestPublishQuotaReservationAcrossProcesses`: 512 MiB/file ceiling and replacement
headroom, two competing API operations plus denied second writer; never double
allocate. ADD `TestPublishCopyFailurePreservesRevision`: injected read/disk-full
boundary leaves old bytes/revision accessible, failed staging eventually zero.

### H1.3 Commit, recovery, tombstones and journal capacity

New `recovery.go`; use desired vs committed revision and explicit finalizer
steps. Inject crashes before/after stage rename, quota state write, commit,
access revoke and cleanup checkpoint. Startup reconciles actual owned files
before accepting mutations. Unpublish writes tombstone/epoch first; old
operation cannot revive it. Retain bounded replay/tombstone metadata charged
to quota; do not delete required fences just to accept new work.

ADD `TestPublishCrashRecovery` table for every write boundary: old complete or
new complete, never partial; no double charge. ADD `TestUnpublishFencesStaleCommit`:
pause publish, unpublish committed revision, resume stale operation; no resurrection.
ADD `TestAuditCapacityDeniesBeforeMutation`: full nonexpired journal refuses
publication with no snapshot/revision mutation; interrupted admission recovered.

## Acceptance / red team

A1–A4 and persistence part of A9/A10: two synthetic roots with different
sentinels; exact limit/limit+1; actual disk bytes agree with charged accounting
after every injected interruption. Check no tokens, paths, credential names
or content in journal/DTO/errors. KEEP existing exposure/DNS tests unchanged.
These tests are ADD plans, not executed evidence. Run package race tests and
just check as allowed; report coverage >=85% own statements and gate failures.
Docker remains prohibited; config-check cannot run under that restriction.

## Out of scope / done

No serving, tunnel, workload execution, dependency installer or public feature
claim. Done only with code, negative/concurrency/recovery tests, checked quotas,
docs for actual behavior and state evidence. Shared #147 stays open. Move this
CHG to done and update links/index only when its own acceptance is demonstrated.

## Implementation record — 2026-10-26

Source implementation is present in this worktree. Current status and remaining
gates are recorded in state.yaml; offline contract evidence is centralized in
CHG-0174/evidence.md. Native runtime, browser and sish proof is not inferred.

The service entrypoint reuses `hubctl hosting`; the optional hosting-only Go
controller reuses ToolHub admission. Dynamic ingress is a bounded network-none
OCI-exec relay, with a helper-only volume and exact release volume subpath.
Shared fleet/zone metadata locks coordinate limits and address ownership; no
app content or secrets are shared through those coordination volumes.
