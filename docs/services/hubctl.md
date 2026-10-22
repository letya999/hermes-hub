---
description: hubctl — host-side control CLI: stack lifecycle, builds, spaces, grants, connector controllers.
last_verified: 2026-10-22
---

# hubctl

`cmd/hubctl` — the operator's control binary. It never runs inside the agent
trust boundary: every mutating path is host-side and most state changes require
a human confirmation link minted through the communication gateway.

## Command surface

`hubctl` (no args) prints the authoritative list:

- **Stack lifecycle**: `init`, `org-init`, `up`, `down`, `logs`, `doctor`,
  `render`, `migrate-spaces`, `migrate-toolhub`, `apply`.
- **Images**: `build`, `dev-rebuild` (the only sanctioned local build paths —
  they force BuildKit, prune superseded hub images, cap build cache at 8 GB).
- **Spaces/selection**: `execution-audit`, `select-execution`.
- **Control plane**: `supervisor` (also the container entrypoint),
  `connector generic-controller` (workload admission endpoint, :8545),
  `connector local-controller` (fixed Telegram variant).
- **Operator state**: `catalog`, `artifact`, `tools`, `grant`, `governance`,
  `capability`, `context`, `memory`, `skill`, `routine`, `secret`.
- **Ops & delivery** (ADR-0072): `usage` (token/runtime-seconds report over
  `usage.jsonl`), `watch` (job/runtime transitions to terminal), `image`
  (smoke-gated rollout pins: `promote`/`revert`/`canary`/`status`),
  `replay` (sanitized incident bundle `export` + isolated mock-channel `run`).
  `apply -f` also converges a declarative `space:` manifest.
- **Interactive**: `chat`, `telegram-login`, `meet-auth`, `companion`.

Flags: `--dir spaces/<user>`, `--root .`, `--user`, `--org`, `--env prod|dev`.

## What it writes

`render`/`init`/`up` generate the space compose (`compose.dev.yaml`), config,
and materialized dirs under `spaces/<user>/` — see docs/spaces.md. Nothing in
`generated/` is hand-edited; re-render instead.

## Controller role

`hubctl connector generic-controller` is the workload-admission HTTP endpoint
ToolHub calls (`HUB_TOOLHIVE_ADMISSION_ENDPOINT=http://workload-controller:8545/
admit`). It validates a trusted artifact before a connector cell is created —
the only path that can mint cell workloads.

## Boundaries

- Never invoked by agent code or inside runtime containers.
- Build paths own Docker image hygiene; raw `docker compose build` bypasses
  BuildKit forcing and cache caps — forbidden by AGENTS.md.
- `secret`/`grant`/`capability` mutations write through confirmed flows; the
  CLI is an operator surface, not a self-service agent tool.
