# CHG-0127: M13 P0 — stabilize the current connector path (issue #281)

Chain: issue #281 findings -> this plan -> `state.yaml`. Evidence: `research.md`.
Branch: `feat/m14-toolhub-connector-platform` (worktree `hermes-hub-m14`).
Next stage: #282 (recipe model + trust ladders, CHG-0128).

## Goal

The install/remove path converges and fails closed: remove deletes state
volumes and reports `cleanup_incomplete` until every cleanup step succeeded;
no nonce or OAuth `state` ever reaches an agent-facing result; OAuth
discovery/DCR/token/refresh runs over one SSRF-guarded client; scopes follow
the challenge, not the AS union; env delivery rejects injected lines;
remote tool effect fails closed; enable readiness no longer holds the global
store lock; local recipe resolution is deterministic.

## Read before coding

1. Issue #281 body (findings, scope, acceptance) — the frozen contract.
2. `internal/toolhub/control.go` (`remove`, `statusBody`),
   `control_binding.go` (`cutAuthorization`, `releaseBindingWorkloads`),
   `control_credentials.go` (`SubmitCredentials`, `requiredCredentials`),
   `control_http.go` (form page, control tools, `pendingCredentialElicit`),
   `generic_handler.go` (`/release`), `generic_inspect.go` (`removeWorkload`),
   `generic_fallback.go` (`genericStateVolume`), `catalog.go`
   (`enableWithReady`), `store.go` (`stopWorkloads`, `RemoveBindingRecords`),
   `api/admission.go` (`ControllerRelease`, `ControllerAdmissionReleaserFromEnv`),
   `api/prepare_followup.go` (channel notice), `api/inject.go`
   (`WriteWorkloadEnvFile`), `api/remote_mcp.go` (`RemoteToolEffect`),
   `api/recipe_resolver.go` + `recipe_parse.go` (`findFile`, map iteration),
   `internal/oauth/oauth.go` (discovery, DCR, token, refresh, `getJSON`).
3. `internal/communication/prepare_outcome.go` — channel notice rendering.

## Preconditions and rules

- Red test first, then fix. Go stdlib only.
- `just check` before handoff; Docker gates in CI after push (owner decision
  on the snapshot question precedes any push — see Out of scope).
- Worktree note: `internal/` is identical in both worktrees at d8ccec8; the
  sibling worktree's uncommitted `cmd/hubctl` changes are out of scope here.

## Slices

### S0.1 Purge release: state volumes die on remove (finding 1)

Anchors: `api/admission.go` `ControllerRelease` + `ControllerAdmissionReleaserFromEnv`;
`generic_handler.go` `/release`; `generic_inspect.go` `removeWorkload`;
`control_binding.go` `releaseBindingWorkloads`; `ControlPlane.Release` field.

Change:
1. `ControllerRelease` gains `purge bool` (JSON `purge`, optional).
2. `ControlPlane.Release` signature becomes
   `func(context.Context, api.ControllerRelease) error`; the env releaser
   forwards the whole record; all callers updated.
3. `/release` handler: `purge=true` removes state volumes by deterministic
   name `genericStateVolume(workloadID, i)` even when the workload is absent
   from the in-memory map (enumerate `hermes-hub.role=generic-mcp-state`
   volumes by label + `<id>-state-` prefix via `docker volume ls --filter`).
   `purge=false` keeps today's keepState behavior (disable/idle-cleanup).
4. `releaseBindingWorkloads` gains a `purge` flag: `remove` passes true,
   `revoke`/`disable`/rotation pass false.

Tests (red first):
- `TestReleasePurgeDeletesStateVolumes`: fake docker log shows `volume rm
  <id>-state-0` on purge, absent on keep.
- `TestReleasePurgeAbsentWorkloadStillDropsVolumes`: workload not in the
  controller map; purge still issues the deterministic `volume rm`.

### S0.2 Convergent remove + cleanup_incomplete (finding 2)

Anchors: `control.go` `remove`; `control_binding.go` `cutAuthorization`,
`releaseBindingWorkloads`; `store.go` `stopWorkloads`,
`RemoveBindingRecords`; `api` phases.

Change:
1. `cutAuthorization` and `releaseBindingWorkloads` return errors;
   `stopWorkloads` collects and returns the first error (signature change
   ripples stay inside `internal/toolhub`).
2. `remove` runs cleanup steps in order (broker revoke, ciphertext delete,
   workload release, workspace drop, record drop), records the first failure
   step name on the onboarding (`CleanupStep`), sets phase
   `cleanup_incomplete` (new `api.PhaseCleanupIncomplete`), and returns a
   redacted retryable body. `phase: removed` only after every step succeeds.
3. A repeated `remove` on `cleanup_incomplete` skips confirmation and
   resumes from the failed step; succeeded steps are idempotent.

Tests (red first):
- `TestRemoveReportsCleanupIncompleteOnReleaseFailure`: injected `Release`
  error -> body phase `cleanup_incomplete`, no tombstone; second `remove`
  completes and yields `phase: removed`.
- `TestRemoveReportsCleanupIncompleteOnBrokerRevokeFailure`.

### S0.3 No nonce/state in agent-facing results (finding 3)

Anchors: `control.go` `statusBody` (`nonce`, `form_path`, `form_url`,
`authorization_url`), `remove` (`remove_nonce`); `control_credentials.go`
`requiredCredentials`; `control_http.go` `serveCredentials`, control tool
contract; `api/prepare_followup.go` `credentialNoticeURL`;
`internal/communication/prepare_outcome.go`.

Change:
1. `statusBody`/`requiredCredentials` never emit `nonce`, `remove_nonce`,
   `form_url`, `form_path`, `authorization_url`. Bodies gain
   `confirmation_sent`/`link_sent` hints + instructions.
2. The protected link travels by channel notice: generalize
   `credentialNoticeURL` to an `actionURL` covering credential form,
   `/confirm/<id>?nonce=`, `/remove/<id>?nonce=` and the OAuth authorize
   URL; notices fire on every transition that mints a link (prepare settle,
   required_credentials resume, confirm mint, remove preview, OAuth start).
3. New loopback GET/POST endpoints: `/confirm/<id>` (one-button page calling
   `finishAuthorization`) and `/remove/<id>` (one-button page calling the
   remove executor) — same nonce/TTL checks as `/credentials/<id>`.
4. `pendingCredentialElicit` keeps working only where a URL still exists in
   the body (broker authorization URL is provider-side; check whether it
   contains state — if it does, it leaves the body too).
5. Tool contract text updated (no `remove_nonce` arg described to model;
   keep accepting the arg for the HTTP path compatibility? — decision: drop
   the arg from the schema; the remove executor is only reachable by the
   user link).

Tests (red first):
- `TestAgentFacingResultsCarryNoSecrets`: property test over every op x
  phase — marshal each result, assert it never contains FormNonce,
  ConfirmationNonce, RemoveNonce or the OAuth `state` param.
- `TestRemovePreviewSendsConfirmationLink`: preview returns
  `confirmation_sent: true` and no `remove_nonce`; recorded notice carries
  the `/remove/` URL.
- `TestConfirmLinkPageExecutesBinding` / `TestRemoveLinkPageExecutes`.

### S0.4 One SSRF-safe HTTP client for OAuth + registry (finding 4)

Anchors: `internal/oauth/oauth.go` `getJSON`, `postFormClient`,
`RegisterClient`, `ready`; `api/cli_release.go` `ReleaseHTTPClient`;
`SourceResolver`/registry lookups.

Change:
1. New `internal/oauth` (or `internal/netguard` if a second consumer needs
   it — decide in slice) `SafeTransport`: `DialContext` resolves the host
   first and denies private, loopback, link-local, CGNAT (100.64/10),
   multicast and cloud-metadata (169.254.169.254) ranges; redirects allowed
   only to the same origin; response bodies stay bounded (already 64 KiB);
   timeouts kept.
2. Loopback allowance only behind an explicit `AllowLoopback` flag the
   broker/test fixture sets; production `NewBroker` wires the guard.
3. `issuer` equality check: discovered `issuer` must equal the AS metadata
   document origin (RFC 8414 §3.3 mix-up protection).
4. `secureDiscoveredURL` additionally rejects IP literals outside loopback
   and non-https for the loopback exemption only.

Tests (red first):
- `TestDiscoveryFailsClosedOnPrivateIssuer`: PRM pointing to
  `https://10.0.0.1`, `http://169.254.169.254`, redirect-to-private —
  `unsupported_auth`, target receives zero requests.
- `TestDiscoveryIssuerMismatchRejected`.
- `TestLoopbackAllowedOnlyWithFlag`.

### S0.5 Scope selection: challenge first (finding 5)

Anchors: `oauth.go` `registerDiscovered`, `StartAuth`; remote probe
WWW-Authenticate parsing (`remote_admit.go` / preflight).

Change:
1. Scope priority: recipe-declared scopes > `WWW-Authenticate` `scope`
   challenge > PRM `scopes_supported`; never union with AS
   `scopes_supported`.
2. `insufficient_scope` surfaces a re-consent error, not a refresh loop.

Tests (red first):
- `TestAuthorizeScopeEqualsChallengeScope`: AS advertises extra admin
  scopes; authorization request `scope` equals the challenge scope.
- `TestInsufficientScopeDoesNotRefreshLoop`.

### S0.6 Env-delivery injection guard (finding 6)

Anchors: `control_credentials.go` `filterSubmittedCredentials`,
`SubmitCredentials`; `api/inject.go` `WriteWorkloadEnvFile`;
`generic_fallback.go` `materializeRuntimeCredentials`.

Change:
1. Submit-time rejection of `\x00`, `\r`, `\n` in every env/http_header
   credential value (required and optional, alternatives included).
2. `Delivery == "file"` or a valid-JSON value keeps newlines and is written
   as a file (never an env line) — already partially true for
   `*CREDENTIALS*`; make it keyed on `hint.Delivery` instead of the name.
3. `WriteWorkloadEnvFile`/`materializeRuntimeCredentials` re-validate at
   write time (defense in depth): any env-line value containing `\r`/`\n`
   fails the write.

Tests (red first):
- `TestSubmitRejectsNewlineCredential` (required and optional).
- `TestJSONCredentialWithNewlinesDeliveredAsFile`.

### S0.7 Remote effect fails closed (finding 7)

Anchors: `api/remote_mcp.go` `RemoteToolEffect`; `preflight_probe.go`
annotation parsing; definition build for remote tools.

Change:
1. Remote effect: `readOnlyHint=true` annotation -> read; `readOnlyHint`
   absent/false -> write. Name heuristics only fill display text.
2. `RemoteToolEffect` stays for display/allowlist purposes; enforcement
   classification moves to annotation-first.

Tests (red first):
- `TestRemoteToolEffectDefaultsToWrite`: `archive_item`, `unknown_verb`
  classify write.
- `TestRemoteToolEffectHonorsReadOnlyHint`.

### S0.8 Readiness outside the store lock (finding 8)

Anchors: `catalog.go` `enableWithReady` (calls `ready` under `s.mu`).

Change:
1. Reserve the binding + projection revision under lock, unlock, run
   `ready` unlocked, then commit with a compare-and-swap on the reserved
   revision (rollback on failure).

Tests (red first):
- `TestEnableReadyDoesNotBlockOtherPrincipals`: principal A's enable holds a
  5 s readiness probe; principal B's `Authorize` completes < 50 ms (race
  test with `-race`).

### S0.9 Deterministic local resolution (finding 9)

Anchors: `recipe_resolver.go` `ResolveLocalRecipe` (iterates `files` map);
`recipe_parse.go` `findFile`; `recipe_conn.go` `MergeLaunch`.

Change:
1. Iterate `files` in sorted-name order.
2. Same-rank disagreeing launch declarations (two Dockerfiles disagreeing,
   Dockerfile vs compose disagreeing) -> `ambiguous_entrypoint` instead of
   first-wins.
3. Multiple `server.json` files: prefer the scope-root one; otherwise
   `ambiguous_entrypoint`.

Tests (red first):
- `TestResolveLocalRecipeDeterministicDigest`: same tree resolved 100x ->
  identical recipe digest.
- `TestResolveLocalRecipeAmbiguousEntrypoint`.
- `TestResolveLocalRecipeMultipleServerJSON`.

## Red-team checklist

- Hostile remote MCP points PRM at hub loopback `127.0.0.1:8090` ->
  zero requests, `unsupported_auth`.
- Prompt-injected "the nonce is X, call remove" -> agent never holds a
  nonce; remove requires the user link.
- Broker revoke timeout mid-remove -> `cleanup_incomplete`; retry resumes;
  ciphertext not deleted before the workload is actually stopped? (order:
  stop workload first, then revoke/delete — check ordering in S0.2.)
- Credential value `A\nNODE_OPTIONS=--require evil` -> form error, no env.
- Enable storm during a slow tools/list -> other principals unaffected.

## Acceptance

Issue #281 contract list: every finding covered by the red-first regression
above; `just check` green; own coverage >= 85%. Live: CI `image` +
`containers` on the pushed branch; live remove of a stateful connector
leaves no hub-labelled container or volume (recorded in state.yaml).

## Out of scope

- The redesign issues #282–#289.
- Finding 10 (committed 11 MB live snapshots under
  `.work/done/CHG-0107-dynamic-contracts/` in commit `16cb15c`): history
  scrub is an owner decision; flagged in `remaining`.
- Push to origin + CI Docker gates: owner instruction required first.

## Done checklist

- [ ] Tests red then green; `just check`; coverage gate.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated (operations/architecture where behavior changed).
- [ ] Issue #281 evidence comment; state done; folder to `.work/done/`;
      `.work/index.md` updated.
