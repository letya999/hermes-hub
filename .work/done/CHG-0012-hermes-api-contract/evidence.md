---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0012 pinned Hermes API contract

The pinned image contains Hermes Agent `0.21.0` at commit
`869228cab4a8276d3b4c78da9d9939670c47bd0f`. The real integration command is:

```text
go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test
```

It starts the upstream API server with an isolated named state volume and verifies the
capability document, durable run idempotency (same payload replay and different-payload
409), persistent session create/read/messages, run status and SSE, cancellation, the
approval boundary, `/api/jobs` availability alongside the gateway scheduler, and a
restart where an admitted non-terminal run is replayed as `interrupted`. The probe uses
safe dummy provider settings and makes no external provider call; it therefore proves
the HTTP contract and restart semantics, not model quality or account access.

The API server's `split_runtime=false` and server-side tool execution are recorded as a
trust-boundary limitation. TUI JSON-RPC session methods are not treated as an HTTP
contract, and unsupported admin/memory-write/audio/realtime endpoints are not claimed.
If the probe fails, Docker delivery is blocked; rollback is to the previous image and
code revision, with no data migration.
