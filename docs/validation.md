---
description: Measured CHG-0009 validation evidence and unverified boundaries.
last_verified: 2026-09-10
---
# Delivery evidence — CHG-0009

The local `go test ./...` suite passes on Windows. It covers peer scope initialization,
kind/ID/path checks, organization policy, persistent service mount boundaries, runtime
HTTP authentication and replay, cross-scope queued jobs, migration dry-run/apply,
symlink/unrelated-data/active-runtime refusal, and existing MCP/self-service behavior.

The private contract is tested with an in-process HTTP server. That proves request,
authorization and replay behavior only; it does not claim a live Docker network,
Telegram Bot API, Hermes provider, OAuth login or external service integration.

`just check`, `just security` and `just docker-check dev|prod` are delivery gates and
must be run on a host with their required toolchains. Their results are recorded here
only after execution; mock or unit tests never substitute for live provider acceptance.

The migration report contains paths, object counts and aggregate checksums. Tests verify
dry-run non-mutation, recoverable source directories, runtime-state mapping and refusal
of symlink, active-runtime and unrelated-destination cases. No source volume or Docker
volume is deleted by the migration implementation.
