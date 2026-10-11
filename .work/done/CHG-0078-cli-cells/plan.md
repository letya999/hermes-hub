# CHG-0078: CLI sandbox cells (milestone 9 follow-up, issues #239–#242)

Move bounded-cli execution from exec-children inside the ToolHub container to
controller-owned sibling **cells**: per-principal sandboxed containers that
carry exactly one workspace bind, no host state, no broker material, no
docker.sock, and `--network none` unless a reviewed egress list earns an
internal network + Squid CONNECT allowlist.

## Design decisions (locked)

- **Cell contract.** Every CLI cell image runs `/cellinit` — a tiny static
  helper (`cmd/cellinit`) bind-mounted read-only into every cell by the
  controller, seeded to `/state/cli-cellinit` at controller start and resolved
  to a host path through `dockerBindSource`. Subcommands: `pause` (pid1),
  `write <path>` (stdin→0600 file), `check <path> <token>` (canary verify),
  `clean <dir> <name> <token>` (pool reset check). No shell required, so the
  same contract covers scratch/distroless owner images.
- **Execution model is uniform:** `docker create --init --entrypoint /cellinit
  pause` → `docker inspect` → receipt fields → `docker start` → `docker exec
  -e K=V cell <command> argv` → optional warm keep or `docker rm -f`.
  Secrets never appear in create-time env, so `docker inspect` of any cell
  shows no credentials at any lifecycle tier.
- **`/cli-exec` endpoint** (synchronous) on the generic controller, beside
  `/admit`/`/release`. Request = plan + command + argv + env + workspace
  {scope,path,access} + principal/context/binding/job + lifecycle + toolset.
  Response = receipt + output + exit_code. `/cli-release` kills warm cells by
  binding/job selector (disable, revoke, job end).
- **Workspace scopes.** `none` → no mount; `binding` → toolhub-resolved
  `/state/...` path translated via `HUB_DOCKER_HOST_ROOT`; `principal` →
  `spaces_root/<principal>/workspace` inside the controller (`/spaces` mounted
  ro at render; host path via new `HUB_SPACES_HOST_ROOT` pair syntax shared
  with dockerBindSource). Access `ro` default, `rw` declared.
- **Admission now means ceiling enforcement.** The old executor-attestation
  config (`ExecutorService`, `AllowedMounts`, `AllowedNetworks`, `Approved`,
  `UserCommands`, `UserArtifactDir`, `cliExecutorProblem`) is removed; the cell
  itself is the isolation proof. `CLIControllerConfig` keeps an egress union
  + execution ceiling and gains `tools_image`, `cell_init`, `spaces_root`,
  `runtime` (runc|runsc|kata), warm bounds and pool size.
- **Catalog image.** `cli-tools` Dockerfile stage (debian slim + git +
  ripgrep + ca-certificates, USER 10001). Shipped defs keep `Source.Command`
  names; a plan without `image` executes on the configured tools image.
  `hubctl build`/`up` gains an explicit `docker build --target cli-tools`
  step tagging `hermes-cli-tools:0.3.0-<env>`.
- **Owner artifacts.** The verified OCI image IS the artifact: definitions
  record `Source.Image` + `Source.Digest` (image manifest digest) + guest
  `Command`. `dockerCLIExtract`, per-call `fileSHA256`, `HUB_CLI_ALLOWLIST`,
  `Resolved`, `UserArtifactDir`, `UserCommands` all go away.
- **Lifecycle ladder** (`Workload.Lifecycle`): `ephemeral` (default) → `task`
  (principal+job+digest, short idle reap) → `binding` (principal+binding+
  digest, WorkloadBudget idle reaping) → `toolbox` (principal+context+
  toolset digest, `--mount type=image` ro layers per member, Docker≥28 gate)
  → `shared-pool` (#242, `stateless` defs only). Warm reuse guard before
  every exec: re-inspect (running, image digest, mounts) + canary
  `check`; drift → quarantine label + kill + audit + fresh create. Kill
  ladder: job end, idle TTL, disable, revoke, max-age, max-reuse.
- **Sources (#241).** `github:` commit → existing restricted build; repo
  without Dockerfile → new cli-mode recipe (multi-stage: toolchain builds,
  minimal runtime — scratch+ca-certs for static go/rust, same-base slim for
  python/node — `ENTRYPOINT` = tool). `github-release:` → release asset by
  name pattern verified against `checksums.txt` or spec digest, wrapped in a
  scratch image. `scope: shared` only via operator principal/catalog path.
- **Runtime tiers + pools (#242).** `cli.runtime` in controller config →
  `docker create --runtime=…`; missing runtime fails closed at admit.
  shared-pool claims/reset/canary/rotate with bounded wait.
  Credential brokering: `Delivery:"brokered"` inputs produce
  `HUB_CRED_URL_<NAME>=http://<credproxy>/<host>` env placeholders; a
  per-cell `cellinit cred-proxy` sidecar (config `docker cp`'d, never env)
  injects the real Authorization header on allowed upstreams.

## Files

- `cmd/cellinit/main.go` — new static helper binary.
- `internal/toolhub/cell.go` (new) — cell registry, create/inspect/exec/canary,
  workspace resolution, egress net+proxy, runtime gate, pools.
- `internal/toolhub/cli_controller.go` — rewritten admission/exec handlers.
- `internal/toolhub/cli_runner.go` — rewritten as /cli-exec HTTP client.
- `internal/toolhub/admission.go` — receipt gains `cell_id`; CLI path posts
  exec requests instead of executor attestation.
- `internal/toolhub/toolhub.go` — `Workload.{Lifecycle,Stateless,WorkspaceScope,
  WorkspaceAccess}`, BoundedCLI source validation for image+digest.
- `internal/toolhub/cli_catalog.go` — defs get principal workspace + lifecycle.
- `internal/toolhub/cli_prepare.go`, `cli_artifact.go` — image-as-artifact
  onboarding, github-release source, scope gate.
- `internal/toolhub/artifact_language.go` — cli recipe mode.
- `internal/stack/render.go` — controller config section, /spaces mount,
  HUB_CLI_ALLOWLIST removal, tools image wiring.
- `cmd/hubctl/main.go` — cli-tools build step; `cmd/hubctl/connector.go` —
  catalog-cli unchanged interface.
- `docker/Dockerfile` — cli-tools stage + cellinit copy.
- Tests: rewrite cli_test/cli_more_test/stage2 CLI parts; new cell_test.go;
  docker integration canary (skip-gated like existing integration tests).
- `specs/active/SPEC-0047`, `docs/adr/ADR-0033` (append executor→cell
  amendment), `docs/index.md` as needed.

## Verification

`just check` (coverage ≥85%, race+shuffle). Docker gates run per AGENTS.md;
report honestly if unavailable locally.
