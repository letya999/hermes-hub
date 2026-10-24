# CHG-0150: Vault layout, feature gate and CORE injection (M20 stage 1)

Chain: `docs/adr/ADR-0073-memory-vault-openknowledge.md` D1–D3 ->
`specs/active/SPEC-0070-memory-vault-openknowledge.md` R1 -> this plan ->
`state.yaml`. Evidence: `research.md`.
Next stage: CHG-0151.

## Goal

Owner enables `memory_vault` and gets a git-backed `spaces/<id>/vault/`
with the layered memory tree, card schema, and `CORE.md` materialized
into the agent prompt context — the substrate every later stage writes to.

## Read before coding

1. `research.md`; ADR-0073; SPEC-0070 R1; `docs/spaces.md`; ADR-0050.
2. `internal/stack/config.go` (Features registry), `render.go`,
   `compose.go`, `space.go`; `internal/stack/defaults/{agent,workspace}.yaml`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib + `google/jsonschema-go` (already a dep).
- `just check` before handoff.

## Slices

### S1.1 `memory_vault` feature registration (R1.1, R1.5)

Anchors: `internal/stack/config.go` `Features`, `Validate`;
`internal/stack/hermes_capability.go` tool resolution.

Change:
1. Add `memory_vault` feature (requires: none; scope note: vault mount +
   scheduled jobs) mirroring `web`/`deep_research` registration.
2. Reject `memory_vault` on non-user scopes; `off` explicit deny honored.

Tests (red first):
- `TestMemoryVaultFeatureRegistered` — valid in `tools:`/features list.
- `TestMemoryVaultDeniedRendersNothing` — `off`/absent → no mount/env/job.

### S1.2 Vault scaffold + mount render (R1.1, R1.4, R1.5)

Anchors: `internal/stack/space.go` home layout, `internal/stack/render.go`
mount table; new `internal/vault` package (layout constants, template).

Change:
1. `internal/vault/template.go` — scaffold tree: `memory/daily/`,
   `memory/cards/{contacts,projects,decisions,ideas,notes}/`,
   `memory/summaries/{daily,weekly,monthly,yearly}/`, `CORE.md`,
   `CORE.history.md`, `schema.json`, `.graph/`, `attachments/`, plus a
   vault `.gitignore` (`.graph/`, temp, nothing else).
2. `vault.Ensure(dir)` — idempotent: create only missing entries,
   `git init` + initial commit when `.git` absent; never overwrite.
3. Render mounts `spaces/<id>/vault` → `/vault` rw when the feature is
   enabled; `generated/` carries the rendered list.

Tests:
- `TestVaultEnsureIdempotent` — second call changes nothing, keeps files.
- `TestVaultEnsureGitRepo` — `.git` exists with initial commit.
- `TestRenderVaultMountGated` — mount only when feature enabled.

### S1.3 Card schema validation (R1.2)

Anchors: `internal/vault/cards.go`; `schema.json` template.

Change:
1. `vault.ValidateCard(path, doc)` via `google/jsonschema-go`: type,
   status, confidence, source, tags[], aliases[], created/updated.
2. Validation errors are structured (`field`, `rule`); invalid cards are
   reported, not repaired.

Tests:
- `TestCardSchemaValid` / `TestCardSchemaRejectsBadType` /
  `TestCardSchemaReportsField`.

### S1.4 CORE materialization into prompt context (R1.3)

Anchors: `internal/stack/render.go` soul-effective composition
(`generated/soul-effective.<env>.md`); `docs/agent-home.md`.

Change:
1. Compose CORE.md (≤3600 chars, hard-truncate with marker when over)
   into the generated prompt context after the contract, before persona.
2. Materialization always re-reads the current file — never cached.

Tests:
- `TestCoreRenderedIntoPrompt` — content present in generated file.
- `TestCoreCapTruncates` — >3600 → capped with truncation marker.
- `TestCoreAbsent` — feature off → no CORE block.

## Red-team checklist

- `vault/` symlink escapes rejected like `scope.yaml` (`noSymlinkPath`).
- Feature on org scope → validation error.
- Secrets naming (`*.env`, `*.auth`) inside vault scaffold impossible by
  construction.

## Acceptance

A1.1–A1.4 on the dev stack: `init` → tree+git; render → CORE in prompt
context; re-init no-op; feature off → doctor shows zero effects.

## Out of scope

- Daily ingest, rollup, search, write contract (later stages).
- Honcho/native memory changes.
