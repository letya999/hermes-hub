# CHG-0149: Connector recovery and system guide

Chain: ADR-0077 -> SPEC-0076 R1-R10 -> this plan/state.yaml.

## Goal

Complete the owner's combined plan: establish the credential incident, repair
access and recovery, expose truthful inventory, document the system and deliver
the English reference skill by default to every user/runtime.

## Slices

1. R1/R5: safe diagnosis and deterministic authoritative ownership. Anchors:
   internal/toolhub/store_queries.go onboardingForConnection;
   reconcile_seams.go reconcileBrokerClient; control_broker.go brokerCredentialGone.
   Tests: owner mismatch, historical onboarding, deterministic ties, team context.
2. R2/R3/R4: Broker persistence and encrypted recovery. Anchors:
   services/credential-broker/internal/journal, provider/local.go, broker/broker.go,
   app/config.go; internal/stack/compose.go. Tests: crash at each durability
   boundary, lost primary volume, corrupt/wrong-key backup, stale revocation.
3. R4/R5: typed recovery reconciliation and bounded durable retries. Anchors:
   internal/toolhub/reconciler.go, reconcile_seams.go, connector_journal.go.
   Tests: independent failures, degraded revisit, delete/restore race, outage.
4. R6: canonical scoped inventory and status. Anchors: connector_facade.go,
   gateway.go, control.go, internal/communication/gateway_toolhub.go.
   Tests: two users/team, history, permission denial, all transports/statuses.
5. R7/R8: English guide and mandatory reviewed delivery. Anchors: config/skills,
   config/SOUL.md, internal/stack/materialize.go, prompt_contract.go,
   hermes_capability.go, internal/supervisor/ensure.go and ToolHub read surface.
   Tests: managed/no loader, custom skill directory, existing persona, spawn/render.
6. R1/R9/R10: incident remediation, operational documentation, scoped rollout and
   isolated end-to-end acceptance. Rotate exposed service credentials without
   displaying values; never guess original owner from encrypted orphan files.

## Verification and red-team

Red regression first for authorization, HTTP and concurrency changes. Check
cross-owner/environment substitution, stale backups after revoke, absent keys,
concurrent delete and restore, unauthorized team access and secret canaries.
Run root just check and Broker's own pipeline (Linux required for securefs),
coverage >=85% with Broker per-package gate, docs checks and Docker gates per
AGENTS.md. Real provider/channel evidence is separate from mocks. Destructive
failure injection targets isolated synthetic data only.

## Scope and evidence discipline

No new framework/database/queue. No upstream Hermes patches. No automatic
provider mutation, login or external sending. No live transcripts or credentials
in tracked files. Diagnostic artifacts only under ignored .work/tmp after checking
ignore status. Preserve other task changes. This plan retains all requirements;
completion of one slice does not close the overall goal.

## Initial evidence

- Working tree clean at start; HEAD a978c65.
- Existing GetCredential masks owner mismatch as not_found.
- Existing reconciler tests only active connections for credential liveness.
- Current onboardingForConnection selects the first Go map match.
- Attached historical diagnostics are not fresh live-state proof.
- Existing native skill defaults exclude managed profiles.

## Implementation progress

- `hubctl up` now provisions the environment recovery volume before starting
  infra/Broker after preflight/build; lifecycle regression passed. Dockerfile
  seeds the mount target as 10001:10001 mode0700 for empty-volume initialization.
  No recursive ownership changes of existing data. `just docker-check` is running
  with ignored output in `.work/tmp/CHG-0149/docker-check.log`; real new-image
  volume initialization was verified separately on hermes-hub:test with a fresh
  synthetic volume: UID/GID10001 mode0700 and non-root write succeeded. Full image
  acceptance remains pending; the image predates later reconciler edits.
- Broker startup now persists an initialization witness after successful Build
  and refuses an empty journal when that witness survives. Optional
  `state_witness_dir` allows independent placement; fallback is the key directory.
  Linux journal-loss and independent-installation replacement regressions passed,
  as did full module `go test ./...` in Linux. Corrupt-witness startup leaves
  journal/marker unchanged. Compose now selects an independent environment-scoped
  external volume via the CLI witness flag; root stack/supervisor tests passed.
  Legacy key-directory witness migration rejects empty-state bypass; full Linux
  Broker module tests passed after the fix. This is detection only: provisioning/rollout,
  generation binding, prefix rollback protection, encrypted backups
  and automatic restoration remain required; do not treat S2 as complete.
- Legacy `statusBody` no longer equates enabled onboarding with readiness:
  it returns owner-visible connection status separately and suggests diagnostics.
  Regressions cover active, degraded, disabled, revoked, reauth and deleting,
  plus missing/foreign references. Operation-history status still needs the
  remaining unified representation; no provider success is inferred here.
- Connector list filters terminal/superseded onboarding records without deleting
  history. Candidates distinguish definition transport, onboarding phase and
  owner-visible current connection status; team connection status does not rely
  on a personal onboarding. Red/green regressions pass. This is an intermediate
  facade change: owned connections without onboarding are now included and scoped
  onboarding records are deduplicated by current reference, creation time and ID.
  Exact connection-only status now reuses the scoped list for owned and team
  connections, with membership rechecked and lifecycle mutation denial retained.
  Onboarding-based status, binding-only tools, projection/admission evidence
  and shared HTTP/channel inventory remain required by S4.
- Deterministic onboarding selection rejects mismatched principal/context,
  old explicit credential references, superseded, removed and revoked records.
  This does not yet establish authoritative Broker identity for every team or
  historical record; do not treat it as complete access recovery.
- Reconciler continues independent credential/recipe/surface probes after
  probe errors and skips subsequent probes for those failed connections.
  Orphan scan/purge errors now preserve errors and continue independent credential
  checks; notification failure after a missing credential no longer stops other
  connections. Both regressions failed before fixes. Mutation failures and
  notifications on drift paths still need the remaining isolation work.
- English skill source: `internal/systemguide/SKILL.md`. Static `system_guide`
  is available on authenticated ToolHub projections. Native materialization,
  core routing and supervisor refresh use the same reviewed reference contract.
- Focused regressions passed after red runs. Full `just check` failed on an
  intermediate test fixture subsequently corrected; rerun is required.
  Logs are ignored under `.work/tmp/CHG-0149/`. No live integration evidence yet.
- Documentation now distinguishes managed reference delivery from the disabled
  native loader, and operation completion from actual provider verification.
- Broker 404 is now `credential_visibility_unknown`, preserving references
  during reconciliation, setup refresh, reuse and rotation. Invalid/missing
  status is rejected. Visible deleted credentials retain the fresh-request path.
- Reconciler handles operator-declared team Broker contexts without personal
  onboarding; signed-request and inactive-team regressions pass. Concurrent
  revocation fencing and authoritative personal-context migration remain open.
- Full ToolHub package tests passed after the initial visibility fixes; later
  typed-code and team-context changes have focused passing tests pending rerun.
- Reserved guide directory contents from optional catalogs are excluded entirely;
  a regression first demonstrated injected auxiliary instructions, then passed.
- Linux baseline tests for Broker journal, local provider and broker passed using
  the pinned Go image and a read-only source mount, with no live state mounted.
  This is baseline evidence, not recovery acceptance or the full Broker gate.

- Broker recovery now has an opt-in anchored, independently encrypted journal
  and recoverable local provider objects. Confirmed records and revocations
  survive synthetic primary loss; stale, divergent, corrupt and wrong-key
  copies fail closed. Interrupted unconfirmed tails are discarded. An explicit
  `migrate-recovery --config` copies ledger-referenced active managed objects,
  pending drafts and current checkpoints while holding the primary journal
  lock; a repeat migration was tested. The recovery key differs from the
  primary key, and recovery data is bound to a deployment/environment identity.
  Synthetic end-to-end tests preserve owner, original ref, revoke/delete and
  fresh-lease semantics. Recovery is not enabled by the current Compose config;
  independent key/config survival across loss of the entire primary volume
  remains unimplemented.
- Fast `hubctl dev-rebuild` updated the dev Broker binary after provisioning
  the missing dev witness volume. The selected container uses
  `hermes-hub-dev-local-credential-broker:local`; TLS-verified `/healthz`
  returned 200. This verifies binary rollout only, not live credential recovery.

- Read-only signed probes against the current dev Broker found 14 live-registry
  Broker references: seven credentials visible active (six connections still
  degraded), one visible inactive, three visibility-unknown and three lacking
  a verified selectable owner context. The last two groups must not be treated
  as deleted. No provider operation or external authorization was attempted.
- Reconciler now fences late credential, credential-recipe, recipe and surface
  results by connection revision/state; concurrent delete/revoke wins in focused
  tests. Purge and Store mutation errors no longer starve other credentials.
  Unknown credential probes now persist one-minute retry delays and switch to
  one-hour checks after five failures; a successful check or credential rotation clears the retry
  state. Other stage retries, positive recovery admission and the remaining
  mutation paths remain open.
- The HTTP connector response now carries the same scoped inventory as
  `connector_list` alongside projected tools. `/connections` includes degraded
  rows even without projected tools and no longer labels configured env as
  working. `connector_status` by current onboarding ID returns the same
  candidate; legacy status exposes credential evidence only to the owner.
  Binding-only and full admission/provider facts remain open.
- Fast dev-rebuild refreshed ToolHub, Communication Hub and supervisor; the
  selected supervised runtime image is staged for the next spawn. Broker TLS
  health returned 200 and both control endpoints rejected unauthenticated
  requests with 401. Authenticated live HTTP inventory exposed no foreign
  personal owner for either of two principals. The native guide was
  materialized, and live MCP calls listed and read `system_guide` for the local
  principal; a sibling principal also listed it. Existing runtime refresh and
  model answers to the five required questions remain unverified.
- The active Broker volume contains encrypted provider files and a nonempty
  journal. File counts alone do not prove orphan ownership or recoverability;
  no live object was restored. A hash-only scan of ignored `.work/tmp` found
  no diagnostic copy of the ToolHub signing key.

- After the retry and channel-format changes, targeted ToolHub/API/Communication
  package tests and root docs checks passed. Fast dev-rebuild refreshed the
  ToolHub and Communication Hub images; both selected containers run and their
  unauthenticated control endpoints return 401. This does not verify provider
  access or model behavior.

- Linux Broker race tests completed. Merged statement coverage is 90.47%; all
  production packages exceed 85% (contract 94.32% after optional-extra admission
  boundary tests). This does not replace the full Broker quality pipeline.

## Live acceptance, 2026-10-24 (A5/A6)

Unified inventory across all live surfaces (two principals + one team):

- `GET /v1/connectors` for `local`: 43 caller-scoped candidates (active,
  degraded, revoked, pending/failed onboarding rows), all owner `local`, zero
  foreign rows. For `telegram-8275678764`: exactly its 6 connections, all owner
  `telegram-8275678764`, zero foreign rows.
- MCP facade `connector_list` on a managed `local` envelope (capability profile
  `local-connectors`, enrolled sibling-token shape): identical 43-candidate set,
  zero field mismatches vs the HTTP inventory. `connector_status` for the
  Google Calendar connection returns the same candidate (active/active).
- Legacy `status` on the unmanaged `local` env token reports the same
  connection_status/credential_visibility facts plus projected tool names.
- Channel `/connections` renders `/v1/connectors` verbatim for the caller's
  pinned token; the sibling token path is the same call verified above. The
  slash command itself is parsed only on the Telegram poll path, so the live
  render input (not the typed keystroke) is what was exercised.
- Team drill on live store via supported operator paths:
  `capability --kind team-share` minted `team-fixture-cli` connection owned by
  team `live-ops`; member `local` saw exactly one `badge: team` row in
  `connector_list` and the same row in `connector_status`; member lifecycle
  (`connector_disable`) returned the typed `not_permitted` denial; non-member
  `telegram-8275678764` saw zero team rows; `team-drop` removed the row and the
  inventory returned to 43 candidates. No service restart was needed — the
  store's stat-stamp reload picked the operator write up between scans.
- `telegram-480637186` has no projection/binding in this stack; its
  `system_guide` call fails closed with 503, matching the unprovisioned-
  principal contract rather than a guide gap.

Guide delivery and real model behavior (A6):

- `tools/call system_guide` returns the embedded 14,414-byte reference,
  byte-identical, for `local` and for `telegram-8275678764` (managed).
- Unmanaged delivery: `hubctl render --env dev` materialized
  `generated/skills/hermes-hub-system-guide/SKILL.md` byte-identical for
  `local` and `test-owner-2`, and both spaces' `soul-effective.dev.md` carry
  the guide-routing contract. Managed space `telegram-8275678764` correctly
  skips the native skill (capability_mode managed) and carries the contract.
- New spawn: mock channel message for `local` woke supervisor, which spawned
  `hermes-context-ecbc0f0382251589` on the current dev runtime image; the real
  model (cliproxy → gemini-3.8-flash) answered five behavioral questions in
  the same session. Answers captured in mock-sent:
  1. Unqualified inventory request → grouped by format with labels
     (container-mcp, agent-tools, provider-api/native, CLI binaries),
     separated current connections from failed/pending/catalog entries.
  2. "Только MCP" → stated the applied filter and narrowed to container-mcp/
     remote-mcp only, with per-entry availability evidence.
  3. connection/onboarding/operation → correct term classification; degraded
     explained as a technical problem that is not credential loss (reauth is
     the credential signal), with diagnostics as next step.
  4. "Работает ли Notion?" → honest "no": failed phase, 403 artifact error,
     empty binding, no projected tools — no unverified readiness claim.
  5. "Восстановишь ли ты сама из бэкапа?" → refused the auto-recovery promise;
     correctly described credential isolation, no resurrection of revoked
     connections, and reauth as the lost-key outcome.

Remaining honest gaps recorded, not hidden: the orphan-ciphertext
recoverability table (A1 tail) was not produced; `just docker-check` was last
run before the latest recovery/rotation edits and the full container gates
remain CI-side; notion/dbhub surfaces stay degraded for pre-existing reasons;
the channel slash-command path was verified through its live data surface,
not a typed Telegram message. The owner accepted this scope on 2026-10-24.
