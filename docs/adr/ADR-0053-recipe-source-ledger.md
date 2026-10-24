---
description: Connector recipes are content-addressed and secret-free; launch and credential claims resolve over ranked source ledgers with fail-closed ambiguity; onboardings reference recipes by digest.
last_verified: 2026-10-19
---
# ADR-0053: Recipe and source ledger — ranked claims, content addressing, fail-closed ambiguity

Status: Accepted 2026-10-19. Implements GitHub issue #282 (M13 P1).
Spec: [SPEC-0060](../../specs/active/SPEC-0060-recipe-source-ledger.md).

## Context

Connector resolution previously merged repository files and catalog
responses procedurally: the first parse won launch attributes, credential
fields merged by name, and disagreements surfaced only as warning text that
heuristically forced `state=review`. The result was order-dependent,
unreproducible, and could not explain to a user *why* a field was required
or which source won. Onboardings embedded the full resolution — every
snapshot duplicated the same recipe per onboarding.

## Decision

- **D1**: A `Recipe` is a content-addressed, secret-free, shareable object:
  `subject` (kind `mcp-container`/`mcp-remote`/`cli` + pinned ref), launch
  recipe, credential contract, surface, egress, state and a derived trust
  tier. `Recipe.Digest()` canonicalizes order-insensitive slices before
  sha256, so identical claim sets always yield identical digests.
- **D2**: Every external assertion is a `Claim` inside a `LedgerEntry`
  (source, ref, ladder rank, verification, response digest, fetched_at).
  `ResolveLedger` is a pure function over the ledger — no network, no
  clock. Ignored candidates stay in the ledger flagged `ignored` so a
  re-resolution can be replayed and audited without refetching.
- **D3**: Ranked ladders order claim strength. Launch: L0 hub catalog → L1
  operator-approved → L2 curated catalogs with built/pinned images → L3
  official MCP Registry → L4 pinned repo machine-readable metadata → L5
  repo-published OCI → L6 language-convention → hint. Credentials: R0 hub →
  R1 operator → R2 registry → R3 MCPB/`server.json` → R4 authoritative
  (ToolHive/PRM) → R6/R7 heuristics/owner words. The highest rank wins each
  attribute and field; **two disagreeing same-rank claims fail closed** —
  `ambiguous_entrypoint` / `ambiguous_credential_contract` — and an R1/L1
  override resolves the tie explicitly.
- **D4**: Lower ranks may add optional fields but can never strengthen
  requiredness. Heuristic ranks (R6+) demote `required` to an unverified
  suggestion surfaced as `Unverified` on the field — the credential form
  renders these as suggestions, not contract.
- **D5**: Trust derives from the weakest winning ranks: T0 iff launch and
  credentials are L0/R0-only; T1 iff launch ≤L3 and credentials ≤R4;
  otherwise T2.
- **D6**: Catalogs are consulted *before* repository files with a
  source-only lookup — repo-derived hints can never steer which catalog
  record wins. Warning-substring conflict detection is removed: state flows
  from resolution outcomes, not text matching.
- **D7**: The store persists resolutions once under `recipes/<sha256>`;
  `Onboarding` carries `recipe_digest` and never embeds the resolution or
  an already-registered definition copy (credential-pending drafts keep
  theirs, nothing else can rebuild it). `credential_source` replay records
  owner-words (R7) ledger entries naming the confirmed source.

## Consequences

Re-resolution of a recorded ledger is deterministic and offline; the golden
corpus (21 real targets) pins digests across 100 shuffled-entry runs.
Consumers of `Resolution.Ledger` can render "required by: official MCP
Registry" vs "suggested by `.env.example`". Snapshots shrink by one copy of
the shared recipe/definition payload per onboarding.
