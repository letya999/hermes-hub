## Essence

Make workspace.yaml a readable exact tree and enforce its writable/read_only/mount boundaries for two users.

## Documents

- Decision: `docs/adr/ADR-0092-six-passport-space-contract.md` (D1–D7).
- Frozen requirements: `specs/active/SPEC-0091-six-passport-space-contract.md` (R4, A4).
- Stage plan: `.work/in-progress/CHG-0245-workspace-layout/plan.md` and `state.yaml`.

## What to do

| Slice | Requirement | Anchors | Result |
|---|---|---|---|
| S4.1 | R4.1 | `internal/stack/render_env.go RenderEnvironment` | Build /config, inbox/people, inbox/services, work, projects (nested Git), artifacts/{documents,images,videos,archives,services}, outbox, vault, skills and shared mounts; derive ASCII tree from actual approved layout. |
| S4.2 | R4.2 | `internal/communication/ internal/stack/skill_doctor.go` | Route human/service incoming originals by safe ID with filename metadata, retain originals read-only, and distinguish generated service originals from editable copies. Apply path and mount guards to file, document, media, archive, inbox and CLI surfaces using real-root confinement; preserve existing data during migration. |

## Acceptance criteria

- [ ] A4: Two-user fixtures preserve Git projects and attachments; file tools cannot write /config, /skills, shared, inbox originals or vault memory.
- [ ] Send one file from a person and one service fixture; inspect safe paths and create a nested Git project in dev.
- [ ] Update all affected callers, documentation, prompts, examples and versioned contracts; do not claim live success from mocks.

## Tests to write (red first)

- `TestWorkspaceLayoutRejectsSymlinkAndCrossUserWrite` — prove the allowed path and the refusal/rollback case.

## Mandatory red-team

- [ ] Filename traversal, symlink swap, renamed mount and Git-project AGENTS do not cross or widen authority.
- [ ] Logs and diagnostics expose only allowlisted metadata; no credential material or key names.

## Live verification

1. Send one file from a person and one service fixture; inspect safe paths and create a nested Git project in dev.
2. Record safe receipts and distinguish local unit/container evidence from a running dev-stack result.

## Prior art to study

- docs/spaces.md; #208; M20 #299/#303; M24 #364.

## Out of scope / dependencies

- Vault internals and skill publishing are owned by M20/M24.
- Dependencies: M29.2,M29.3. Stage issues: #426, #427. 
