---
description: toolhub — capability gateway: tool admission, governance evaluation, connector lifecycle, audit ledger.
last_verified: 2026-10-25
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
- **Team connectors**: `shareable: credential` shares one team credential
  across per-member workloads; `shareable: connection` shares one credential
  and one workload. Each member binding lazily mints one Broker grant,
  attributed to the member binding and signed under the owning team context —
  grants live in a side ledger, never inside the shared ref. Member removal,
  connection removal, rotation and disable all retire the member's grants.
- **Browser connectors**: Playwright workloads are ordinary isolated
  connectors with a persistent per-connection profile volume, egress only
  through the sidecar proxy and enforced CPU/memory/PID/shm budgets. The
  owner-only viewer mints one-time nonces, spends them into short-lived
  sessions (sliding 15 min, 2 h cap), polls JPEG frames and forwards a
  bounded browser-tool allowlist — scripting, network, file and close tools
  are denied.
- **Admin diagnostics** (`admin.go`, ADR-0077): a `hermes-admin` principal
  enrolled in `toolhub-tokens.json` with `"scopes": ["admin"]` gets read-only
  forensics — `GET /v1/admin/onboardings`, `GET /v1/admin/diagnose/<id>`,
  `POST /v1/admin/inspect-source` (bounded file list + literal-grep snippets
  of the pinned upstream commit; never whole file bodies, credentials,
  nonces or wizard codes). Calls are serialized, paced ≥1 s apart and
  audit-tagged `admin.diagnostics`. A control-plane bearer +
  `X-Hub-Principal` cannot reach them: the scope must live on the presented
  token itself.

## State

`connector_list` excludes removed, revoked and superseded onboarding records
without deleting their history. Its candidates expose `definition_id`,
`onboarding_phase` and the transport of the recorded definition version when
known. Owner-matching personal connections and active-team memberships expose
`connection_id` and `connection_status` separately from onboarding phase.
Missing fields remain unknown; none of these fields proves provider access.
Personal onboarding records for an existing owned connection are deduplicated
within the caller's exact context/runtime/policy. The current credential reference
outranks legacy blank references; newer creation time and then ID break ties.
Stale explicit references are excluded. Owned connections without onboarding
remain visible, with no invented setup phase; their format is resolved from
the pinned definition when available. Connection candidates expose the verified
owner. Pending onboarding without a connection has no connection owner field.
Connection candidates carry the last credential check time, its visibility
(`active`, `inactive` or `unknown`) and a typed cause when one is known.
`active` means the Broker or local credential backend saw the stored reference;
it does not establish provider access or workload admission.
For an exact connection ID without an owner onboarding, `connector_status`
returns the same scoped candidate facts, including team connections visible to
current members. `state: done` means the read completed, not that the provider
works. Membership is rechecked; this read path does not change the lifecycle
selector's denial of member mutations on team connections.
The legacy control `status` body preserves `phase` as onboarding history and
adds `connection_status` and credential check evidence only for an
owner-matching connection. `connector_status` by current onboarding ID includes
the same scoped candidate as `connector_list`. An `enabled`
onboarding now suggests `diagnostics`, never `ready`: even an active registry
record does not prove admission, workload readiness or provider access.
`GET /v1/connectors` now includes this same scoped `inventory` next to its
existing projected `connectors` tool groups. `/connections` renders connection
state even when no tools project and merges projected tools into that row.
Projected-only groups without a recorded format are labelled `unknown` rather
than presumed to be MCP.
Configured service environment is reported as configured, not verified ready.
This is still short of SPEC-0069: onboarding-based status, binding-only tools,
admission/workload/provider evidence and every channel representation need
the same complete fact model.

Credential checks preserve references on a Broker `404`: it can also mean the
actor cannot see the credential. The typed cause is
`credential_visibility_unknown`. Empty/unrecognized credential status and
transport errors are also failed checks, not evidence of credential deletion.
These errors do not automatically request reauthorization, replace a reusable
credential or discard a rotation target. A visible `deleted` credential can
still enter the supported fresh-request flow.

Reconciler applies credential, recipe and tool-surface observations only while
the connection revision and state match the checked snapshot. Concurrent
deletion or revocation therefore wins over a late probe. One connection's
credential purge or store mutation error no longer stops independent checks.
Degraded connections are rechecked for credential visibility without being
silently reactivated; an inactive credential blocks later recipe and surface
probes in the same pass. Unknown credential checks wait one minute between
attempts and persist the retry state across restarts. After five failures,
checks run at most once per hour; a successful check or credential rotation
clears that retry state. Rotation also clears check evidence tied to the old
reference. Recipe check failures also persist a one-minute retry delay across
restarts and enter a one-hour cooldown after five failures; a successful
recipe check or credential rotation clears its retry state. Credential-recipe,
surface and mutation failures still lack durable bounded retries; automatic recovery of verified
degraded connections is pending.

Reconciliation signs team credential checks under the Broker context recorded
by the operator's team-share operation. It uses the `toolhub-reconciler` service
actor and requires an active team and matching Broker reference; a personal
onboarding is not required. This does not replace the pending durable recovery,
state-generation and concurrent-revocation work in SPEC-0069.

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
`HUB_DOCKER_SCOPE` (label-scoped Docker ops), `HUB_CRED_STAGING` +
`HUB_CRED_STAGING_VOLUME` (per-call `credentials.env`/`<name>.secret` staging
on the shared `cred-staging` tmpfs, never on the `/state` bind; the volume
name feeds `DockerBindSource` mountpoint resolution for sibling bind mounts),
`HUB_CLI_ALLOWLIST` (`rg,bash` —
owner-registered bounded CLI commands), `HUB_RECIPE_CATALOGS`,
`HUB_AGENT_EXEC_MODE=supervisor` (agent-tools exec via docker exec, never the
agent network), `HUB_CAPABILITY_MODE`/`_PROFILE_ID`/`_GENERATION`/`_ENVIRONMENT`
(managed mode), `HUB_CREDENTIAL_BROKER_*` client env, `HUB_BUILD_SECCOMP`
(rootless buildkit profile), `HUB_COMMUNICATION_CONTROL_URL`.

## Boundaries

- Credential values live only in the broker (ADR-0049); ToolHub holds refs.
- Per-call credential material (`credentials.env`, `<name>.secret`,
  `credentials.runtime.env`) stages under `HUB_CRED_STAGING` — a tmpfs named
  volume shared by `toolhub` and `workload-controller` — and is wiped after
  admission. Nothing credential-bearing lands on the `/state` bind or any
  other persistent filesystem.
- Workload admission goes through `workload-controller` — ToolHub never
  creates a cell from model-supplied config.
- Agent-tools executor runs inside the owning runtime container via
  supervisor docker exec — the one control path that never crosses the agent
  network.
- Model cannot issue grants; every authority record needs a matching human
  `Confirmation` digest (docs/rbac.md).
