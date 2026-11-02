# CHG-0245: M29.4 Materialize the workspace tree, safe inbox/artifacts, projects, vault and read-only mounts (M29 stage 4)

Chain: `docs/adr/ADR-0092-six-passport-space-contract.md` -> `specs/active/SPEC-0091-six-passport-space-contract.md` R4 -> this plan -> `state.yaml`.
Previous stage: CHG-0244. Next stage: CHG-0246. Evidence: state.yaml.

## Goal

Make workspace.yaml a readable exact tree and enforce its writable/read_only/mount boundaries for two users.

## Read before coding

- `AGENTS.md`, `README.md`, `docs/index.md`, ADR-0092, SPEC-0091, and docs/spaces.md; #208; M20 #299/#303; M24 #364.
- Re-run the migration matrix from CHG-0241 against the current branch before touching any consumer.

## Preconditions and rules

- Preserve other tasks' work. Tests red first; `just check` before delivery; broker and Docker gates when affected.
- Every agent-tool contract change includes description, `internal/stack/prompt_contract.go`, `docs/tools.md`, `docs/routines.md`, catalog version bump, rebind and respawn.

## Slices

### S4.1 Contract and failing regression (R4.1)

Anchors: internal/stack/render_env.go RenderEnvironment; internal/stack/compose.go Compose; internal/agenttools/tools_fs.go; internal/communication/ internal/stack/skill_doctor.go.

Change:
1. Build /config, inbox/people, inbox/services, work, projects (nested Git), artifacts/{documents,images,videos,archives,services}, outbox, vault, skills and shared mounts; derive ASCII tree from actual approved layout.

Tests (red first):
- `TestWorkspaceLayoutRejectsSymlinkAndCrossUserWrite` — Two-user fixtures preserve Git projects and attachments; file tools cannot write /config, /skills, shared, inbox originals or vault memory.

### S4.2 Implementation and consumer migration (R4.2)

Anchors: internal/stack/render_env.go RenderEnvironment; internal/stack/compose.go Compose; internal/agenttools/tools_fs.go; internal/communication/ internal/stack/skill_doctor.go.

Change:
1. Route human/service incoming originals by safe ID with filename metadata, retain originals read-only, and distinguish generated service originals from editable copies.
2. Apply path and mount guards to file, document, media, archive, inbox and CLI surfaces using real-root confinement; preserve existing data during migration.

Tests (red first):
- Extend `TestWorkspaceLayoutRejectsSymlinkAndCrossUserWrite` with failure, isolation and rollback cases before implementation.

## Red-team checklist

- Filename traversal, symlink swap, renamed mount and Git-project AGENTS do not cross or widen authority.
- Safe diagnostics expose only allowlisted fields; never auth bytes, envs or credential dictionary keys.

## Acceptance

- A4: Two-user fixtures preserve Git projects and attachments; file tools cannot write /config, /skills, shared, inbox originals or vault memory.
- Live: Send one file from a person and one service fixture; inspect safe paths and create a nested Git project in dev.
- Record unit, integration and live evidence separately in state.yaml.

## Out of scope / dependencies

- Vault internals and skill publishing are owned by M20/M24.
- Dependencies: M29.2,M29.3.

## Done checklist

- [ ] Tests red then green; `just check`; applicable broker/security/Docker gates.
- [ ] Red-team and live acceptance recorded; affected docs verified and indexed.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
