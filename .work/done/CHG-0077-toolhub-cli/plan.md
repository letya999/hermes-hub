# CHG-0077: M8 — ToolHub CLI connectors (milestone 9, issues #87–#91, #100)

One coherent feature: bounded CLI becomes a first-class ToolHub catalog
transport, projected through the single ToolHub MCP endpoint. No shell, no
interpolation, no PATH-wide lookup, no Hermes `mcp_servers` writes.

## Design decisions (locked)

- **Runner (`cli_runner.go`)**: `IsolationCheck` becomes
  `func(ctx, EffectiveBinding, root, working string) error`; `Run` takes the
  effective binding so the isolation callback sees the immutable workload
  identity. `CallEnv` opens the per-binding workload workspace and uses it as
  cwd (per-job temp dirs cleaned up after the call). `RuntimeEnvironment` from
  the definition merges under injected credentials. `Resolved` map pins each
  allowlisted command to the absolute binary found once via `exec.LookPath`;
  `CLIRunnerFromEnv()` builds the runner from `HUB_STATE` + `HUB_CLI_ALLOWLIST`
  (comma-separated names or absolute paths). An allowlist entry ending in `/`
  admits artifact binaries under that directory prefix (needed for #100).

- **Admission (`admission.go`)**: `controllerPlan` gains `command`, `root`,
  `working_dir` (omitempty — container plans unchanged). `AdmissionReceipt`
  gains `isolation` scope list. `AdmissionReceipt.validate` branches per
  transport: BoundedCLI requires workload/state/enforced match, execution
  deep-equal, `isolation` superset of {filesystem, network, pid}, no endpoint,
  no image digest. `CLIAdmissionFromEnv()` posts the plan to the same
  `HUB_TOOLHIVE_ADMISSION_ENDPOINT` + token; unset → nil → runner fails closed.

- **Controller (`generic_controller.go`)**: `/admit` decodes the union plan;
  a plan with `command` set goes to `admitCLI`. New optional config section
  `cli`: `{executor_service, allowed_networks, allowed_mounts, approved[]}`.
  `admitCLI` verifies the plan against an approved (definition_id, version,
  command, execution) tuple, then `docker inspect`s the executor container
  (resolved via compose service label inside the docker scope project) and
  requires: running, non-privileged, non-root user, no host pid/net mode, no
  added caps, mounts ⊆ allowed targets, networks ⊆ allowed list, and container
  cgroup limits ≤ plan limits (tighter-than-plan is honest; looser denies).
  Receipt: `{workload_id, running, enforced, isolation:[fs,net,pid], execution}`.
  No per-workload objects are spawned; `/release` stays a no-op for CLI.

- **Catalog (#89)**: `CLICatalogDefinitions()` returns the immutable shipped
  catalog CLI definitions (`cli-git-ls-remote`: `git ls-remote` with typed
  scalar args + `GIT_CONFIG_*` env credentials; `cli-rg-search`: `rg` search).
  `Store.Catalog` already handles any transport — nothing else needed except a
  `hubctl connector catalog-cli` registration verb that registers definitions +
  catalog publications for the operator.

- **Owner CLI (#90)**: new control op `prepare_cli`. Args: `name`, `command`,
  `args` (fixed argv), `tools` (name/effect/typed scalar args), `credentials`
  (env names), `runtime_env`. Control plane builds the definition from a fixed
  template (PerUser, bounded execution defaults, no egress claim), requires the
  `self-install` grant, validates command ∈ `HUB_CLI_ALLOWLIST`, argv tokens
  against the restricted pattern, then runs the normal
  onboarding → credentials → confirm → enable → binding path. Definition is
  `PublicationUser` owner-scoped — other principals cannot see or resolve it.

- **CLI artifacts (#100)**: `prepare_cli` also accepts `source` (repo@commit)
  or `oci` digest: goes through `ImportGitHubArtifact`-style pipeline, then
  `docker create`/`cp` extracts the built binary into
  `HUB_ARTIFACT_DIR/cli/<digest>/bin` — the allowlist directory-prefix entry
  admits it. Minimal: reuses the existing recipe/build machinery; the CLI
  recipe's entrypoint binary is extracted by digest, never rebuilt per owner.

- **Stack (`render.go`)**: generic-controller.json gains the `cli` section
  (executor_service=toolhub, approved defs seeded from catalog definitions);
  toolhub env gains `HUB_CLI_ALLOWLIST` generated from the same list.

## Verification

- `just check` (coverage ≥85%); no dependency changes → no `just security`.
- Docker gate: run `just docker-check`/`docker-check-prebuilt` if a daemon is
  available; report honestly otherwise.

## Out of scope

Implementing a per-workload sandbox supervisor; egress host-allowlisting for
host-exec'd CLI (spec documents CLI egress = executor container egress);
changing the generated Hermes MCP default.
