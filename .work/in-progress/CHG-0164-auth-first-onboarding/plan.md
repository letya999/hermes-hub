# CHG-0164: Auth-first onboarding completion (issue #293)

Chain: `docs/adr/ADR-0076-auth-first-admission-contract.md` -> this plan ->
`state.yaml`. Evidence: `research.md`. Continues CHG-0148's source-to-form
route and the acceptance items recorded in CHG-0147 item 3. GitHub issue #293.

## Goal

Finish the generic auth-first route so any declaratively declared
"authenticate before tools/list" MCP reaches the protected Broker form before
artifact execution, retries credential entry without rebuilding an unchanged
artifact, and classifies interactive-terminal/browser-profile servers as
explicitly unsupported instead of minting a fake form. No provider-name
branches.

## Read before coding

1. `docs/prepared-connectors.md` (auth-first section), ADR-0067, ADR-0070,
   `internal/toolhub/api/preflight.go`, `internal/toolhub/api/control_review.go`,
   `internal/toolhub/broker.go`, `internal/toolhub/control_broker.go`,
   `internal/toolhub/api/recipe.go`, `internal/toolhub/api/recipe_conn.go`.
2. Issue #293 body: split form-prep / build / authorized run; honest
   form_ready/artifact_ready/tools_confirmed; retry without rebuild;
   interactive servers classified unsupported; no secrets in build/argv/logs.

## Preconditions and rules

- Red test first, then fix. Go stdlib only.
- `just check` before handoff. Docker gates are explicitly deferred this
  session (owner instruction) — live acceptance stays open.
- No secrets, links, provider data or personal logs in records.

## Slices

### S1 `interactive` recipe marker -> unsupported_auth (R: classify interactive)

Anchors: `api/recipe.go` `ConnectionRecipe`; `api/recipe_conn.go`
`mergeConnection`/`FilterConnectionSource`; `api/control_review.go`
`DefaultSourceReviewerWithCatalogs`; `internal/toolhub/connector_journal.go`
`connectorInstall`.

Change:
1. `ConnectionRecipe.Interactive bool` json `"interactive,omitempty"`.
2. `mergeConnection`: OR-propagate; `FilterConnectionSource`: preserve.
3. Reviewer: after recipe resolution (+ prepared overlay), before the
   auth-first draft return and before any build/pull: `Interactive` ->
   `ErrUnsupportedAuth` wrapped detail. Same one-liner in
   `admitPinnedPrepared`.
4. `admitWithBrokerCredential` deferred recipe copy clears `Interactive`
   symmetric to `AuthBeforeToolsList`.
5. `connectorInstall`: `ErrUnsupportedAuth` -> `OpCodeUnsupportedAuth`
   instead of blanket `probe_failed` (both the prepare-failure and the
   PhaseFailed re-observe paths).

Tests (red first):
- `TestInteractiveRecipeClassifiesUnsupported` (api): recipe parses/validates,
   merge+filter propagate, reviewer errors `ErrUnsupportedAuth` with zero
   builds/fetches after resolution.
- `TestConnectorInstallInteractiveIsUnsupportedAuth` (toolhub): install with
   `connection_recipe {"interactive":true}` fails with code
   `unsupported_auth`, no form fields minted.

### S2 Retry without rebuild (R: retry preserves unchanged artifact)

Anchors: `api/control_review.go` reviewer; `internal/toolhub/broker.go`
`admitWithBrokerCredential`; `internal/toolhub/control_broker.go`
`completeBrokerAdmission`.

Change:
1. Reviewer returns `SourceReview{Definition: <built def>, Recipe}` also on
   non-gate preflight failure (imported and published paths) — evidence only;
   callers still see `err`.
2. `admitWithBrokerCredential` returns the partial `admitted` on error so the
   caller can keep built evidence.
3. `completeBrokerAdmission` failure branch: when `admitted.Source.Digest`
   is set, persist the stripped built def (Tools=nil, ToolContract* cleared)
   as the onboarding draft. Retry then takes the `Digest != ""` branch ->
   `AdmitWithCredentials` -> CAS load + credentialed tools/list only.

Tests (red first):
- `TestAuthFirstRetrySkipsReviewerAfterBuiltProbeFailure` (toolhub): first
   admission fails after a built artifact; retry admits via
   `AdmitWithCredentials` with the reviewer spy never called; stored draft
   carries Source.Digest but Tools stays empty.

### S3 Coverage close-out

Tests (red first):
- `TestAuthFirstConcurrentOwnersAdmitIndependently` (toolhub): two principals
   admit the same auth-first draft concurrently; separate grants, both
   converge to AwaitingConfirm, no cross-talk.
- Verify existing coverage already pins: canceled/expired link healing
   (`broker_stale_expired_test.go`), wrong creds rejection, crash not masked
   as auth (`credential_gate_test.go`), secrets out of argv/build context.
   Add only what is missing.

### S4 Docs and records

1. `config/skills/mcp-connector-onboarding/SKILL.md`: one line — auth-first
   connectors may show a ready form before the artifact exists;
   `unsupported_auth` covers declared interactive servers.
2. `docs/prepared-connectors.md`: `interactive` marker + retry-reuse of the
   unchanged artifact; bump `last_verified`.
3. ADR-0076 + index rows (.work, docs/adr).

## Red-team checklist

- `interactive` declared but a form still opens -> must not happen (reviewer
  rejects before draft/form).
- Probe failure after build persists a draft that could masquerade as
  admitted -> Tools/ToolContractDigest stripped; `tools_confirmed` stays
  false; registration never happens.
- Persisted built def lets a retry skip review of a *changed* source ->
  source is pinned by CommitSHA on the onboarding; recipe edits create a new
  onboarding generation, so reuse is bounded to the identical artifact.
- Owner A's failed admission leaks into owner B's flow -> draft is embedded
  on the owner's own onboarding record only.
- `interactive` via user-supplied recipe vs prepared/blocked catalog ->
  both covered; blocked.json stays the catalog-level unsupported list.

## Acceptance

- A declared auth-first server reaches form before build; after credentials,
  real tools/list; a second credential entry does not rebuild.
- `interactive` recipes produce `unsupported_auth` in both APIs.
- `just check` green; focused -race on toolhub admission paths.
- Docker/live-MCP acceptance: deferred by owner instruction — recorded
  honestly as not run.

## Out of scope

- Live real-MCP proof (needs owner instruction + account login anyway).
- Docker image gates (owner instruction this session).
- Interactive-server support itself (browser profile/TTY auth) — explicitly
  classified unsupported for now.
- Private-registry/private-source access during build.
