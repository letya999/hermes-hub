# CHG-0183: Image hygiene pack (M26 stage 1)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` D5 ->
`specs/active/SPEC-0082-image-role-budgets.md` R1 -> this plan -> `state.yaml`.
GitHub issue: #373. Previous stage: `CHG-0182`. Next stage: `CHG-0184`.

## Goal

Remove ~400MB of dead weight from the agent image without touching
architecture: compiled-bytecode venv stage, pruned upstream checkout,
static noVNC, no e2e in prod, cleaned apt lists, no setuid, shm_size scoped
to runtime.

## Read before coding

1. `docker/Dockerfile` (python_build, runtime, prod), `internal/runtime/runtime.go`
   (noVNC/websockify start, privateFiles), `internal/devcheck/hermes_contract_integration.go`
   (git rev-parse usage), `docker/hub-sidecar`, `justfile`.
2. Upstream image: editable `__editable__.hermes_agent` .pth/finder mapping —
   the import allowlist.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates when image/runtime code changes.
- Keep `optional-skills`, `plugins`, `skills`, `compat_manifest.json` intact.

## Slices

### S1.1 venv builder stage + bytecode (R1.1)

Anchors: `docker/Dockerfile` python_build + runtime stage.

Change:
1. Dedicated `python_build` stage: `UV_COMPILE_BYTECODE=1`, `uv sync --extra mcp`,
   hash-pinned extra installs only.
2. Runtime copies `.venv` + allowlisted upstream dirs.

Tests (red first):
- `TestImageHasBytecode`: image FS report shows `__pycache__` > 0.

### S1.2 Upstream tree prune + SHA file (R1.2, R1.3)

Anchors: `docker/Dockerfile` git fetch step;
`internal/devcheck/hermes_contract_integration.go` `git rev-parse`.

Change:
1. After fetch, write `git rev-parse HEAD` to `/opt/hermes/.hermes-rev`; remove `.git`.
2. Remove `tests/ apps/ website/ ui-tui/ web/ evals/ nix/ docs/ .github/ tests-js/
   mcp-research-data/ datagen-config-examples/ docker/ package-lock.json` from
   the runtime copy (keep everything the editable mapping points at +
   `optional-skills` + `plugins` + `skills` + `compat_manifest.json`).
3. devcheck reads the SHA file instead of `git rev-parse`.

Tests (red first):
- `TestHermesRevFileVerified`: devcheck verifies revision from `.hermes-rev`.
- `TestEditableImportsIntact`: module list from the mapping resolves.

### S1.3 Static noVNC (R1.4)

Anchors: `docker/Dockerfile` apt line; `internal/runtime/runtime.go` websockify args.

Change:
1. Remove `novnc` apt package; vendor noVNC static files (pinned release) into
   `/opt/novnc`; websockify `--web` path updated.

Tests (red first):
- `TestNoLibnodeInImage`: `libnode`/`nodejs` absent.

### S1.4 Small removals (R1.5)

Change:
1. `e2e` binary only in dev target; `rm` of `/var/lib/apt/lists` in every apt layer;
   strip setuid bits; `shm_size` only on runtime service; audit `uv` need.

## Red-team checklist

- Editable mapping imports all still resolve (smoke import inside built image).
- Bytecode writes cannot target a writable mount (rootfs stays RO).

## Acceptance

- A1 (partial): runtime image >= 400MB smaller before role split;
  devcheck revision check green.

## Out of scope

- Voice stack (CHG-0184), role split (CHG-0185), browser (CHG-0186).

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #373 closed; folder moved to `.work/done/`.
