---
description: toolhub — capability gateway: tool admission, governance evaluation, connector lifecycle, audit ledger.
last_verified: 2026-10-20
---

# toolhub

`cmd/toolhub` → `internal/toolhub`. The capability gateway every projected
tool call crosses. Listens `0.0.0.0:8090`, host-bound to `127.0.0.1:8090`.

## Responsibilities

- **Projection + dispatch** (`governance_store.go`): evaluates
  `Governance.Evaluate` (docs/rbac.md) per call, projects only allowed tools,
  logs admit/allow/deny with the full identity tuple.
- **Connector lifecycle** (`api/`): the nine `connector_*` tools drive
  onboarding records through `preparing → review → awaiting-credentials →
  awaiting-confirm → confirmed → enabled`, then `disabled`/`revoked`/`removed`
  with the finalizer ladder (ADR-0056).
- **Cells** (`cell*.go`): bounded CLI/MCP tool execution in disposable
  containers seeded by `cellinit` (see services/cell-proxy.md).
- **Catalog** (`catalog.go`, `artifact_catalog.go`): prepared connector
  registry, CI-built images (ADR-0057), content-addressed recipes
  (`recipes/<sha256>`, ADR-0053).
- **Endpoint**: one stateful MCP server per authenticated owner token;
  projection changes emit `notifications/tools/list_changed` and reload the
  persisted store — no Hermes restart needed for list changes (a parked
  server still needs its runtime restarted before retry). Backend hop pins
  `MCP-Protocol-Version: 2025-11-25` for legacy-wire servers (Serena 1.5.x);
  standalone SSE off — calls are request/response.
- **Multi-tenant auth**: `toolhub-tokens.json` (`HUB_TOOLHUB_TOKENS_FILE`)
  maps sibling-space bearers to full identity envelopes; render/supervisor
  write each sibling's `HUB_RUNTIME_AUTH` in place (same inode → live bind
  update); a bearer miss reloads the file. The infra owner's own token is
  never copied. `infra: true` space renders/owns the shared control plane
  once; `infra: false` renders only runtime outputs. Rendered spaces default
  `HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp` on the shared
  `hermes-hub-runtime` network; explicit env override, empty = opt out.
- **Control-plane reads**: `POST /v1/tool-call` (owner-pinned, read-effect
  tools only, bounded args — powers `mcp_tool` watch probes and
  `delivery=stream` polls) and `GET /v1/connectors` (caller-scoped inventory
  for communication-hub when its local registry is absent; sibling token or
  supervisor bearer + `X-Hub-Principal`).
- **Runtime restart relay**: ToolHub cannot write a spawned runtime's state —
  projection changes go to the supervisor as `POST /v1/restart-request`
  (identity triple = lookup key, not a grant); the runtime's own endpoint
  writes the deferred marker, applied immediately when idle.

## State

All under `/state` (the space's `runtime/` bind):

| Path | Contents |
|---|---|
| `toolhub/store.json` | onboarding records, connections, definitions |
| `toolhub/audit.jsonl` | `HUB_AUDIT_LEDGER` — per-call decisions |
| `toolhub/governance.json` | `HUB_TOOL_GOVERNANCE` — governance document |
| `credentials/store.enc` | `HUB_CREDENTIAL_STORE` — sealed credential refs |
| `artifacts/` | `HUB_ARTIFACT_DIR` — trusted connector artifacts |

## Key env

`HUB_TOOLHUB_STORE`, `HUB_TOOLHUB_LISTEN`, `HUB_TOOLHIVE_ADMISSION_ENDPOINT`
(`http://workload-controller:8545/admit`), `HUB_DOCKER_HOST_ROOT` +
`HUB_DOCKER_SCOPE` (label-scoped Docker ops), `HUB_CLI_ALLOWLIST` (`rg,bash` —
owner-registered bounded CLI commands), `HUB_RECIPE_CATALOGS`,
`HUB_AGENT_EXEC_MODE=supervisor` (agent-tools exec via docker exec, never the
agent network), `HUB_CAPABILITY_MODE`/`_PROFILE_ID`/`_GENERATION`/`_ENVIRONMENT`
(managed mode), `HUB_CREDENTIAL_BROKER_*` client env, `HUB_BUILD_SECCOMP`
(rootless buildkit profile), `HUB_COMMUNICATION_CONTROL_URL`.

## Boundaries

- Credential values live only in the broker (ADR-0049); ToolHub holds refs.
- Workload admission goes through `workload-controller` — ToolHub never
  creates a cell from model-supplied config.
- Agent-tools executor runs inside the owning runtime container via
  supervisor docker exec — the one control path that never crosses the agent
  network.
- Model cannot issue grants; every authority record needs a matching human
  `Confirmation` digest (docs/rbac.md).
