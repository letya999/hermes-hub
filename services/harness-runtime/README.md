# harness-runtime

## Responsibility

Runs one space workload: the upstream Hermes agent loop, its native tool
executor and the tool modes (`tools`, `tools-exec`, `tools-daemon`,
`exec-pack`). One runtime serves exactly one `(space, agent, policy
digest)` rendered by spawn-controller — a narrower-rights subagent claims
a separate runtime, it is never admitted here. The runtime does not
re-evaluate policy: the spawn-time governance snapshot is the authority
and a revoke takes effect on respawn.

## Owns

- Its own workspace and state directories (`HUB_WORKSPACE`, `HUB_STATE`);
  supervised children run with cwd under state, never the workspace.
- The session table: at most `HUB_SESSION_SLOTS` (default 5, max 64)
  distinct sessions hold an active run; one session runs one run at a
  time (per-session FIFO). Run identity resolves only through this
  table — a caller can name an admission, never create one.
- Native tool execution per the rendered capability passport.

## Does not own

- Runtime lifecycle, policy/spec rendering, isolation decisions —
  spawn-controller.
- Model routing and egress — model-gateway / egress-gateway (M40.12/13);
  the relay binaries stay outside this service.
- Secrets — credential-broker leases only.

## API

| Method and path | Purpose | Effect | Idempotency key |
|---|---|---|---|
| `GET /healthz` | spawn health probe | read | - |
| `POST /v1/messages` | process-mode first message (trusted dev only) | write-local | - |
| `POST /v1/execute` | admit one run for the bound (space, agent, policy) | write | `idempotency_key` |
| `POST /v1/observe` | stream an admitted run's events (NDJSON) | read | - |
| `POST /v1/control` | cancel/approve/reconcile an admitted run | write | `job` + `run` |
| `POST /v1/restart` | respawn the agent loop inside this container | write | - |
| `POST /v1/goal`, `POST /v1/compact`, `POST /v1/skill-apply` | session-adjacent control surfaces | write | - |

`/v1/*` requires `Authorization: Bearer $HUB_RUNTIME_AUTH`; run-bearing
calls additionally carry `X-Run-Delivery` proof. Observe and control
resolve an already-admitted `(job, run, session)` row only — they never
admit. An envelope signed for another policy digest is refused 409.

## State machines

Session slot: `open` -> `held` (run bound) -> `open` on terminal run
status (`completed`, `failed`, `cancelled`, `interrupted`). Admission
waits for a free slot; a second run on a busy session waits on the same
session key, not the pool.

## Tables

None — all state is in-memory plus the state-dir ledgers Hermes owns.

## Environment

The managed container's `HUB_*` environment is exactly the rendered spec
(`internal/stack/compose.go` + the spawn-controller spec): an unknown
`HUB_*` name fails startup. Prefix families `HUB_DEFER_*`,
`HUB_CREDENTIAL_BROKER_RUNTIME_*` and `HUB_MEDIA_*` are materialized whole.

Identity and binding:

| Variable | Default | Meaning |
|---|---|---|
| `HUB_RUNTIME_AUTH` | - | bearer for `/v1/*`; required by `serve` |
| `HUB_RUNTIME_ID` | user | runtime lane id the envelope must name |
| `HUB_RUNTIME_GENERATION` | - | generation check on observe/control |
| `HUB_USER_ID` / `HUB_PRINCIPAL_ID` | `me` | principal the envelope binds |
| `HUB_CONTEXT_ID` / `HUB_CONVERSATION_ID` / `HUB_DELIVERY_TARGET_ID` | - | envelope context fields |
| `HUB_ORGANIZATION_ID` / `HUB_ORGANIZATION_ROLE` | `personal` | tenant scope |
| `HUB_SCOPE_KIND` | user | `user`, `organization` or `group` scope |
| `HUB_POLICY_VERSION` | `policy-1` | policy digest the envelope must carry |
| `HUB_PASSPORT_REVISION` | - | mounted `/config/revision` must match |
| `HUB_CAPABILITY_PROFILE_ID` / `HUB_CAPABILITY_GENERATION` / `HUB_CAPABILITY_ENVIRONMENT` | - | managed capability identity |
| `HUB_FEATURES` | - | rendered feature list |
| `HUB_ENVIRONMENT` | - | `dev` or `prod` |
| `HUB_EXECUTION_SUBSTRATE` | - | backend marker (docker/firecracker) |
| `HUB_EXTERNAL_ID` | - | transport identity for group scopes |

Topology and plumbing:

| Variable | Default | Meaning |
|---|---|---|
| `HUB_STATE` | `/state` | runtime state dir; children run with this cwd |
| `HUB_WORKSPACE` | `/workspace` | agent workspace |
| `HUB_WORKSPACE_BOUNDARY` | `/config/workspace.yaml` | writable/read_only contract file |
| `HUB_WORKSPACE_RO` | - | comma-separated read-only workspace paths |
| `HUB_RUNTIME_LISTEN` | `0.0.0.0:8080` | contract listener |
| `HUB_RUNTIME_MODE` | - | lane mode |
| `HUB_RUNTIME_SUPERVISOR_URL` | - | supervisor callback base |
| `HUB_HERMES_API_HOST` / `HUB_HERMES_API_PORT` | `127.0.0.1` / `8642` | upstream Hermes API |
| `HUB_HERMES_CONFIG` | - | upstream config pin |
| `HUB_TOOLHUB_ENDPOINT` / `HUB_TOOLHUB_URL` / `HUB_TOOLHUB_CONTROL` / `HUB_TOOLHUB_STORE` | - | ToolHub addresses and store root |
| `HUB_TOOLHUB_TOKEN_ENV` | `HUB_RUNTIME_AUTH` | env name carrying the ToolHub token |
| `HUB_TOOLHUB_RECONNECT` | `true` | reconnect the ToolHub stream |
| `HUB_COMMUNICATION_CONTROL_URL` / `HUB_COMMUNICATION_AUTH` | falls back to `HUB_RUNTIME_AUTH` | communication-hub callback and token |
| `HUB_MODEL_RELAY_URL` / `HUB_MODEL_RELAY_AUTH` / `HUB_MODEL_RELAY_ADMIN` | - | model relay edges |
| `HUB_EGRESS_PROXY` | - | reviewed egress proxy for run HTTP |
| `HUB_EXPOSURE_CONFIG` | - | sish exposure config path |
| `HUB_SSH_CONFIG` / `HUB_SSH_WRITE` / `HUB_SSH_SHELL` / `HUB_SSH_TUNNEL` | - | ssh capability contract |
| `HUB_STT_URL` | - | speech-to-text sidecar |
| `HUB_BROWSER` / `HUB_MEET` / `HUB_BROWSER_HOST` | `false` | browser grant and CDP host |
| `HUB_FC_INGRESS` / `HUB_FC_PROXIES` | - | firecracker agent endpoints |
| `HUB_KUBERNETES_POD_NAME` / `HUB_KUBERNETES_POD_NAMESPACE` | - | pod identity (M40.15) |
| `HUB_HOSTING_URL` / `HUB_HOSTING_TOKEN` / `HUB_HOSTING_TOKEN_FILE` | - | hosting-hub control |
| `HUB_VAULT` | - | vault mount marker |
| `HUB_PUBLIC_TARGET` | - | public ingress target |
| `HUB_LISTEN` | - | process-mode listen address |
| `HUB_TRUSTED` / `HUB_SERVICE_TOKEN` | - | trusted dev process-mode gate |

Policy surface (all rendered; never read from workspace):

| Variable | Default | Meaning |
|---|---|---|
| `HUB_TOOL_POLICY` | - | spawn-time governance snapshot path |
| `HUB_TOOL_MANIFEST` | - | rendered tool manifest path |
| `HUB_TOOLS_RO` | - | read-only capability families |
| `HUB_NATIVE_TOOLSETS` | - | reviewed upstream toolset carve-outs |
| `HUB_ORG_SCOPED` / `HUB_ORG_ACTIONS` | - | org-scope tools and actions |
| `HUB_SELF_ENV_KEYS` / `HUB_PROTECTED_ENV_KEYS` | - | env self-service contract |
| `HUB_MANAGED_MODEL_ID` / `HUB_MANAGED_MODEL_URL` | - | model identity for preflight replay |
| `HUB_MANAGED_WEB` / `HUB_MANAGED_IMAGE_GEN` / `HUB_MANAGED_BROWSER` / `HUB_MANAGED_CONTEXT` / `HUB_MANAGED_MEMORY_VAULT` / `HUB_MANAGED_KNOWLEDGE_UI` | - | grant JSON blocks replayed in-container |
| `HUB_BOOTSTRAP_GOAL` / `HUB_BOOTSTRAP_REVISION` | - | seeded goal on first boot |
| `HUB_SESSION_REVISION` | - | session spec revision |
| `HUB_SESSION_SLOTS` | `5` | concurrent sessions bound, 1-64 |
| `HUB_RUN_BUDGET` / `HUB_RUN_STALL_TIMEOUT` | - | run wall-clock and stall bounds |
| `HUB_RUN_VERIFY_URL` / `HUB_RUN_VERIFY_AUTH` | - | supervisor verify callback |
| `HUB_PERSISTENT_HERMES` | - | keep the agent process across runs |
| `HUB_SKILLS_EFFECTIVE` / `HUB_SKILLS_EFFECTIVE_RW` / `HUB_SKILLS_META` / `HUB_SKILLS_PLATFORM` / `HUB_SKILLS_STORE` / `HUB_SKILLS_ORG` | - | skills mount contract |
| `HUB_AUDIT_LEDGER` | - | audit ledger path |
| `HUB_DEFAULT_LANG` | `en` | operator locale |
| `HUB_SHARED_GID` | `1000` | shared group id for mounts |
| `HUB_HH_ENABLED` | - | hh.ru integration flag |
| `HUB_SELFTEST_CHILD` | - | spawn self-test marker |

## Extension point

`tools`/`tools-exec`/`tools-daemon`/`exec-pack` argv modes execute native
tools inside this boundary; `HUB_DEFER_*` materializes deferred env into
those children.

## When a dependency is disabled

| Dependency | Behaviour when disabled |
|---|---|
| ToolHub | native tools still serve; ToolHub-backed tools refuse |
| model endpoint | runs fail at the model call; the contract listener stays up |

## Tools it executes

Every cataloged native tool the capability passport admits — files,
documents, media, context, skills, ssh (broker lease), browser —
enforced inside this boundary; nothing executes on the control plane.
