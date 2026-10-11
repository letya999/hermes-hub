---
status: active
title: Bounded CLI as a first-class ToolHub catalog transport
---
# ToolHub CLI transport

SPEC-0018 froze ToolHub as the single private streamable-HTTP MCP endpoint and
the five immutable records that carry a connector from definition to workload.
M8 (milestone 9; CHG-0077) freezes bounded CLI as a first-class catalog
transport on top of those records. A CLI definition is the same immutable
ToolDefinition as any other transport; only the runner differs. CLI tools are
projected through ToolHub — they are never written into Hermes `mcp_servers`,
and the generated MCP default for the Hermes config does not change.

## Records

A CLI connector uses the same five records as every other transport:

- **ToolDefinition** — `transport: bounded-cli`, `source.command` naming one
  executable plus `source.args` fixed argv; `tools[]` declare typed scalar
  inputs; `execution` carries the immutable policy (timeout, output bytes,
  cpu/mem/pid bounds); `credentials[]`/`credential_groups`/`runtime_environment`
  declare the exact environment the process may receive.
- **Connection** — unchanged: owner-scoped credential binding.
- **CredentialReference** — unchanged: opaque locator + named keys; values are
  materialized only into the selected workload's environment at call time.
- **ToolBinding** — unchanged: enable/disable/revoke lifecycle, binding
  revision and staleness checks apply before any exec.
- **WorkloadInstance** — unchanged identity contract; the deterministic
  workload id binds the admission plan to the receipt.

## Two definition classes

- **Catalog CLI** — immutable definitions shipped with the release (registered
  by the operator via `hubctl`, promoted to `catalog` visibility). Visible and
  enableable per the existing catalog grant model (catalog-default or
  per-definition grant). Commands must be binaries already pinned inside the
  released ToolHub image — no package installation into the base image.
- **User-owned CLI** — definitions registered through the authenticated
  Hermes → ToolHub control path by a principal holding the `self-install`
  grant (`prepare_source` with a `cli` spec object). Published with `user`
  visibility: only the owning principal can see, resolve, enable or call
  them. The operator allowlist bounds which executables a user definition
  may name; the model cannot introduce new binaries, only declare a spec
  over allowlisted ones.
- **Artifact CLI** — user-owned CLI whose `cli` spec names a commit-pinned
  GitHub `source` plus the guest `binary` path instead of `command`. The
  immutable source runs the same restricted build, OCI quarantine and
  provenance chain as MCP artifacts; the built OCI image IS the artifact.
  The definition pins the image manifest digest in `source.digest` and
  `source.image`, the declared `binary` stays inside the verified image
  layers as an absolute guest path, and nothing is extracted to the host.
  The cell runs the image itself, so the executed bytes can never drift
  from the pinned digest.
- **Release CLI** — artifact CLI whose `source` is
  `github-release:owner/repo@tag` plus an `asset` glob and optional
  `digest` / `member`. The tag is mutable and is never trusted as the pin:
  the immutable evidence is the downloaded asset's SHA-256, verified
  against the spec digest, the GitHub API digest and/or the release's
  checksums file — conflict or absence fails closed. The verified binary is
  wrapped in a `FROM scratch` image (`cli-release-v1` recipe, one checksum
  lock, `COPY --chmod=<octal>`, non-root `10001:10001`), and the definition
  records `source.release_tag` / `source.release_asset` /
  `source.asset_digest` as provenance.
- **Shared CLI** — `cli` spec with `scope: shared`. Only a principal
  holding the `install_shared` control grant may register one; the
  definition publishes with catalog visibility and `class: shared`.
  Catalog visibility does not mean a shared cell: every caller still gets a
  per-caller cell and never a binding workspace. `scope` accepts only
  empty/`owner` or `shared`; anything else fails closed.

Catalog and user-owned CLI share the runner, admission, credential and
lifecycle machinery; they differ only in publication visibility and who may
register them.

## Execution contract — no shell, ever

- Catalog definitions name a bare `source.command` resolved inside the
  dedicated `cli-tools` cell image; artifact definitions name an absolute
  guest path inside their pinned image. Neither path consults a host
  filesystem or a server-side allowlist of binaries — the admission
  boundary is the operator's controller config (`Approved` tuples and
  `UserCommands`), enforced per call.
- `source.args` and declared `tools[].arguments` produce argv tokens
  directly: fixed strings plus typed scalar values
  (string/integer/number/boolean). There is no shell, no interpolation, no
  command chaining, no glob expansion, no arbitrary flag injection —
  undeclared arguments are rejected.
- Owner spec argv is stricter than catalog argv: every fixed token must be a
  long option (`--flag` or `--flag=value`), because a bare token is a
  positional operand — for `rg` a literal path that would escape the
  contained workspace. Per-command deny lists block flags that execute
  another program or read files outside the workspace (`git` is excluded
  entirely: `.git/hooks` in a writable workspace executes arbitrary code).
- Shell names (`sh`/`bash`/`zsh`/`cmd`/`powershell`/`pwsh`) stay structurally
  denied as artifact guest paths and image commands. As **bare command
  plans** they are admitted only when the operator places the name in
  `HUB_CLI_ALLOWLIST`/`user_commands` — that is the deliberate `bash -c`
  warm-cell surface: argv is still fixed (`-c` plus one pattern-checked
  operand), and the cell's mounts, network and limits remain the boundary.
- The cell receives exactly the declared environment: injected credential
  keys plus static `runtime_environment`, validated against the env-name
  pattern. Caller env can never name `PATH`, `HOME`, `*_PROXY`, `NO_PROXY`
  or `LD_*` — the cell sets its own proxy variables and a caller-set value
  would bypass the egress allowlist or preload a binary.
- cwd inside the cell is `/work`: either a scratch tmpfs (`scope: none`),
  the binding workspace bind, or the principal workspace bind — each
  resolved, contained and symlink-checked before the bind is formed.
- Output is bounded, secrets in injected env are redacted, timeout and
  process-tree cancellation apply as in M2.

## Isolation — sibling cells, not children

Bounded CLI does not exec inside the ToolHub container. Every call goes to
the controller's `/cli-exec` endpoint with an immutable plan + call tuple;
the controller creates a sibling **cell** container
(`docker create → inspect → start → exec → wait/rm`) and the receipt proves
the inspected profile:

- `running` + `enforced: true` + `cell_id` of the executed cell;
- `runtime` naming the OCI tier (`runc`, `runsc` or `kata`);
- `isolation` covering `filesystem`, `network` and `pid`;
- for artifact definitions, `image_digest` matching the pinned manifest.

The cell profile is enforced at create and re-verified from `docker inspect`
before and after start: read-only rootfs, `--cap-drop ALL`,
`no-new-privileges`, seccomp, non-root user, cgroup cpu/mem/pids bounds at
the plan values, and a mount table of exactly `/cellinit` (ro bind),
`/tmp` (tmpfs), `/work` (one approved bind or scratch tmpfs) plus optional
`/tools/<i>` image mounts. Cells carry no `/state`, no credential stores,
no broker material, no docker socket and no other principal's workspace.

Network is `none` by default. A plan with reviewed non-loopback egress gets
a per-cell internal network plus a Squid CONNECT allowlist sidecar; brokered
credentials get a per-cell `cred-proxy` sidecar that injects the real
Authorization header on allowed upstreams — the secret travels to it via
`docker cp`, never argv/env/inspect. Caller-visible artifact and toolbox
images must be `image@sha256:` digest-pinned.

Empty `200`/`204` responses are not proof; missing or mismatched receipts
deny the call. A runner without a configured `/cli-exec` channel fails
closed — bounded CLI may not silently degrade into unprotected host exec.

## Lifecycle ladder

`workload.lifecycle` selects the cell tier: `ephemeral` (default —
create/run/remove per call) → `task` (warm per principal+job, short idle
reap, no workspace bind) → `binding` (warm per principal+binding) →
`toolbox` (warm per principal+context+toolset, pinned member images mounted
as read-only `/tools/<i>` layers) → `shared-pool` (operator pool for
`stateless` defs with no workspace). Warm reuse re-inspects the cell and
runs the in-cell canary (`/tmp` clean, canary token intact, no stray
processes) before every exec; drift quarantines the cell — killed, audited,
replaced — never reused. The kill ladder is job end, idle TTL, disable,
revoke (`/cli-release`), max-age and max-reuse; the warm table is capped
and evicts oldest-idle rather than growing without bound.

## Onboarding and lifecycle

User-owned CLI onboarding reuses the M7 path unchanged: `self-install` grant
gate, `prepare_source` with a `cli` spec (direct command) or a `cli` spec
with immutable `source` + `binary` (artifact), protected credential
elicitation on the loopback form, confirm nonce, enable, disable, revoke,
remove, rotate and version-bump re-registration. Identity comes from the
authenticated envelope; model arguments cannot select owner, locator,
backend or policy. Communication Hub must not intercept these requests —
they travel the Hermes → ToolHub control MCP like every other onboarding
call.

CLI artifacts (issue #100) reuse the M5 import contract: source pinned to an
immutable Git commit, recipe + restricted build + OCI quarantine +
provenance/SBOM evidence recorded, and published as a user-scoped
definition whose `source.image`+`source.digest` pin the verified image
manifest and whose `source.command` is the binary's absolute guest path.
The cell executes that image directly — no binary is extracted to the host,
so the executed bytes are exactly the reviewed layer content. Floating
branches and mutable tags are not sources. Repository sources without an
upstream `Dockerfile` run a generated multi-stage `cli` recipe producing a
minimal non-root runtime image; a present upstream Dockerfile is verified
and kept as the recipe. Release sources produce the `cli-release-v1`
scratch recipe described above. Build workers and workloads never
read ToolHub ciphertext, Hermes homes, chat transcripts or another user's
state.

## Failure modes — all fail closed

Cross-user resolution, revoked/disabled bindings, stale binding revisions,
missing grants, off-allowlist executables, missing credentials, missing or
mismatched isolation receipts, and controller unavailability all deny before
any exec. Credential values appear only in the selected CLI process
environment — never in logs, catalog output, Hermes config or MCP HTTP
responses.
