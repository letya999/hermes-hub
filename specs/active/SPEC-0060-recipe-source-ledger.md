# SPEC-0060: Connector recipe model and source ledger

Frozen: 2026-10-19.
Decision: [ADR-0046](../../docs/adr/ADR-0046-recipe-source-ledger.md).
Implements GitHub issue #282 (M13 P1).

## Requirements

- R1: `Recipe{subject, launch, credentials, surface, egress, state,
  shareable, trust}` is secret-free and content-addressed; `Digest()` is a
  canonicalized sha256 over sorted order-insensitive data. Recipes carrying
  credential material are rejected by `Validate`.
- R2: `LedgerEntry{claim, source, ref, rank, verified, ignored, digest,
  fetched_at}` records every consulted claim including ignored/mismatched
  candidates.
- R3: `ResolveLedger(subject, entries)` is pure: no network, no clock.
  Highest rank wins per launch attribute and credential field; equal-rank
  disagreement fails closed with `ambiguous_entrypoint` /
  `ambiguous_credential_contract`; a stronger override (L1/R1) resolves the
  tie and is recorded.
- R4: Lower-rank sources may only add optional fields; R6+ ranks demote
  `required` to `unverified` suggestions.
- R5: Trust tiers: T0 = L0/R0 only; T1 = launch ≤L3 and credentials ≤R4;
  else T2.
- R6: `RecipeResolver.Resolve` queries catalogs first with a source-only
  `RecipeLookup`; repository files never feed image/version hints into the
  catalog lookup. Each fetched response lands in `Resolution.Ledger` with
  digest + fetched_at; mismatched candidates are recorded `ignored`.
- R7: Onboardings persist `recipe_digest` into snapshot `recipes/<sha256>`
  and embed neither the resolution nor a registered `ToolDefinition`.
  `credential_source` replay appends R7 owner-words entries.
- R8: The golden corpus covers >=20 real targets (named: github-mcp-server,
  google-calendar-mcp, notion-mcp-server, mcp-atlassian, gitlab-mcp; plus a
  remote-only registry entry, an MCPB bundle, a docker mcp/* image and three
  CLIs) — 100 shuffled-entry resolutions per target produce identical
  pinned digests.

## Verification

- `go test ./internal/toolhub/api -run 'TestLedger|TestGoldenCorpus'`
- `go test ./internal/toolhub -run 'TestOnboarding|TestCredentialSource|TestResolve'`
- `GOLDEN_UPDATE=1` re-pins digests after an intentional model change.
