# CHG-0128: M13 P1 — recipe model with trust ladders and source ledger (issue #282)

Chain: M13 milestone. Previous stage: `CHG-0127` (P0 stabilization). Next: #283 channel-agnostic human action surface (parallel-priority P2, done after P1 per milestone order).

## Goal

Make connector recipes deterministic, explainable and reusable: launch and
credential layers resolved from ranked source ladders, every claim recorded in
a source ledger, recipes content-addressed by sha256 and referenced from
onboardings/connections by digest instead of an embedded `ToolDefinition`
copy.

## Read before coding

1. `internal/toolhub/api/recipe.go`, `recipe_resolver.go`, `recipe_conn.go`,
   `recipe_parse.go`, `recipe_launch.go`
2. `internal/toolhub/api/catalog_search.go`, `catalog_registry.go`,
   `catalog_oci.go`, `prepared/catalog.json`
3. `internal/toolhub/api/grants.go` (`Onboarding`), `control*.go` onboarding
   consumers
4. Issue #282 (rank tables L0–L6/hint, R0–R7+PRM), ADR-0034 (allowlist)

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only.
- `just check` before handoff.
- Recipes never contain secrets or user data; connections never contain a
  build recipe. Recipe digest changes → connection stays pinned until accepted.

## Slices

### S1.1 Ledger and recipe types + canonical digest

Anchors: `api/recipe.go` types; new `api/ledger.go`.

Change:
1. `LedgerEntry{Claim, Source, Ref, Rank, Verified, Digest, FetchedAt}`,
   `LaunchClaim{Attribute, Value, ...}`, `CredentialClaim{Field, Required,
   Secret, Delivery, Target}`, `Recipe{Subject, Launch, Credentials, Surface,
   Egress, State, Shareable, Trust}` types.
2. Canonical JSON encoding (sorted keys/fields) → `RecipeDigest` sha256.
   Same entries in any order → same digest.

Tests (red first):
- `TestLedgerEntryCanonicalDigest`: entry set in shuffled order → identical
  digest; one-byte mutation → different digest.
- `TestRecipeContainsNoSecrets`: validator rejects a recipe carrying a
  literal-looking token value in any field.

### S1.2 Rank vocabulary

Anchors: `api/ledger.go`; `catalog_search.go`/`catalog_registry.go`/
`catalog_oci.go` candidate producers.

Change:
1. Rank constants `L0..L6, LHint` and `R0..R7` (+PRM oauth method rank);
   `ErrAmbiguousCredentialContract` sentinel.
2. Every existing producer annotates its claims: prepared catalog=L0/R0,
   operator-approved=L1/R1, Docker mcp registry+ToolHive=L2/R4 respectively
   per ladder, MCP registry=L3/R2, server.json/MCPB=L4/R3, OCI image=L5,
   language fallback=L6, Dockerfile/compose/README/.env.example/--help=
   hint/R6, owner words=R7.

Tests:
- `TestProducerRanks`: a resolved resolution's claims carry the expected rank
  per source.

### S1.3 Pure ledger resolution

Anchors: `api/ledger.go` `ResolveLedger(entries) (Recipe, error)` — pure
function, no network.

Change:
1. Highest rank wins per field/attribute; equal-rank disagreement →
   `ambiguous_entrypoint` / `ambiguous_credential_contract` with candidates.
2. Lower rank may only add optional fields — never make required, never
   change delivery/target, never drop secret.
3. R6-only contract → fields marked `unverified` (T2 only).
4. Trust: T0 iff launch L0 and credentials R0 only; T1 iff launch ≤L3 and
   credentials ≤R4; else T2.
5. Ignored candidates (CandidateMatches fail) recorded as ignored ledger
   entries.

Tests:
- `TestLedgerR2OverridesR6`, `TestLedgerSameRankAmbiguous`,
  `TestLedgerR1ResolvesAmbiguity`, `TestLedgerLowerRankCannotRequire`,
  `TestLedgerR6OnlyUnverified`, `TestLedgerTrustTiers`,
  `TestLedgerIgnoredCandidateRecorded`, `TestResolveLedgerIsDeterministic`
  (100 shuffled-entry orders → same digest).

### S1.4 Registry-first resolution wiring

Anchors: `RecipeResolver.Resolve`, `ResolveLocalRecipe`, `LookupCatalogs`.

Change:
1. `Resolve` queries MCP Registry/ToolHive/Docker catalogs first; repository
   files only contribute L4/L6/R3/R6 claims — never consulted before the
   catalog answer exists in the ledger.
2. Each fetched response lands in the ledger with `digest` + `fetched_at`.
3. Warning-substring conflict detection removed; state flows from ledger
   outcome (`ambiguous_*`, `unverified`).

Tests:
- `TestResolveQueriesRegistriesBeforeRepoFiles`: fake catalog records a call
  ordering marker; repo-file reader records second.
- `TestResolveLedgerPersistsFetches`.

### S1.5 Content-addressed recipe store + onboarding decoupling

Anchors: `Store` (`recipes/<sha256>.json` persistence); `Onboarding.Definition`
embed removal; `grants.go`.

Change:
1. `PutRecipe`/`Recipe(digest)` on Store; resolution result cached by input
   digest.
2. Onboarding/operation records carry `recipe_digest`; the per-onboarding
   `ToolDefinition` copy is removed.

Tests:
- `TestOnboardingReferencesRecipeDigest`: stored onboarding has no embedded
  definition; `Recipe(digest)` resolves.
- `TestOnboardingFixtureShrinks`: synthetic 200-onboarding fixture is
  smaller than the pre-change baseline.

### S1.6 credential_source as ledger entry

Anchors: `control_credentials.go`, `control.go` arg handling.

Change:
1. `credential_source` argument becomes an R1 (operator-confirmed) or R7
   (owner words) ledger entry instead of an agent choice between sources.

Tests:
- `TestCredentialSourceRecordsLedgerEntry`.

### S1.7 Golden corpus

Change:
- Snapshot corpus ≥20 real targets (repo files + registry responses):
  `github/github-mcp-server` (aux `cmd/mcpcurl`), `nspady/google-calendar-mcp`
  (file credential), `makenotion/notion-mcp-server`, `sooperset/mcp-atlassian`,
  `zereight/gitlab-mcp`, one remote-only registry entry, one MCPB bundle,
  one Docker `mcp/*` image, three CLIs, remainder misc. Each pins its expected
  recipe digest; 100 runs → identical.

Tests: `TestGoldenCorpus`.

### S1.8 Docs

- ADR "Recipe and source ledger" appended; SPEC delta;
  `docs/prepared-connectors.md` updated.

## Red-team checklist

- Forged catalog claims a wrong repository/commit → `CandidateMatches`
  rejects, recorded as ignored ledger entry, never influences the recipe.
- Two same-rank registry claims disagree → `ambiguous_*`, not first-wins.
- A low-rank file claims a credential `required` that the registry declared
  optional → stays optional; cannot escalate.
- Recipe digest collision check: inputs differing in any claim → different
  digest (canon encoding).
- Ledger replay: same ledger, no network → same recipe.

## Acceptance

Issue #282 contract list: types exist, resolver is a pure function over the
ledger, corpus ≥20 with pinned digests, R2>R6 override, same-rank ambiguity
typed, R1 resolves, ignored candidates recorded, onboarding carries a digest
not a definition, docs updated. `just check` green; own coverage ≥85%.
Live: `connector_install github-mcp-server` resolves registry/catalog-first
and requests only `GITHUB_PERSONAL_ACCESS_TOKEN` (recorded in state.yaml).

## Out of scope

- CI-built T0 images (#286), Credential Broker authority (#287), team
  connectors (#288), browser workloads (#289), nightly drift job scheduling.
- Push/publish — owner instruction required.
