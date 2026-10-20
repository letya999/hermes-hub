---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0019 M2 ToolHub foundation

`internal/toolhub` now validates immutable versioned definitions for remote MCP,
stateful container MCP and bounded CLI workloads. It keeps connections and credential
references separate, derives deterministic binding/tool/workload names, and rechecks
exact identity, owner, current revision and status before admission. The registry
snapshot contains only metadata and opaque credential locators; no secret value is
returned or persisted by this contract.

Targeted `go test -race ./internal/toolhub` passed with 85.7% package coverage. The
tests are control-plane regressions for strict schema/budget/source/mount validation,
cross-owner and stale-policy denial, disabled/revoked bindings, concurrent admission
versus revocation, concurrent rotation, file round-trip and workload lifecycle
metadata. `just check` passed with 85.07% total Go statement coverage. With Docker
29.4.3, `just docker-check` passed on image `3a011b97f1c2`, including standalone
smoke and the pinned Hermes 0.21.0 contract/lifecycle probe; those fixtures made no
live Telegram or external-provider call. The safety scan passed secret scanning and
govulncheck but remains non-zero on 91 pre-existing gosec findings and unavailable
host npm/eslint; no dependency changed in CHG-0019, so `just security` was not
required. The tests and Docker probe do not claim ToolHub gateway, ToolHive/vMCP,
MCP backend or provider integration success.
