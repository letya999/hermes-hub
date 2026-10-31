# CHG-0201 evidence

S201.1 partial: a process fixture showed that the notifier's first-snapshot
RAM priming lost a pending restart revision. The initial test failed before
`cmd/toolhub` wired the notifier to the Store. Applied revision now persists
in the existing ToolHub JSON snapshot; a new notifier and Store load skip an
already acknowledged revision and post a higher pending revision. A second
Store instance also observes the applied revision after Reload. Monotonicity
and failed applied-write reporting were separate red/green tests. Focused
`-race` runs for cmd/toolhub, ToolHub and api passed. These are local synthetic
filesystem and HTTP fixture tests, not actual supervisor/runtime acceptance.

Root `just check` passed after desired/applied implementation: 85.95% own Go
statement coverage (`.work/tmp/chg0201-applied-revision-root-check.log`). A
subsequent file-backed stale-writer regression failed for both absent and
existing applied revisions: failed `saveLocked` had advanced the in-memory
acknowledgement. The Store now restores the previous revision on failure.
Focused ToolHub/api/cmd `-race` tests passed after that change; docs check and
`git diff --check` passed. Full root `just check` was rerun after the rollback
fix and passed at 85.95% (`.work/tmp/chg0201-applied-write-rollback-root-check.log`).

Same-snapshot duplicate revisions previously posted twice (and a descending
legacy baseline could regress). Both failures were reproduced before a
small notifier coalescing change; focused ToolHub/api/cmd `-race` runs passed
afterward. Full root `just check` passed at 85.96%
(`.work/tmp/chg0201-projection-burst-root-check.log`).

S201.3 partial: static and registry model relay previously returned untyped
plain-text 502 after all fallback hops failed. Both modes failed the new HTTP
contract test before the change and passed afterward with a fixed
`model_route_unavailable` JSON code. Focused `-race` also covered fallback,
cooldown and redirect refusal. This is a local HTTP fixture, not pinned
Hermes or container acceptance. Full root `just check` passed afterward at
85.96% (`.work/tmp/chg0201-typed-model-outage-root-check.log`).

S201.1 legacy semantic digest: a file-backed synthetic fixture reproduced a
metadata-only projection revision causing a second restart. The digest now
tracks projected tool specs, binding/policy pins, grants, connection ownership
and status, credential references/revisions/status, while ignoring health
observations. Matching digest advances the persisted applied revision without
posting; a disabled binding still posts. Reload and stale-writer conflict
tests cover durable digest and rollback. Managed profiles, team bindings and
unknown authority return no digest, so revision-based restarts remain. Focused
ToolHub/api/cmd `-race` passed. Full root `just check` passed at 85.96%
(`.work/tmp/chg0201-legacy-semantic-digest-root-check.log`).

Managed/team semantic digest and container reconciliation remain open. No
Docker resources or working data were touched.

S201.1 restart request contract: notifier now sends a stable operation ID,
reason and revision; supervisor forwards them with the selected generation.
Runtime validates the bounded marker and rejects malformed, stale-generation,
same-revision/different-operation and corrupt state. Concurrent revisions
first reproduced a lower final marker (15 after 16), then passed with
serialized compare/write. A concurrent reader first saw empty JSON during
in-place replacement; atomic replacement now preserves complete documents.
Symlink marker escape was reproduced and rejected. Focused runtime,
supervisor and ToolHub API `-race` tests passed. These are synthetic HTTP and
filesystem fixtures, not container or pinned-Hermes integration. Marker file
and directory sync under sudden power loss remain unverified.

After the marker change, the first root `just check` failed lint on a
shadowed local error variable; the correction passed focused staticcheck and
restart tests. The full rerun passed lint and every Go package except the
unchanged `internal/mediasvc` test
`TestComfyVideoResumesPersistedHandleWithoutSubmission`, which hit a Windows
temporary `job.json` sharing lock. Its isolated `-race -count=3` repeat passed.
The full gate remains failed and its aggregate coverage was not emitted
(`.work/tmp/chg0201-restart-marker-root-check-fixed.log`).

S201.3 title failure: the existing worker records the terminal result before
best-effort auto-title lookup. A synthetic runner whose `SessionUsage` fails
left the completed job, result mapping and auto-name intact. Focused
Communication `-race -count=3` passed. No external message was sent; the
fake API captured the local delivery. Pinned-Hermes tool error behavior is
still unverified.

The unchanged Comfy resume fixture failed twice in full Windows runs because
its polling read treated a transient sharing lock during `job.json` replacement
as terminal. The test now retries reads within its existing two-second
deadline; persistent failures still fail. Focused `-race -count=20` passed.
The subsequent root `just check` passed at 85.96% own Go statement coverage
(`.work/tmp/chg0201-final-root-check.log`).

A further fault-injection regression showed that acknowledging a marker after
a failed file sync would replace the previous request. The helper now syncs
the temporary file before rename and propagates sync failure without changing
the old marker. Focused runtime `-race` marker tests passed. Directory-entry
sync and native crash/power-loss acceptance remain outstanding; the prior
root `just check` predates this small change. A later complete root `just
check` passed with 85.97% own Go statement coverage
(`.work/tmp/chg0200-final-claim-root-check.log`); no Docker or native
power-loss acceptance was run.

Managed semantic digest now includes admitted tools, effective scopes/limits,
control surface and runtime identity; profile issue/reason metadata is excluded.
Team digest includes only the caller's membership/write authority and excludes
unrelated membership/observation metadata. Red regressions first observed empty
digests, then passed with the existing legacy authority cases under `-race`.
Unresolvable authority retains conservative revision-based restarts. Full root
gate after these changes remains pending; no Docker or acceptance was run.

S201.2 restart causes: a deferred ToolHub restart request on a busy runtime
now persists a bounded `pending_restart` cause (operation ID, reason,
revision, generation — no free text) alongside runtime state. A synthetic
supervisor restart first lost the cause; after the deferred-path write the
reloaded manager still reports it (`TestRestartReasonSurvivesSupervisorRestart`).
An applied restart clears the cause and persists the spend, so a stale reason
cannot reappear after reload (`TestAppliedRestartClearsPendingCause`). The
gateway's post-delivery `/v1/restart` poll coalesces on an unarmed runtime:
after the armed marker was consumed, a follow-up poll does not bounce the
container again (`TestStatusDeliveryDoesNotTriggerFinalRestart`). The
revoke-immediacy requirement is already enforced per call and covered by the
pre-existing `TestManagedRevocationOnOpenMCPSession` — a revoked capability
denies the next call on an open session without waiting for any restart.
Focused supervisor package and `-race` runs passed.

S201.3 marker directory sync: a crash could still drop the acknowledged
rename even with the file itself synced. `writeRestartMarker` now syncs the
parent directory entry after the atomic rename (Unix helper added; Windows
keeps the atomic-rename no-op as documented). A fault-injected directory sync
first let an unsynced rename be acknowledged; now sync failure propagates and
the caller is not told the marker is durable (`TestRestartMarkerDirectorySync`,
`-race` green). Native power-loss acceptance remains outstanding.

The pinned-Hermes tool `IsError` vs transport-failure probe is still
unverified: it requires the pinned upstream contract probe (container gate),
which is deferred with the rest of Docker acceptance.

Gate closure: root `just check` passed at 86.02% own Go statement coverage
(`.work/tmp/chg0201-restart-causes-root-check-6.log`). The run surfaced and
cleared pre-existing leftovers of this workstream: a dead `storageLimits`
field and a dead `applyConfirmedWrites` (superseded by verified channel
confirmation), a gofmt drift, and an ST1005 usage-string violation — the last
gained a regression test (`TestStorageUsageErrorIsCompleteWords`). Two
load-induced flakes in untouched packages (`TestNativeWireAndAllowlist`,
`TestModelRelayTimedOutHopFallsBackWithinRequestDeadline`) passed isolated
repeats; the model-relay test's 40ms hop timeout was raised to 250ms to close
its flake window without changing the timeout-fallback contract. No Docker,
deployment, migration or live acceptance was run.
