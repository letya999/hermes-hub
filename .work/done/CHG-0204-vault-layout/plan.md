# CHG-0204: Vault substrate, provider seam, audience-scoped CORE (M20 stage 1)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D1/D2/D10/D12/D15 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R1 -> this plan ->
`state.yaml`. Evidence: `research.md`, `spike-m20.md`.
Next stage: CHG-0205.

## Goal

Owner enables `memory_vault` and gets a git-backed `spaces/<id>/vault/`
with the layered memory tree, the `memory.Provider` seam everything later
plugs into, a single hash-checked `VaultWriter`, and `CORE.md` delivered
per run to private-audience runs only — while the native `memory` toolset
leaves the tool surface.

## Read before coding

1. `research.md`, `spike-m20.md`; ADR-0086 D1/D2/D10/D12/D15; SPEC-0085 R1;
   `docs/spaces.md`; ADR-0050.
2. `internal/stack/config.go` (Features registry), `render.go`,
   `hermes_capability.go`, `space.go`; `internal/commodel/mapping.go`;
   `internal/communication/tasks.go` `StyleForRun`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib + `google/jsonschema-go` (already a dep).
- `just check` before handoff.

## Slices

### S1.1 `memory_vault` feature registration (R1.1)

Anchors: `internal/stack/config.go` `Features`, `Validate`;
`internal/stack/hermes_capability.go` tool resolution.

Change:
1. Add `memory_vault` feature (requires: none) mirroring `web`/
   `deep_research` registration; reject on non-user scopes; `off`
   explicit deny honored.

Tests (red first):
- `TestMemoryVaultFeatureRegistered`, `TestMemoryVaultDeniedRendersNothing`.

### S1.2 Vault scaffold + mount render (R1.1)

Anchors: `internal/stack/space.go` home layout, `internal/stack/render.go`
mount table; new `internal/vault` package (layout constants, template).

Change:
1. `internal/vault/template.go` — scaffold tree: `memory/daily/`,
   `memory/cards/{contacts,projects,decisions,ideas,notes}/`,
   `memory/summaries/{daily,weekly,monthly,yearly}/`, `CORE.md`,
   `CORE.history.md`, `schema.json`, `.graph/`, `attachments/`, plus a
   vault `.gitignore` (`.graph/`, temp) and `.okignore`
   (`.git/`,`.graph/`,`.ok/`,`runtime/` — D8 spike).
2. `vault.Ensure(dir)` — idempotent: create only missing entries,
   `git init` + initial commit when `.git` absent; never overwrite.
3. Render mounts `spaces/<id>/vault` → `/vault` rw when enabled.

Tests:
- `TestVaultEnsureIdempotent`, `TestVaultEnsureGitRepo`,
  `TestRenderVaultMountGated`, `TestVaultSymlinkRejected`.

### S1.3 Card schema validation (R1.2)

Anchors: `internal/vault/cards.go`; `schema.json` template.

Change:
1. `vault.ValidateCard(path, doc)` via `google/jsonschema-go`: type,
   status, confidence, source, tags[], aliases[], created/updated.
2. Structured errors (`field`, `rule`); report, never repair.

Tests:
- `TestCardSchemaValid`, `TestCardSchemaRejectsBadType`,
  `TestCardSchemaReportsField`.

### S1.4 `Provider` seam + scope minting (R1.3)

Anchors: new `internal/memory` package; `internal/memory/memorytest`.

Change:
1. `Provider` interface: `Caps/Capture/Recall/Search/Write/Export/Health`
   with `Entry`, `Scope`, `Caps` types (audience, provenance, kind fields;
   `vector` capability reserved, off in MVP).
2. `MintScope(env, principal, audience)` → opaque key (HMAC over the
   triple with a hub-side secret material; raw ids never inside).
3. `memorytest.Conformance(t, factory)` — isolation, idempotent capture,
   empty≠unavailable, provenance respected; run against an in-memory
   fake AND the vault provider stub.

Tests:
- `TestProviderScopeIsolation`, `TestConformanceVault`,
  `TestConformanceFake`, `TestScopeOpaqueNoRawIDs`.

### S1.5 `VaultWriter` (R1.4)

Anchors: `internal/vault/writer.go`.

Change:
1. Content-hash-checked writes (`sha256` of the file read for the call);
   authored scoped git commits (`agent`/`owner`/`rollup`/`ui` classes);
   stale-hash → rejected; conflict → owner version kept + `truth_pending`
   marker in a writer-owned sidecar (not user-visible markdown).
2. Zone model: `daily/`, `cards/`, `summaries/` guarded zones;
   `library/` free; path always confined under vault root (no symlink
   follow, same rule as `scope.yaml`).

Tests:
- `TestWriterStaleHashRejected`, `TestTruthPendingOnOwnerConflict`,
  `TestWriterScopedCommits`, `TestWriterZoneConfined`.

### S1.6 Audience-scoped CORE per run (R1.5)

Anchors: `internal/vault/core.go`; `internal/communication/runtime.go`
instructions assembly; `internal/commodel/mapping.go` JobMapping.

Change:
1. `vault.CoreBlock()` → `## Directives` + `## Facts`, ≤3600 chars,
   re-read each use.
2. Delivered inside `ExecuteRequest.Instructions` for private-audience
   runs only, pinned on the durable mapping (new `Core`/`CoreSnapshotted`
   fields, same mechanism as `StyleForRun`); group/audience runs get
   zero private bytes.

Tests:
- `TestCorePrivateOnly`, `TestGroupPromptExcludesCore`,
  `TestCoreCapTruncates`, `TestCorePinnedAcrossRetries`.

### S1.7 Native `memory` toolset removal (R1.6)

Anchors: `internal/stack/hermes_capability.go` `disabledToolsets`,
`render.go` memory block.

Change:
1. `memory_vault` on → `"memory"` joins rendered `disabled_toolsets`
   (both managed and unmanaged renders) and `memory.memory_enabled/
   user_profile_enabled` render false; `MEMORY.md`/`USER.md` files are
   never touched.

Tests:
- `TestNativeMemoryDisabledWithVault`, `TestNativeFilesUntouched`.

## Red-team checklist

- `vault/` symlink escapes rejected like `scope.yaml`.
- Secrets-named files (`*.env`, `*.auth`) impossible in scaffold.
- Crafted audience/scope fields cannot mint a private scope in a group
  run.
- Provider cannot influence provenance or scope — hub-owned fields only.

## Acceptance

A1.1–A1.7 on the dev stack: `init` → tree+git; private run → CORE in
instructions, group → none; conformance green; stale-hash rejected;
native tool gone.

## Out of scope

- Daily ingest, search internals, recall lane, rollup, write contract,
  OK, external providers.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
