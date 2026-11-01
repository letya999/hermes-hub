# CHG-0243: M29.2 Compile platform, organization, and user sources into one six-passport revision (M29 stage 2)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R2 -> this plan -> `state.yaml`.
Previous stage: CHG-0242. Next stage: CHG-0244. Evidence: state.yaml.

## Goal

Make six read-only effective passports from pinned templates and narrowing ceilings without creating six editable configs per level.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/spaces.md; SPEC-0084 provisioning profiles; ADR-0092 D1-D3.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S2.1 Contract and failing regression (R2.1)

Anchors: internal/stack/provisioning.go resolveProvisioningProfile; internal/stack/profiles.go applyProfiles; internal/stack/scope.go ReadScope; internal/stack/render_env.go RenderEnvironment; cmd/hubctl/profile.go.

Change:
1. Implement schema field classes (invariant, narrow, choice, user_data), exact template inheritance and safe user overrides; map old profiles without silently widening.

Tests (red first):
- `TestPassportCompilerAtomicSixFileRevision` — One invalid layer changes none of the six active files; approved new revision is consistent and user cannot edit generated authority.

### S2.2 Implementation and consumer migration (R2.2)

Anchors: internal/stack/provisioning.go resolveProvisioningProfile; internal/stack/profiles.go applyProfiles; internal/stack/scope.go ReadScope; internal/stack/render_env.go RenderEnvironment; cmd/hubctl/profile.go.

Change:
1. Validate six cross-file contracts and common apiVersion/kind/metadata, with deterministic diff and provenance including timestamps and catalog/source versions.
2. Stage generation under a revision directory, fsync/publish one pointer atomically, keep previous revision on any parse/validation/write failure.

Tests (red first):
- Extend `TestPassportCompilerAtomicSixFileRevision` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Tampered generated grants or symlinked template cannot widen a user's effective authority.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A2: One invalid layer changes none of the six active files; approved new revision is consistent and user cannot edit generated authority.
- Live: Dry-run/apply an org template update for two users; compare source diff and six active revision IDs.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- No account login, deployment or automatic template upgrades.
- Dependencies: M29.1.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
