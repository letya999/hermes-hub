---
description: Accepted bounded CLI as a first-class ToolHub transport on the existing admission contract, with executor-container receipts.
last_verified: 2026-10-08
---
# ADR-0033: Bounded CLI through ToolHub admission, not through Hermes config

Status: accepted, 2026-10-08. Milestone 9 (M8); owner instructed the full
milestone per SPEC-0047.

## Context

Several high-value connector surfaces are mature CLIs already pinned inside the
released ToolHub image (git, ripgrep, media tools) or owner-built binaries.
Routing them through a synthesized remote MCP or writing `mcp_servers` entries
into Hermes would bypass the ToolHub authorization, credential and audit
boundaries and would duplicate per-owner config that the store already manages.
Conversely, running arbitrary host commands for the model would create a shell
backdoor around every control the project just built.

## Decision

1. Bounded CLI is a first-class `transport` on the immutable ToolDefinition.
   Catalog CLI ships as immutable operator-registered definitions; user-owned
   CLI is registered through `prepare_source` (declarative `cli` spec, or
   immutable artifact source) behind the `self-install` grant and `user`
   publication. Both share the runner, admission, credential-injection,
   projection and lifecycle machinery.
2. The runner never invokes a shell: one allowlisted executable resolved to an
   absolute path once, fixed argv plus typed scalar inputs, an exact declared
   environment, a per-binding workspace cwd under the state root, bounded
   output, timeout and process-tree cancellation.
3. Every call is gated by the existing controller admission contract. The plan
   carries the command, root, working dir and execution policy; the receipt
   must prove running + enforced + matching policy + an explicit isolation
   scope list. The shipped controller proves the claims by inspecting the
   executor container the exec lands in (non-privileged, non-root, bounded
   cgroups, approved mounts/networks) — a real external check, not Go
   self-attestation. Empty 200/204 and missing configuration fail closed.
4. Nothing is written to Hermes `mcp_servers`; the model still sees one
   ToolHub endpoint and the generated MCP default is untouched.

## Consequences

- A ToolHub deployment without a reachable CLI-capable controller runs no CLI
  — catalog entries stay enableable but calls deny with the isolation error.
- The receipt's isolation scopes name only what the controller verified;
  per-principal fs separation inside the shared state mount is policy, and
  per-workload egress host-allowlists are not claimed for host-exec'd CLI.
- New catalog CLIs are pinned binaries in the image; user artifact CLIs land
  under the artifact directory by digest. Neither path installs packages at
  runtime.

## Amendment 2026-10-08: owner spec shape and artifact admission

- User-owned registration rides `prepare_source` with a `cli` spec object —
  not a new op — so grant gating, request-key idempotency, credential forms,
  confirmation and lifecycle all reuse the reviewed control path.
- Owner spec argv is long-option-only (`--flag`/`--flag=value`); positional
  operands are denied because for tools like `rg` they are literal paths that
  would escape the contained workspace. Per-command deny lists block
  program-spawning and file-reading flags; `git` is categorically excluded
  (`--git-dir`/`.git/hooks` execute code from writable data).
- Owner artifact CLI reuses the M5 restricted build + OCI quarantine
  unchanged; the binary is copied out of the verified image into
  `artifacts/cli/<owner>/<image-manifest-digest>/` (staging container is
  `docker create` + `docker cp` — never started), and the definition pins
  the extracted file's sha256 plus the full source provenance. The runner
  re-verifies that digest on every call, so a swapped binary denies stale.
- Dynamic owner plans are admitted by class at the controller: bare commands
  in `user_commands` or absolute paths under `user_artifact_dir`, with every
  execution bound fitting `user_execution`. A missing pipeline or unset
  artifact dir fails closed.

## Amendment 2026-10-08: executor children → controller-owned cells

The M8 audit found four holes in the executor model this ADR accepted:
CLI children inherited the ToolHub container's mounts (`/state`, credential
stores, other principals' workspaces, broker material, docker.sock); the
extracted-binary pin was bypassed by any mutable code the workspace
interpreter chose to run; children shared the control plane's cgroup; and
host exec inherited the controller's network, making per-workload egress
advisory.

Bounded CLI therefore moves from executor attestation to controller-owned
sibling cells:

- The runner posts each call to `/cli-exec` with the immutable plan plus
  command/argv/env/workspace; it never execs locally. Bounded-CLI plans on
  `/admit` are rejected — admission for CLI *is* the cell execution.
- The controller runs `docker create → inspect → receipt → start → exec →
  wait → rm` for `ephemeral` cells, and `create → inspect → canary → exec`
  for warm tiers. The receipt names the `cell_id`, `runtime` and verified
  isolation scopes; it is produced only after the inspected profile matches
  the create request.
- A cell mounts exactly `/cellinit` (static helper, ro), `/tmp` (tmpfs) and
  `/work` (one approved workspace bind or scratch tmpfs), plus optional
  read-only `/tools/<i>` image mounts for toolbox members. No host state,
  no secrets, no socket.
- Network is `none` by default; reviewed egress earns a per-cell internal
  network + Squid CONNECT allowlist, and brokered credentials reach a
  per-cell cred-proxy via `docker cp` — never argv/env/inspect.
- Owner artifacts stop extracting binaries: the verified OCI image is the
  artifact, `source.digest` pins its manifest, and the declared command is
  a guest path inside it. Toolbox members and artifact refs must be
  `image@sha256:` pinned; `HUB_CLI_ALLOWLIST`, `user_artifact_dir` and the
  executor attestation fields are removed.
- The lifecycle ladder (`ephemeral` → `task` → `binding` → `toolbox` →
  `shared-pool`) reuses warm cells only after re-inspect + in-cell canary;
  drift kills the cell. Runtime tiers map `runc|runsc|kata` onto
  `docker --runtime`, missing runtimes fail closed at admit.
