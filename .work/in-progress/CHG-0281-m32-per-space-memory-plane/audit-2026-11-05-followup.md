# M32 follow-up audit — 2026-11-05

This supplements, rather than replaces, [the earlier audit](audit-2026-11-05.md).
Source snapshot: HEAD `97f79b68`, branch `chore/merge-m31-m20-into-dev`,
212 changed/untracked paths before this document. The work is uncommitted;
the Devin transcripts establish history, not attribution of every current edit.
Only audit/plan/state documents were edited by this audit. No personal spaces
or credentials were read. Source evidence is not live deployment acceptance.

## What has improved

- **F1's retired-file gate is fixed in source.** Supervisor `loadSelected`
  reads `ReadEffectiveSpace` directly; group authorization reads the published
  identity. `materializeHermesConfig` now refuses a missing revision instead of
  materializing empty settings. Keep negative identity/path tests and prove the
  full init/apply/control path with real authored publication.
- Repository search finds no remaining calls to deleted `stack.Read`,
  `stack.WriteSpace` or `stack.LegacyFeatureTools`. Many CLI/supervisor fixtures
  were updated; the previous compile-error report must not be copied as current.
- The full `internal/stack` package passes in this follow-up (23.340s).
  Native-skills denial now expects the actual managed-read error, the vault
  initialization fixture explicitly enables memory, and `config.image_gen`
  now has compiler validation, a consumer declaration, a typed view and a
  Settings projection. The FAL expectation was retained, not replaced by the
  accidental CLIProxy default. Runtime media endpoint/auth assertions were
  moved to the managed-runtime environment contract.
- Sidecar removal resolves an immutable container ID and validates its ownership
  labels before removal by ID. This is a useful independent hardening change;
  it does not complete the passport or memory cutover.
- `devcheck format` passed during this invocation of `just check` (the lint leg
  advanced to vet). The earlier six-file gofmt failure is historical.

## Remaining defects, in implementation order

### P1 — Activation and authority are still incomplete (earlier F2/F3/F4/F7)

- `space_apply.go:applyConfirm` still reconstructs rollback from grants only.
  `DeriveSpaceRights` loses `tools.binding.profile_id`; first-apply recovery
  explicitly supplies an empty profile ID. Recovery can rewrite sibling profiles
  in the same environment. Restore the exact affected records and retain concurrent
  revocations; test first apply, reapply and partial failure separately.
- `agenttools/settings.go` and `stack/hermesconfig.go:applySelfSettings` still
  make the runtime-owned overlay effective. Move supported edits to the governed
  six-passport publisher, then remove the overlay authority and its writers.
- The `tool-policy` live hook is still a no-op, and admission still consumes stored
  profile selections. Rights derivation repins selections; it does not derive
  their membership from the tools passport. Implement and activate that projection
  independently of grants; verify list/call changes within an existing session.
- Current-pointer publication, per-file config-view updates, render, ledger updates
  and service reconciliation are not one consistent activation/recovery operation.
  A pointer rollback is not rollback of already changed services or ledger records.

### P1 — Memory and automation are not moved (earlier F5)

`HUB_VAULTS_BY_PRINCIPAL` is still only rendered; ToolHub has no production reader.
Memory tools still operate on local `VaultDir`; communication owns the routines
engine, capture and night worker; the shared `runtime/run-scope/current` remains.
Runtime mount removal therefore precedes the actual backend move. OpenKnowledge's
direct vault route also remains a boundary to close. CHG-0284/0286/0287 are not
implemented, CHG-0285 is partial, CHG-0288 is not accepted.

Finish real ToolHub storage/calls/events, exact-run authorization and independent
memory rights/confirmation before claiming functional or isolated per-user memory.
Implement per-user dreaming in the existing routine mechanism, with local-day
idempotence, restart recovery, admission/commit checks and communication downtime
affecting delivery only. Do not restore runtime mounts to hide the missing backend.

### P1 — Hosting is still applied to the wrong consumer (earlier F6)

`consumers.go` marks hosting fields as compose/recreate, but
`reconcile.go:runtimeTarget` still falls through to `hermes-runtime`.
Wire hosting and its controller, and check their actual loaded configuration.
Image-generation carry-through is now present; it still needs real authored
apply/respawn coverage, including removal/reset, rather than only generated fixtures.

### P2 — Test repair is incomplete and some acceptance evidence is misleading

- `just check` again fails **all eight** `TestDeploymentConfigurationContract`
  variants at line 32: init creates authored files, but the test reads an effective
  revision before publishing it. It also hardcodes `prod` inside the dev/prod loop
  and retains a retired `legacy` axis. Repair the lifecycle and supported matrix;
  do not recreate legacy readers to make this fixture pass.
- `TestRenderAllFeatures` still checks browser/timeouts through a clone with
  `CapabilityMode=""`. Keep those guarantees on the actual managed path.
- `publishManagedDocs` writes generated files/current directly, stamps metadata
  `environment: dev` into both env views and bypasses compilation/publication.
  `saveSpace` returns nil unconditionally after that helper. Low-level reader tests
  may need synthetic revisions, but these helpers are not proof of authored apply.
- Earlier managed web/provider transit and attestation assertions were removed
  from `TestManagedEgressRelayRenders`. A negative legacy-influence test does not
  replace a positive passport-provider round trip. Preserve both boundaries.
- Organization policy still selects `scope.yaml`/`settings.yaml` by mtime in
  `ReadOrganization`. `ReadScope` now reads the authored identity. Classify these
  authority paths explicitly; protected platform policy is distinct from agent
  configuration, but ambiguous aliases and unpublished identity must not silently
  affect running policy. This needs a targeted check, not indiscriminate deletion.

## Verification

- `go test -p 1 ./internal/stack ./cmd/hubctl ./internal/migration ./internal/supervisor`:
  stack (23.340s), hubctl (58.365s) and migration (2.019s) passed. The multi-package
  process was stopped before a supervisor result (exit 1); this is not a full pass.
- `go test -p 1 -timeout 90s ./internal/passport ./internal/passportview
  ./internal/reconcile ./internal/migration`: all four packages passed, exit 0.
- `go test -p 1 -timeout 90s ./internal/supervisor -run
  'TestApprovalRejectsChangedUnavailableAndExpandedPolicy|TestApprovalRechecksOrganizationMembership|TestScheduledExecutionReauthorizesPolicyBeforeRuntimeStart'`:
  passed (6.529s), exit 0. This covers the selected policy regressions, not the
  complete supervisor suite or a running container.
- `just check`: configuration-contract leg failed with process exit 1; formatting
  passed and docs leg advanced to actionlint. The broad run was stopped after the
  concrete failure (exit 1). No full coverage/vet/security result is claimed.
- `go run ./cmd/devcheck docs` passed after the follow-up document/plan changes;
  scoped `git diff --check -- .work/index.md` passed.
- No Docker, live deployment, catalog rebind/respawn, provider calls or two-user
  memory/dreaming acceptance ran.

## Continuation

Use [the updated session prompt](handoff-2026-11-05.md). Existing ADR-0095/0096 and
SPEC-0094/0095 already require the missing behavior. No new ADR/SPEC is needed;
do not weaken those accepted contracts to match the partial implementation.
