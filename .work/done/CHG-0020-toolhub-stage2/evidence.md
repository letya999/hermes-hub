---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0020 M2 ToolHub stage 2

The stage-2 implementation adds an opt-in stateless streamable-HTTP ToolHub
gateway, a shared projected-tool resolver for list/call, private MCP HTTP
backend adapter, bounded direct-exec CLI runner and per-user/per-job workspace
lifecycle. Runtime wiring defaults to the in-stack endpoint: rendered spaces write
`HUB_TOOLHUB_ENDPOINT=http://host.docker.internal:8090/mcp` into `runtime.<env>.env` unless the
operator sets an explicit or empty value in `secrets.<env>.env`, while
`HUB_TOOLHUB_AUTOSTART=true` starts `hub-toolhub` against `HUB_TOOLHUB_STORE`.

Evidence completed locally:

- `go test -race ./internal/toolhub` and `go test -race ./internal/devcheck` passed;
  the final full run records the exact repository coverage below.
- `just check` passed with 85.03% total Go statement coverage, race/shuffle,
  format, vet, pinned staticcheck, docs and actionlint.
- `gosec ./internal/toolhub` passed with 0 issues and 6 narrow, justified
  `#nosec` annotations covering validated command/process and secret-boundary
  operations.
- `just docker-check` passed with Docker 29.4.3 and image
  `4257481689e2`: standalone smoke and pinned Hermes 0.21.0 contract/lifecycle
  passed, including actual five-minute idle removal and original-session cold
  restore. The probe used fixtures and made no live Telegram/provider call.
- `go run ./cmd/devcheck toolhub-contract` failed closed as expected because
  `TOOLHIVE_VMCP_ENDPOINT`, `TOOLHIVE_REMOTE_TOOL` and
  `TOOLHIVE_STATEFUL_TOOL` were not configured. The command therefore fails
  closed instead of treating a fixture or unset endpoint as integration proof.
- With the local vMCP inputs configured, the same probe reached initialize but
  failed at the external `404 Session terminated` continuity boundary described
  below; no ToolHive call success is claimed from that run.
- In an isolated local Docker run, the official ToolHive v0.48.0 source commit
  `c25e508592bc3bcfa10dea99af04daaf341db95a` was built outside the repository.
  `thv` created the remote and digest-pinned stateful fixtures and vMCP
  discovered their namespaced tools. The current vMCP process then returned
  `404 Session terminated` when the MCP session header was reused after
  initialize, so the live vMCP call and gateway contract are recorded as
  unavailable rather than claimed as passed.
- The stateful fixture used a bind-mounted state directory; its marker survived
  the ToolHive stop/start check. This proves only the local fixture persistence
  boundary. vMCP used anonymous auth and local fixtures, so no live provider or
  secret integration is claimed.
- Container MCP definitions now require an exact ToolHive version and
  digest-pinned sidecar references. The Go adapter fails closed unless the
  private `HUB_TOOLHIVE_ADMISSION_ENDPOINT` controller accepts the plan; this
  keeps CPU, memory, PID, filesystem and egress enforcement outside the Go
  gateway.
- `toolhub-hermes-contract` is an integration-only real-Hermes probe. It keeps
  the Hermes container/session volume alive while restarting only the Go
  ToolHub endpoint and requires explicit real vMCP inputs.

Unavailable acceptance evidence remains explicit: target Linux VPS resource and
egress measurements, production-pinned ToolHive sidecar/resource enforcement,
external remote-provider auth, the current vMCP session continuity gate, and
the real-Hermes reconnect probe (blocked by that external vMCP session failure).
`just security` was not required
because no repository dependency or pin changed.
