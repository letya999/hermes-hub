---
description: Pinned Hermes HTTP API contract for persistent sessions and runs.
last_verified: 2026-09-11
---
# ADR-0012: Validate the pinned Hermes API contract

Accepted 2026-09-11.

## Decision

Use Hermes Agent's authenticated API server as the future runtime-adapter contract,
not undocumented latest APIs or the TUI transport. Pin and verify commit
`869228cab4a8276d3b4c78da9d9939670c47bd0f` (version `0.21.0`) before implementing a
long-lived adapter. Durable runs use `POST /v1/runs` and `Idempotency-Key`; sessions
use `/api/sessions`; event delivery uses `/v1/runs/{run_id}/events` SSE.

## Consequences

The API server gives restart-safe idempotency and marks an ownerless non-terminal run
`interrupted`. It also runs tools on its own host (`split_runtime=false`), so the
current private Go runtime remains the trust boundary and the API server stays an
opt-in, internal compatibility target. Cron remains gateway-owned and can coexist
with API runs. Unsupported admin/memory-write/audio surfaces are not reimplemented.

## Evidence

`go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test` runs against
the real pinned image and fails on version/commit drift or contract changes. Mocks are
not accepted as integration evidence.

Related: SPEC-0011, ADR-0009, ADR-0011 and CHG-0012.
