# CHG-0209: OpenKnowledge service render (M20 stage 8)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D8 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R8 -> this plan ->
`state.yaml`. Evidence: `../CHG-0204-vault-layout/research.md`,
`../CHG-0204-vault-layout/spike-m20.md` (external-write + `.okignore`).
Previous stage: CHG-0212. Next stage: CHG-0210.

## Goal

`knowledge_ui` renders one pinned, single-replica `open-knowledge`
container per space over the vault — private network only — so the owner
can see and edit what the agent knows; human edits carry owner
provenance and always win.

## Read before coding

1. ADR-0086 D8; SPEC-0085 R8; spike-m20.md Q4; research.md OK section.
2. `internal/stack` compose render (`compose.go`, `render.go`,
   `doctor.go`); existing sidecar patterns (browser, media).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only. No docker steps in this milestone —
  render/golden tests only; live items recorded pending.
- `just check` before handoff.

## Slices

### S8.1 Feature + pinned single replica (R8.1, R8.5)

Anchors: `internal/stack/config.go` Features; `internal/stack/compose.go`.

Change:
1. `knowledge_ui` feature; compose service `open-knowledge` with pinned
   `@inkeep/open-knowledge` version image (hubctl BuildKit path),
   `replicas: 1` enforced — scale-out rejected at render.
2. `ok init --no-mcp --no-skills` first-boot init + `ok start`.

Tests (red first):
- `TestKnowledgeUIFeature`, `TestOKRenderSingleReplica`,
  `TestOKDisabledRendersNothing`.

### S8.2 Vault mount + `.okignore` (R8.2)

Anchors: compose render; scaffold (CHG-0204 adds `.okignore`).

Change:
1. Volume = `spaces/<id>/vault` → project root; rendered `.okignore`
   covers `.git/`, `.graph/`, `.ok/`, `runtime/` sidecars.

Tests:
- `TestOKMountVaultRoot`, `TestOKIgnoreContent`.

### S8.3 Private network only (R8.1)

Anchors: compose networks.

Change:
1. Joins the space private network; no published ports in this stage
   (edge is CHG-0211).

Tests:
- `TestOKNoPublishedPorts`, `TestOKPrivateNetOnly`.

### S8.4 External-write coexistence (R8.3)

Anchors: service config; spike-verified OK conflict behavior.

Change:
1. Encode the verified policy: hub writes while a doc is open → OK
   stale-save rejection + conflict surface; document the contract in
   `docs/operations.md`.

Tests:
- `TestExternalWritePolicyDocumented` (unit vs spike findings).

### S8.5 Owner provenance on human writes (R8.4)

Anchors: `internal/vault` writer attribution.

Change:
1. Commits landing from the OK/UI path classify `owner` provenance —
   they win the next rollup (`truth_pending` flow).

Tests:
- `TestUIWriteOwnerProvenance`, `TestUIEditWinsRollup`.

## Red-team checklist

- Container cannot reach outside the space network.
- Symlink inside vault pointing outside → UI cannot follow past root.
- Service down → vault/capture/recall unaffected.

## Acceptance

A8.1–A8.5. Live checks (docker ps single replica, UI edit commits)
recorded pending — requires live stack (deferred — no-docker scope).

## Out of scope

- Public edge/auth (CHG-0211, M18), agent MCP wiring (CHG-0210), org
  instances (backlog milestone).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
