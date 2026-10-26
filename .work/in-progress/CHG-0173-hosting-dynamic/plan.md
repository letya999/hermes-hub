# CHG-0173: Admitted stateless dynamic sites (M18 #147, H4)

Chain: ADR-0079 D4/D5/D6/D10/D11 → SPEC-0077 R7–R10 → plan → state.yaml.
Requires CHG-0170–0172 code; live predecessor proof tracked by CHG-0174.
Next: CHG-0174. Evidence: design research and pinned Haloy review in CHG-0169.

## Goal / immutable boundaries

One stateless python-http profile survives the chat with hard resource/lifetime
limits and independent cleanup. No subprocess in Hermes, Haloy fork or new
sandbox implementation. Extend existing ToolHub controller admission, inspect
and finalizer seams; never reuse ephemeral CLI idle eviction as site lifetime.
Read ADR/SPEC, `internal/toolhub/cell_lifecycle.go`, `cell.go`, `cell_net.go`,
`api/admission.go`, `remove_finalizers.go` and their actual callers/tests.

## Ordered slices (one commit each; red test before implementation)

### H4.1 Persistent site workload contract and policy

New `internal/toolhub/api/hosting.go`, `internal/toolhub/hosting.go`; controller
route wiring beside existing admitted workloads. Structured owner/env/site/
revision/expiry/profile/release receipt, no agent-provided raw image/mount/env.
Operator pins image digest and fixed non-shell entrypoint/port. One Python
stdlib profile, no pip/npm/build hooks. Validate hosting runtime explicitly
runsc or verified Kata; do not accept empty/runc from cell defaults.
Capacity reservation includes old+new replacement before create. Reuse shared
admission/label/receipt primitives after reading callers; do not silently change
CLI behavior. No secrets delivery in first profile; any request for unsupported
Broker env fails closed with named error rather than passing raw credentials.

ADD `TestHostingMissingSecureRuntimeDenied`: empty/runc/missing runsc denies
before create; approved available tier positive control. ADD
`TestHostingWorkloadIdentityAndProfileDenied`: owner/image/mount/entrypoint/env
substitution fails before daemon dispatch. ADD `TestHostingComputeOverlapQuota`:
exact aggregate cap succeeds, replacement +1 fails without stopping old app.

### H4.2 Create/inspect/start and owned ingress relay

Existing anchors: `cell_lifecycle.go:createCell`, `cell.go:runtimeFlag`,
`cell_net.go:cellEgress`, inspect/profile helpers located through callers.
New hosting workload argv/profile helpers: digest, non-root, read-only root
and release, drop all caps, NNP, seccomp, CPU/mem/PID, bounded tmpfs, `log-driver
none`, no inherited env, no Docker socket/full-space mount. Own private network,
no default egress or hub attachment. Per-site restricted ingress relay can reach
only that workload; no shared path from app to control/Broker/sibling networks.
Inspect create-time profile before start and running receipt before readiness.
Run runtime-specific canaries; readiness starts unknown/unavailable, never
optimistically healthy like Haloy StateTracker.SyncTargets.

ADD `TestHostingInspectDriftDenied` table removing each mandatory control:
no start for pre-start drift, stop+durable finalizer for post-start drift, no route.
ADD `TestHostingRelayCannotReachForeignTarget`: wrong revision/IP/port/host
denied; correct admitted target only. These are contract tests with scoped fake
executor, not proof of actual kernel isolation. ADD
`TestDynamicProxyStripsHostingCredentials`: own app cookies preserved if safe,
hosting cookie/auth and untrusted forwarded headers absent; parent-domain
Set-Cookie rejected, request/response bounds and unsafe Origin gate enforced.

### H4.3 Bounded restart, expiry and reverse-order finalization

New persistent lifecycle in `internal/hosting/dynamic.go`/`reconcile.go`;
reference `internal/toolhub/remove_finalizers.go:runRemoveFinalizer/purgeLabelled`.
Persist restart counters/backoff, desired/active revision, inspect proof and
owned workload/network/relay handles before mutation. never/on-failure bounded
policy, no unless-stopped lifetime shortcut. Recheck revision immediately before
route swap and destructive cleanup. Unpublish/expiry revokes first; retry scoped
workload→relay/network→release cleanup to verified zero. No global image/volume
prune. Kill traffic within 30s after revoke even if cleanup fails. Any future
Broker grant must bind revision and stop app on revoke; no provider guessed here.

ADD `TestHostingRestartBudgetPersists`: crash/restart controller cannot reset
attempt count or extend site expiry; fake clock verifies 1–30s backoff and ceiling.
ADD `TestStaleHostingEventCannotStopNewRevision`: delayed old event after new
route selection cannot stop/delete active revision. ADD
`TestHostingFinalizersResumeScoped`: crash each step, restore and retry; foreign
project/env/site resources untouched, no success until verified zero.

## Acceptance / red team / done

A7/A8/A9/A10 plus A11-dynamic. KEEP CLI-cell tests including
TestCLIExecCellProfileDrift/TestCLIExecRuntimeTiers/TestCLIExecWorkspaceScopes;
ADD hosting cases because different lifecycle/ownership. Real Linux runsc/Kata
acceptance in CHG-0174: foreign-space/hub/metadata/credential sentinels unreachable,
CPU/mem/PID/tmpfs/inode controls enforced, workload stdout not persisted,
site remains after chat, expiry and failed restart stop it. No simulated daemon
result counts as isolation proof. Docker forbidden now: implementation can be
contract-tested, live evidence remains unavailable until authorized.

Update current docs only for actual behavior; package/race/coverage and just
check with honest limitations. Dynamic is required for #147, but a fake executor
pass is insufficient to close it. No writable persistent app disk, extra runtime
languages, opt-in secret delivery implementation, arbitrary images or egress.

## Implementation record — 2026-10-26

Source implementation is present in this worktree. Current status and remaining
gates are recorded in state.yaml; offline contract evidence is centralized in
CHG-0174/evidence.md. Native runtime, browser and sish proof is not inferred.

The service entrypoint reuses `hubctl hosting`; the optional hosting-only Go
controller reuses ToolHub admission. Dynamic ingress is a bounded network-none
OCI-exec relay, with a helper-only volume and exact release volume subpath.
Shared fleet/zone metadata locks coordinate limits and address ownership; no
app content or secrets are shared through those coordination volumes.

Profile correction 2026-10-26: the first admitted profile is `go-http`, not
`python-http` (ADR-0079 amendment; SPEC-0077 amended). The release carries a
workspace-built static Linux ELF at `app`; PID1 executes `/work/app` on
`127.0.0.1:8080`. Publish rejects a missing or non-ELF `app` before snapshot
work and marks the committed binary executable.
