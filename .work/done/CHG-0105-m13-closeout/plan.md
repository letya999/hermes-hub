# CHG-0105: M13 ToolHub connector platform closeout (M13 final stage)

Chain: milestone 14 issues #104, #105, #106, #133 (issues #138, #150, #151 audited
complete or out of scope) -> this plan -> `state.yaml`.
Previous stage: `CHG-0037-toolhub-closeout`, `CHG-0040-shared-toolhub-control-plane`,
`CHG-0075-remote-mcp`, `CHG-0077-toolhub-cli`.

## Goal

Finish the conversational connector lifecycle: a user can install a connector
from a link without guessing, authorize it with file credentials or OAuth,
disable/revoke/remove it by friendly name with an explicit confirmation step,
and share a single workload across an authorized member set. Anything the
platform cannot verify fails closed with a structured error code the agent can
explain in chat — never a guessed runtime or a false "enabled".

## Read before coding

1. `internal/toolhub/api/definition.go` — workload/credential validation
2. `internal/toolhub/api/recipe_launch.go`, `artifact_language.go` — entrypoint resolution
3. `internal/toolhub/control.go`, `control_binding.go`, `control_http.go` — lifecycle + form surface
4. `internal/toolhub/catalog.go` — `matchingOwnerConnectionsLocked`, `Enable`, `Disable`, `resolveLocked`
5. `internal/toolhub/generic_fallback.go` — `materializeRuntimeCredentials`
6. `internal/toolhub/prepared_oauth.go`, `internal/oauth/providers.go` — OAuth flow
7. `internal/toolhub/api/grants.go` — `SharedCredentialPolicy`
8. `internal/credstore/credstore.go` — `Delete` for ciphertext cleanup

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates when image/runtime code changes.
- No secrets in projections, URLs, logs, audit bodies, or command arguments.
- Records are immutable within a revision; removal deletes connection/binding/
  credential records but leaves a scrubbed onboarding tombstone for idempotent
  status replay (retention contract: metadata kept, ciphertext gone).

## Slices

### S1.1 Structured resolution errors (#106)

Anchors: `api/artifact_language.go` `GenerateArtifactRecipe`;
`api/recipe_launch.go` `selectComposeService`; `control_http.go` `statusBody`.

Change:
1. Add `api.ErrAmbiguousEntrypoint`, `api.ErrUnsupportedCredentialContract`,
   `api.ErrUnsupportedAuth` sentinels; wrap existing ambiguity failures.
2. `api.ErrorCode(err)` maps sentinels to stable snake-case codes.
3. `statusBody` surfaces `error_code` when onboarding.Error unwraps to one.
4. Pin precedence doc-comment on `ResolveLocalRecipe` (server.json / mcpb
   manifest > Dockerfile/compose > single conventional executable; else fail).

Tests (red first):
- `TestRecipeResolutionSurfacesStructuredErrorCodes`: ambiguous tree produces
  `ambiguous_entrypoint` in status body; unsupported credential contract
  produces `unsupported_credential_contract`.

### S1.2 Lifecycle UX (#104)

Anchors: `control_binding.go` `resolveOnboarding`, `remove`, `cutAuthorization`;
`catalog.go` `Store.Disable`; `store_queries.go`.

Change:
1. `selector`/`connector`/`name`/`source` args resolved against the caller's
   non-removed onboardings (definition ID, repository URL, repo basename);
   ambiguity lists candidates, never guesses.
2. Two-phase `remove`: first call marks a removal nonce and returns the exact
   deletion inventory + `next_action: confirm-remove`; `confirmation=<nonce>`
   executes. `request_key` makes replays return the same outcome.
3. Hard cleanup on confirmed remove: `Secrets.Delete` ciphertext, drop
   connection/credential-ref/binding rows not shared with other bindings,
   `RemoveBindingWorkspace`, release workloads, invalidate pending links
   (form/oauth nonces, prepared flow), keep a scrubbed `PhaseRemoved`
   tombstone so `status`/`remove` replay idempotently.
4. `status` accepts `operation_id` (= onboarding id of the lifecycle op).

Tests (red first):
- `TestRemoveByNameRequiresConfirmationAndCleansEverything`: name selector,
  inventory preview, nonce-gated delete of credential ciphertext + records,
  replay returns same tombstone.
- `TestRemoveSelectorAmbiguityListsCandidates`.
- `TestLifecycleRequestKeyReplayReturnsIdenticalOutcome`.

### S1.3 Generic file credentials, durable state, OAuth fail-closed (#105)

Anchors: `api/definition.go` `CredentialInput.Validate`; `control_http.go`
`serveCredentials`; `generic_fallback.go` `materializeRuntimeCredentials`;
`prepared_oauth.go` `ensurePreparedOAuth`.

Change:
1. `CredentialInput.Delivery` gains `"file"` for container transports; `Target`
   must be an absolute in-container path under a reviewed prefix
   (`/run/...`, `/state/...`); read-only mount.
2. Form renders `delivery: file` hints as file inputs for any connector (not
   just `GOOGLE_OAUTH_CREDENTIALS`); uploads bounded and stored via credstore.
3. `materializeRuntimeCredentials` honors declared file targets; falls back to
   the existing `*_CREDENTIALS` heuristic only when no file input declared.
4. Stateful defs already mount `connection-state`; generic OAuth stays
   prepared/official-provider only — a recipe that requires auth the platform
   cannot honor fails closed with `unsupported_auth` (per issue fail-closed
   clause), surfaced via S1.1 codes.

Tests (red first):
- `TestGenericFileCredentialRendersUploadAndMountsAtDeclaredTarget`.
- `TestGenericOAuthRequiredFailsClosedUnsupportedAuth`.

### S1.4 Shared connector bindings (#133)

Anchors: `catalog.go` `matchingOwnerConnectionsLocked`, `resolveLocked`;
`control_binding.go` `materializeBinding`; `api/grants.go`
`SharedCredentialPolicy`; `api/definition.go` shared-class validation.

Change:
1. `ToolBinding.Scope` (`""`=owner, `"shared"`); member bindings point at the
   shared connection owned by `SharedCredentialPolicy.StoreOwner`; binding ID
   stays per-principal (audit), workload ID stays shared → exactly one running
   workload.
2. `resolveLocked`/`matchingOwnerConnectionsLocked` accept a store-owner
   connection when an active policy covers (definition, caller principal);
   per-call authorization unchanged otherwise.
3. Lifecycle: only policy issuer / store owner may rotate/revoke the shared
   credential or remove the shared binding's connection; member `disable`
   releases only their projection view.
4. `Shared` class may carry non-per-request credentials only when the
   definition is marked `Sharable` — keep the default closed.

Tests (red first):
- `TestSharedMembersResolveOneWorkload` (5 members → 1 workload ID, audit per
  caller).
- `TestSharedMemberCannotRotateOrRevokeSharedCredential`.
- `TestSharedClassWithoutShareableDefinitionStillRejectsStaticCreds`.

## Red-team checklist

- Friendly-name selector must never resolve another principal's onboarding;
  ambiguity is an error with a candidate list, not a guess.
- Confirmed remove between two principals sharing a connection must not
  delete the shared ciphertext while the other binding still references it.
- File credential targets escape `/run/...` or `/state/...` → rejected;
  upload size bounded; values never land in projections or argv.
- Shared member with a revoked policy loses access without error beyond the
  documented membership error; in-flight calls keep a stable outcome.
- `request_key` replay after a successful remove must not re-run cleanup.

## Acceptance

- `just check` green (format, vet, staticcheck, race/shuffle, coverage ≥85%).
- Focused regressions above red→green.
- Issue acceptance bullets mapped in `state.yaml`; live/Docker acceptance
  deferred to CI per repo convention (reported, not claimed).

## Out of scope

- #138 browser-as-workload (idea only per issue).
- #150/#151 already shipped (audit confirmed).
- New OAuth providers, marketplace search changes, UI work beyond the
  credential form.
- Hermes upstream changes.

## Done checklist

- [x] Tests red then green; `just check` green (85.26% coverage); Docker gates deferred to CI (no image-side changes).
- [x] Red-team and live acceptance recorded in state.yaml.
- [x] Docs updated (SKILL.md, prepared-connectors.md, architecture.md, SPEC-0023 delta).
- [x] Issue work complete on `feat/m13-closeout`; state done; folder moved to `.work/done/`. Issue #104/#105/#106/#133 closure on GitHub left to the owner.
