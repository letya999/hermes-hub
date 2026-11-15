# CHG-0259: workspace.yaml drives the container filesystem and file admission (M30 stage 5)

Chain: ADR-0093 D2, D6 -> SPEC-0092 R5 -> this plan -> `state.yaml`.
Evidence: `../../done/CHG-0254-passport-gap-audit/research.md` Findings 1, 2, 5.
Previous stage: CHG-0258. Next stage: CHG-0260.

## Goal

What the agent sees and where it may write is exactly `workspace.yaml`:
the runtime container is created with `/config`, the `/workspace` subtrees,
`/vault`, `/skills` and every `/shared/*` mount from the passport; file
tools allow writes only under `writable` (read_only wins) and pick up a
narrowed boundary on the next call without restart. The ASCII tree is the
single human map.

## Read before coding

1. SPEC-0092 normative `workspace.yaml`; ADR-0092 D5 (inbox/service artifacts never agent-writable, vault owns memory).
2. `internal/stack/render_env.go:99` (hardcoded dir list), `compose.go:61-93`, `supervisor/ensure.go:503`, `:658`.
3. `internal/stack/tools.go` `Workspace.validate`, `resolveMount`, `deniedMountSource`, `workspaceReadOnlyPaths`.
4. `internal/agenttools/tools_fs.go` `readOnlyPathList`, `guardWritable`, `resolvePathLinks`; `tools.go:252` (env-derived boundary).
5. CHG-0255 S1.4 (`/config` directory view).

## Preconditions and rules

- Agent-tool contract unchanged in names; behaviour change (multi-root paths) ships with docs/tools.md + prompt contract + version bump.
- Existing project trees and Git repos are never moved or deleted.

## Slices

### S5.1 Typed tree source and ASCII emission (R5.1)

NOTE (updated by CHG-0255 S1.0): writable/read_only/mounts.* vocab + emission landed in CHG-0255; tree is a plain layout join there — this slice owns the typed layout nodes and the box-drawing renderer.

Anchors: `passport.go` `classes["workspace"]`, tree derivation (`:300`); new `internal/passport/tree.go`.

Change:
1. Platform template declares `workspace.layout` as typed nodes `{path, note?, mode: ro|rw, placeholder?}`; layers may only narrow (remove rw, add ro) and add `mounts`.
2. Compiler emits `tree` (box-drawing ASCII with `[только чтение]` markers and notes, localized via `internal/locale`), `writable`, `read_only`, `mounts`; `layout` itself is not emitted.
3. Checks: writable ∩ read_only empty after longest-prefix resolution; `/config`, `/skills`, `/workspace/inbox`, `/workspace/artifacts/services`, `/vault/memory` are invariant read_only; mount targets under `/shared/`, sources under the org/user space via `resolveMount`.

Tests (red first):
- `TestWorkspaceTreeASCIIMatchesNormative`
- `TestWorkspaceInvariantReadOnlyCannotBecomeWritable`
- `TestWorkspaceMountSourceOutsideScopeRejected`

### S5.2 Materialization and mount plan (R5.2)

Anchors: new `internal/stack/mount_plan.go` `MountPlan(eff, dir) []Mount`; `render_env.go:99`; `compose.go` runtime volumes; `supervisor/ensure.go` mount loops.

Change:
1. Render creates every layout directory (including `vault/{memory,library,attachments}` skeleton, no memory content) instead of the hardcoded list.
2. `MountPlan` yields: `/config` (config-view dir, ro), `/workspace` (rw base) with ro sub-binds for read_only subtrees, `/vault/library|attachments` rw, `/vault/memory` ro, `/skills` ro, `/shared/<name>` per `mounts`.
3. Compose and both supervisor spawn paths consume `MountPlan`; hardcoded lists deleted.

Tests (red first):
- `TestSupervisorMountsFollowWorkspacePassport`
- `TestComposeRuntimeMountsFollowWorkspacePassport`
- `TestRenderMaterializesLayoutWithoutTouchingProjects`

### S5.3 Live file admission (R5.3)

Anchors: `agenttools/tools_fs.go` `guardWritable`; `tools.go:252`.

Change:
1. Boundary loaded from `/config/workspace.yaml` (mtime-cached) instead of `HUB_WORKSPACE_RO`; absent/undecodable passport → deny writes (fail closed).
2. Absolute paths across roots (`/workspace`, `/vault`, `/skills`, `/shared`) resolved through per-root `os.Root`; longest-prefix decision, read_only wins; symlink/rename escape checks kept per root.
3. Writes additionally call PDP `workspace.write` (CHG-0256); project creation under `/workspace/projects` requires `workspace.create_project`.

Tests (red first):
- `TestFileWriteDeniedOutsideWritableLive` (swap revision mid-session → next write denies)
- `TestInboxAndServiceArtifactsNeverWritable`
- `TestVaultMemoryNotWritableByFileTools`
- `TestSymlinkFromWritableIntoReadOnlyDenied`

## Red-team checklist

- Symlink in `/workspace/work` → `/workspace/inbox` or `/config` → write denied.
- Rename from writable into read_only → denied.
- Two users: user A mount never visible in user B's plan.
- Org mount `mode: rw` attempted by user layer → compile error.

## Acceptance

A5 live: runtime container `docker inspect` mounts equal `MountPlan`;
`file_write` to `/workspace/inbox/x` denies; narrowing `writable` (remove
`/workspace/outbox`) via apply → next write there denies in the same
session; `/shared/acme-docs` readable, not writable; agent `cat
/config/workspace.yaml` shows the current revision.

## Out of scope

M20 memory tools and rollup; vault git substrate.

## Done checklist

- [ ] Tests red then green; `just check`; docker-check; docker-clean.
- [ ] hub-agent-tools bump + rebind + respawn.
- [ ] docs/spaces.md, docs/agent-home.md, docs/tools.md updated.
- [ ] Issue closed; state done; folder moved.
