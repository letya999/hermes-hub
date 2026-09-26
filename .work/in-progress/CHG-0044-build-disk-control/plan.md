---
description: Keep optional connectors out of the hub image and bound Docker rebuild state.
last_verified: 2026-09-26
---

# CHG-0044 Build and disk control

## Root cause

The default image fetched and compiled an unused Slack MCP binary and copied
Docker CLI into every service. Post-build image pruning could not remove old
generations pinned by stopped Compose containers. BuildKit cache had no
project-level bound after ordinary `hubctl build` or `up`.

## Change

- Remove Slack MCP from the default image; ToolHub owns connector artifacts.
- Share core layers between core and control targets; Docker CLI and ToolHive
  exist only in the control target used by ToolHub and workload controller.
- Build pinned CLIProxy in an independent stage so its network fetch and Go
  compile do not block compilation of the hub's Go services.
- Use BuildKit cache mounts for Go modules and compiler outputs across source
  edits, bounded by the ordinary 8 GB builder cache policy.
- Before and after each build/up attempt, remove stopped hub containers on dangling images,
  prune those images and bound the selected builder cache to 8 GB.
- Keep up to 8 GB of BuildKit cache in ordinary `just docker-clean`; full cache
  deletion is explicit with `--deep`.
- Keep named volumes and unrelated containers. No provider mutation.

## Verification

Run `just check`, `just docker-check`, inspect core/control images and compare
fresh versus cached build durations. Windows VHDX compaction is an operator
step after Docker Desktop closes because it requires administrator rights.
