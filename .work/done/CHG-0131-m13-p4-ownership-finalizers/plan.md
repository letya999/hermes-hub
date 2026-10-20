# CHG-0131: Ownership labels, finalizers and a reconciler (M13 stage P4)

Chain: issue #285 -> this plan -> `state.yaml`.
Previous stage: `CHG-0130` (unified connector surface + operation journal).
Next stage: `CHG-0132` (P5 catalog tiers / CI images).

## Goal

Replace "do and forget" cleanup and per-case self-heal with ownership labels +
finalizers + one reconciler: `connector_remove` converges to zero resources with
a verified receipt, drift between the ToolHub store, Docker and the Credential
Broker heals through one mechanism, and concurrent users never block each other.

## Read before coding

1. `docs/adr/ADR-0048-unified-connector-surface.md` — verb/journal contract.
2. `docs/adr/ADR-0038*` — file-store evidence (stays on the file store).
3. Issue #285 acceptance criteria.

## Preconditions and rules

- Red test first, then the smallest green change. No implementation "on spec".
- `hermes-hub.*` label keys are the only ownership proof; objects with foreign
  or missing labels are never touched.
- The reconciler never silently recreates secrets.
- `Authorize` keeps "revoke wins before backend admission".
- `just check` before handoff; Go coverage >= 85%.

## Slices

### S4.1 Ownership label set on spawned Docker resources

Anchors: `internal/toolhub/generic_controller.go` `scopeLabelArgs`;
`generic_fallback.go` `network/volume/create` argv; `generic_handler.go`;
`cell_net.go`, `cell_init.go` `labels :=`.

Change:
1. `internal/toolhub/api/labels.go`: label keys
   `LabelOwner/LabelConnection/LabelBinding/LabelOperation/LabelRecipe` and
   `LabelArgs(map)` -> sorted `--label k=v` argv (fail-closed: caller drops
   empty values).
2. Spawn paths stamp `hermes-hub.owner=<principal>`, `.connection`, `.binding`,
   `.recipe` alongside the existing `hermes-hub.scope`/`.role` labels. The
   binding identity is already in scope at every create call site (network,
   proxy/bridge/relay volumes, cell containers).
3. `genericController.labelQuery(ctx, map[string]string) ([]string, error)`:
   `docker ps -a`, `volume ls`, `network ls` with `--filter label=`; bounded
   output; shared by finalizers and the reconciler.
4. Workspace dirs get a `.hermes-owner` marker file at materialize time
   (`api.RemoveBindingWorkspace` counterpart), consumed by the workspace
   finalizer.
5. Broker credential `metadata` records `hermes-hub.connection` when the
   broker client supports metadata; otherwise the store keeps the mapping
   (documented limitation).

Tests (red first):
- `TestSpawnStampsOwnershipLabels`: docker-runner spy sees
  `--label hermes-hub.connection=<id>` on volume/network/container creates.
- `TestLabelQueryBuildsFilters`: labelQuery argv carries the label filter and
  parses names.

### S4.2 Finalizer ladder on connector_remove

Anchors: `internal/toolhub/control.go` `remove`;
`control_binding.go` `cutAuthorization`, `releaseBindingWorkloads`;
`connector_journal.go` `connectorRemove`; `api/types.go` `Status`.

Change:
1. `api.DeletingStatus` on `Connection`; remove sets connection -> `deleting`
   and cuts authorization (projection removed) before finalizers run.
2. `Onboarding.Finalizers []api.Finalizer` — `{name,state,attempts,error}`;
   ladder: `workloads`, `volumes`, `network`, `broker-credential`,
   `ciphertext`, `workspace`, `image-ref`. Each step idempotent.
3. A failed step persists `state=failed` + error; onboarding phase becomes
   `cleanup_incomplete`; the next remove resumes at the first incomplete
   finalizer (retryable, bounded attempts recorded).
4. `verify-zero`: labelQuery over every resource kind with the connection
   label must return empty; the receipt
   (`removed: N container(s), M volume(s) ... verified 0 remaining`) lands on
   the operation and `connector_status`; the record becomes a tombstone.
5. `connector_disable` keeps state volumes and credentials — only workloads
   stop and projection is cut (existing behaviour, asserted by test).

Tests (red first):
- `TestRemoveEmitsVerifiedReceipt`: spy docker returns zero post-purge;
  receipt text + tombstone phase.
- `TestRemoveFinalizerRetryAfterFault`: broker revoke 500 x3 ->
  `cleanup_incomplete` naming `broker-credential`; 4th attempt converges.
- `TestRemoveVerifyZeroBlocksOnLeftover`: label query reports a stray volume
  -> not done, `finalizers` field visible in `connector_status`.

### S4.3 Reconciler — drift healing loop

Anchors: new `internal/toolhub/reconciler.go`; `store.go` `MarkDegraded`;
`api.PostPrepareNotice`.

Change:
1. `Reconciler{Store, Docker labelQuery, Broker, Notify, MaxScan}` +
   `ScanOnce(ctx)`. Bounded: at most MaxScan objects per pass.
2. Orphan: a docker object labelled `hermes-hub.scope=<project>` whose
   `hermes-hub.connection` has no store record -> purge (rm). Objects with a
   foreign or missing hub label are never touched.
3. Desired-but-missing: an active connection whose broker credential is gone
   -> `degraded` + owner notice carrying a reauth link (#283 path). No silent
   re-mint.
4. Recipe digest drift: stored recipe digest vs resolved digest differs ->
   `degraded`, write tools hidden, `/confirm` link asks to accept.
5. Unreferenced images labelled `hermes-hub.recipe` not referenced by a live
   connection -> `image rm` (shared layers kept by the daemon).

Tests (red first):
- `TestReconcilerPurgesOrphans`: labelled container+volume+credential with no
  store record are removed; foreign-labelled objects untouched.
- `TestReconcilerDegradesMissingCredential`: deleted broker cred on a live
  connection -> `degraded` + reauth notice; credential not recreated.
- `TestReconcilerRecipeDrift`: digest change -> `degraded`, writes hidden.

### S4.4 Per-connection locks, readiness outside the write lock

Anchors: `internal/toolhub/store.go` `mu`; `control_*.go` `enableWithReady`
readiness path.

Change:
1. `Store.connLock(id)` striped locks; mutation paths that span one
   connection take it instead of the store-wide write lock where safe.
2. Readiness/probe: reserve revision -> probe outside the lock -> commit via
   compare-and-swap on `Connection.Revision`; a revoked revision loses.
3. `Authorize` re-checks revision under the conn lock — revoke wins before
   backend admission.

Tests (red first):
- `TestConcurrentInstallsIndependentConnections`: 20 principals install in
  parallel (5 s fake readiness); all converge; no lock-order deadlock.
- `TestRevokeWinsDuringProbe`: revoke between reserve and commit -> commit
  rejected, `Authorize` denies.
- `BenchmarkAuthorizeUnderInstall`: p95 `Authorize` for unrelated principals
  recorded in state.yaml evidence (report, not a hard gate).

### S4.5 Store compaction

Anchors: `internal/toolhub/store_io.go` `saveLocked`; snapshot structs in
`store.go`.

Change:
1. `Store.compactLocked(now)` before persist: tombstone onboardings older
   than `HUB_STORE_TOMBSTONE_DAYS` (default 30), terminal operations older
   than `HUB_STORE_OP_DAYS` (default 7); embedded definition payloads dropped
   in favour of recipe digests where the recipe record exists.
2. Compaction is pure deletion of terminal records — live state untouched.

Tests (red first):
- `TestCompactionBoundsSnapshot`: synthetic 10k-operation history -> snapshot
  stays bounded; a fresh tombstone survives.

### S4.6 Tool-surface digest pinning (rug-pull guard)

Anchors: `control_credentials.go` admit/probe; `api.ToolBinding`;
`projection.go` write gate.

Change:
1. `api.ToolSurfaceDigest(tools []ToolSpec)` = sha256 over a canonical
   JSON of {name, inputSchema, outputSchema, annotations} sorted by name.
2. Pinned at admission on the binding (`ToolBinding.ToolDigest`) and
   connection metadata.
3. Drift (reconciler or enable re-probe) -> `degraded`,
   `WritesEnabled=false`, `/confirm` link; confirm accepts the new digest and
   re-enables.

Tests (red first):
- `TestToolDigestStableAndOrderIndependent`.
- `TestToolSurfaceDriftDegradesAndHidesWrites`: changed fixture tool ->
  degraded + write tool absent; `/confirm` restores.

### S4.7 Docs and closeout

- `docs/adr/ADR-0049-ownership-finalizers.md` (appended), index row.
- `docs/architecture.md` lifecycle section: labels -> finalizers -> receipt.
- `state.yaml` done; `just check`; issue #285 comment.

## Red-team checklist

- Label spoofing: a container carrying `hermes-hub.connection` of another
  principal — reconciler checks scope label AND store membership; scope label
  is stamped only by this hub's project.
- Orphan purge must never touch foreign/no-label objects (asserted).
- Finalizer retry storm: attempts bounded, backoff between scans.
- Reconciler never recreates secrets; degraded + human reauth only.
- `/confirm` digest accept requires the one-time token — no model-side
  approval.

## Acceptance

Contract (CI-testable, all mapped to tests above): chaos-kill after each
finalizer step (remove resumes and converges), fault-injected broker revoke,
orphan purge, credential-deletion drift, tool-surface drift, 20-way parallel
install, 10k-op compaction bound.
Live (owner stack, post-push): remove a stateful connector -> receipt shows
zero; `docker ps -a`/`volume ls`/broker list empty for the label; hub restart
mid-remove converges.

## Out of scope

- New database or queue; the file store stays (ADR-0038 evidence).
- Team connectors (#288) and catalog tiers (#286) — later stages.
- Changing the Authorize hot path shape beyond the revision check.

## Done checklist

- [ ] Tests red then green per slice; `just check` green.
- [ ] ADR-0049 appended; architecture.md lifecycle section.
- [ ] Live acceptance evidence recorded (or honestly deferred).
- [ ] Issue #285 updated; state done; folder moved to `.work/done/`.
