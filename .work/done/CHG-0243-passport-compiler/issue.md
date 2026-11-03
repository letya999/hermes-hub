## Essence

Make six read-only effective passports from pinned templates and narrowing ceilings without creating six editable configs per level.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R2, A2).
- Stage plan: `.work/in-progress/CHG-0243-passport-compiler/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S2.1 | R2.1 | `internal/stack/provisioning.go resolveProvisioningProfile` | Implement schema field classes (invariant, narrow, choice, user_data), exact template inheritance and safe user overrides; map old profiles without silently widening. |
| S2.2 | R2.2 | `cmd/hubctl/profile.go` | Validate six cross-file contracts and common apiVersion/kind/metadata, with deterministic diff and provenance including timestamps and catalog/source versions. Stage generation under a revision directory, fsync/publish one pointer atomically, keep previous revision on any parse/validation/write failure. |

## Acceptance criteria

- [ ] A2: One invalid layer changes none of the six active files; approved new revision is consistent and user cannot edit generated authority.
- [ ] Dry-run/apply an org template update for two users; compare source diff and six active revision IDs.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestPassportCompilerAtomicSixFileRevision` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Tampered generated grants or symlinked template cannot widen a user's effective authority.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Dry-run/apply an org template update for two users; compare source diff and six active revision IDs.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/spaces.md; SPEC-0084 provisioning profiles; ADR-0092 D1-D3.

## Out of scope / dependencies

- No account login, deployment or automatic template upgrades.
- Dependencies: M29.1. Stage issues: #425. 
