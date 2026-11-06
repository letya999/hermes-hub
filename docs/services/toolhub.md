---
description: toolhub — capability gateway: tool admission, governance evaluation, connector lifecycle, audit ledger.
last_verified: 2026-11-06
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
- **Shared connector consumers**: stateless `shared` workloads track every
  active binding as a consumer; release stops the process only when the last
  consumer leaves, and the reconciler never purges one with live consumers.
  `workload.retention` is `demand` (default), `warm` or `persistent`
  (persistent needs a stateless shared workload and skips idle reaping).
- **Recovery budget**: re-admitting a connection whose recorded workload is
  not running counts as one recovery; after three the connection degrades
  instead of restart-looping.
- **Per-run call ceiling**: `HUB_TOOL_CALLS_PER_RUN` bounds tool calls under
  one job correlation (default 256); uncorrelated control-path calls are
  never bucketed.
- **Cells** (`cell*.go`): bounded CLI/MCP tool execution in disposable
  containers seeded by `cellinit` (see services/cell-proxy.md).
  Skill credential refs are leased from the calling principal's running
  runtime through the supervisor; ToolHub never reads its own self-env for
  another principal. An unavailable runtime yields no ref lease.
- **Catalog** (`catalog.go`, `artifact_catalog.go`): prepared connector
  registry, CI-built images (ADR-0057), content-addressed recipes
  (`recipes/<sha256>`, ADR-0053).
- **Endpoint**: one stateful MCP server per authenticated owner token;
  projection changes emit `notifications/tools/list_changed` and reload the
  persisted store — no Hermes restart needed for list changes (a parked
  server still needs its runtime restarted before retry). Backend hop pins
  `MCP-Protocol-Version: 2025-11-25` for legacy-wire servers (Serena 1.5.x);
  standalone SSE off — calls are request/response.
- **Per-user vault resolver** (`vault_resolver.go`): `HUB_VAULTS_BY_PRINCIPAL`
  names the stack-rendered bindings root — one mount per enrolled,
  memory-enabled, environment-matching principal. Startup scans it into a
  binding table; `Resolve(principal, environment)` then requires an
  identity-valid principal present in the table plus the live published
  passport record (`HUB_PASSPORTS_BY_PRINCIPAL`) declaring the same
  space/environment with memory enabled — only then is the vault opened,
  under a per-user lock so one user's blocked operation never serializes
  another's. A caller-supplied path is never consulted.
- **Exact-run memory admission** (`memory_run.go`, `api/run_status.go`):
  a `memory.*` call is admitted only when the published passport gate passes
  AND the claimed job/run pair verifies against the supervisor lifecycle
  record (`Gateway.Runs`, `GET /v1/jobs/<id>` over the existing
  `HUB_RUNTIME_SUPERVISOR_URL` + `HUB_COMMUNICATION_AUTH` control channel).
  `X-Hub-Job-ID`/`X-Hub-Run-ID` select the record; the record's own fields
  must prove a `running` private run bound to the caller's principal,
  context, runtime, environment and current generation. Group and
  background/system records are explicit `RunKind`s that never admit; an
  unconfigured or erroring authority denies. The gate sits inside
  `AuthorizeProjectedCall` so direct, CLI and nested calls share it and
  every denial lands before the backend.
- **ToolHub-owned memory search** (`memory_search.go`): `memory_search`
  executes inside ToolHub (`runtime:toolhub` executor) — the verified
  envelope's principal/environment resolves the vault through the #513
  resolver, the #514 gate runs first, and the shared `internal/memory/search`
  indexer answers with the same hit shape (evidence refs, `status`,
  `untrusted`). Arguments and headers never select user, vault or audience;
  an unresolvable vault reports `status=unavailable` with no local fallback.
  The in-runtime `handler:memory` path no longer serves `memory_search`
  (`memory_card` still owns vault writes) and the runtime container carries
  no vault mount.
- **Trusted capture** (`memory_capture.go`): `/v1/memory-capture` admits only
  the communication service bearer plus an enrolled principal. It compares
  the event's user, scope, trigger, channel, flags, run ID and content hash
  with supervisor's durable job fact; completed replies use the historical
  `completed` record, while model memory calls still need a live private run.
  The vault write is confined to the authenticated user's binding and
  rechecks published `memory.enabled` and the `memory.remember` grant under
  that user's lock. Producer/event ID is the durable dedup key. Denied and
  sensitive events never write a leaf.
- **Multi-tenant auth**: `toolhub-tokens.json` (`HUB_TOOLHUB_TOKENS_FILE`)
  maps sibling-space bearers to full identity envelopes; render/supervisor
  write each sibling's `HUB_RUNTIME_AUTH` in place (same inode → live bind
  update); a bearer miss reloads the file. The infra owner's own token is
  never copied. `infra: true` space renders/owns the shared control plane
  once; `infra: false` renders only runtime outputs. Rendered spaces default
  `HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp` on the shared
  `hermes-hub-runtime` network; explicit env override, empty = opt out.
- **Managed capability admission** (six-passport spaces): a passport's
  `tools` projection reaches the live MCP surface only after four operator
  acts on the protected store — `hubctl capability --kind policy` (ceiling
  plus the principal as sole member), `--kind profile` (selections narrowed
  to the passport's admitted capability families, allows inside the ceiling,
  `core_tools` a subset of selection names capped at 20, and every
  `implementation_digest` pinned to the *bound* `hub-agent-tools` version),
  `--kind agent-tools` (installs the cataloged definition and mints the
  binding under the principal's exact context/runtime/policy envelope;
  `--version <v>` repins onto an already-registered definition when the
  profile's selections pin that version), and
  sibling token enrollment via the infra space render. The MCP session then
  projects `system_guide`/`tools_list` plus the bounded core directly;
  extended selections stay live through `tools_list` and `invoke` under the
  connectors contract. The periodic projection refresh applies the same
  core bound as the initial build — an extended selection re-admitted by a
  profile revision never regains a direct schema. Profiles whose identity
  (principal/context/runtime/environment/generation/policy_version) or
  `policy_revision` pin diverges from the runtime envelope resolve to an
  unauthorized projection: the session degrades to meta tools, never to a
  widened surface. On `policy_version` rotation the passport-derived
  `DeriveRights` pass repins each active credential-free binding covered by
  the profile's selections onto the new pv through the same `Enable`
  admission — the binding follows the pv the platform already moved the
  profile and sibling tokens to; credential-bearing bindings still require
  the explicit `--kind` re-issue because their connection/credential
  revisions are operator state.
- **Control-plane reads**: `POST /v1/tool-call` (owner-pinned, read-effect
  tools only, bounded args — powers `mcp_tool` watch probes and
  `delivery=stream` polls) and `GET /v1/connectors` (caller-scoped inventory
  for communication-hub when its local registry is absent; sibling token or
  supervisor bearer + `X-Hub-Principal`).
- **Runtime restart relay**: ToolHub cannot write a spawned runtime's state —
  projection changes go to the supervisor as `POST /v1/restart-request`
  (identity triple = lookup key, not a grant); the runtime's own endpoint
  writes the deferred marker, applied immediately when idle.
  The desired projection revision and last acknowledged restart revision are
  stored in the ToolHub snapshot. After ToolHub restarts, an unacknowledged
  revision is posted again; a previously acknowledged revision is not.
  The snapshot also stores the acknowledged
  semantic digest of tools, grants, connection and credential authority. A
  revision change with the same digest advances the acknowledgement without
  another restart. Managed profiles include admitted tools, effective scopes
  and limits, control surface and runtime scope. Team bindings include this
  caller's membership and write authority; unrelated member and observation
  metadata does not change the digest. Unresolvable authority falls back to
  revision-based restarts.
  Each posted revision carries a stable operation ID and
  `tool_projection_change` reason; retries reuse the same ID.
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
Legacy `status` returns the same not-found text for a foreign and an unknown
onboarding ID. Internal lookup reasons distinguish `scope`, `unknown`,
`version` and `terminal` without placing those codes in the agent response;
the remaining expiry/storage classifications are still pending CHG-0224.
File-backed onboarding updates roll back the in-memory record if persistence
fails before snapshot replacement, so a failed nonce refresh cannot appear in
status. If directory sync fails after replacement, the error is reported and
memory retains the same record as the replaced snapshot.
For an awaiting-confirm onboarding, agent status now reports
`next_action=await_user_confirmation`. The MCP `confirm` tool reads status
without consuming a nonce or enabling a binding. Protected pages display the
action and reject confirmation POSTs. Communication stores an opaque Telegram
callback in its existing spool and verifies the sender, chat and expiry before
using the service-only `/v1/confirmations` endpoint. ToolHub checks current
enrollment and authority and atomically binds the verified actor, action digest,
revision and expiry to one durable claim. The safe actor consent record is
saved before binding work. Credential/OAuth/device submission waits for this
separate consent. A failed or uncertain step can require a
fresh link after expiry. A claim write failure before snapshot replacement
restores the unused in-memory record; a directory-sync failure after
replacement reports an error and retains the claimed record in memory.
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
| `toolhub/store.json.initialized` | local initialization fence; a missing snapshot with this marker refuses an empty replacement |
| `toolhub/audit.jsonl` | `HUB_AUDIT_LEDGER` — per-call decisions |
| `toolhub/governance.json` | `HUB_TOOL_GOVERNANCE` — governance document |
| `credentials/store.enc` | `HUB_CREDENTIAL_STORE` — sealed credential refs |
| `artifacts/` | `HUB_ARTIFACT_DIR` — trusted connector artifacts |

ToolHub writes `store.json` through a synced temporary file and rename, then
syncs the parent directory and creates a synced initialization marker. A missing
snapshot with a retained marker refuses a fresh empty Save; the same Store
instance also refuses to rewrite a vanished snapshot. The directory sync is
effective on Unix; the existing Windows sync helper is a no-op, so native
filesystem durability still needs the isolated acceptance gate. If the entire
volume and its marker disappear together, this local fence cannot prove that
the installation was previously initialized.

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
(rootless buildkit profile), `HUB_COMMUNICATION_CONTROL_URL`,
`HUB_PASSPORTS_BY_PRINCIPAL` + `HUB_VAULTS_BY_PRINCIPAL` (per-principal
passport/vault mount roots).

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

## Shared audit capacity

`internal/audit` preserves the initial JSONL file and adds consecutive
`.v1-NNNNNN` segments under an inter-process writer lock. New segments cap at
1 MiB, records at 16 KiB; the ledger admits at most 16 segments, 16 MiB and
65536 records. There is no destructive TTL. At capacity, writes stop with an
explicit error; export/recovery is an operator action. Corrupt/partial records,
unknown schemas and missing segments fail closed without truncation.
For an oversized, unsegmented schema-1-only legacy file, stop its ToolHub and
use `hubctl audit rollover --ledger <absolute audit.jsonl>` for a count/size/
digest plan. `--apply --digest <sha256:...>` holds the audit writer lock,
rechecks every record, writes and verifies a mode-0600 gzip archive named
`audit.jsonl.legacy-<digest>.jsonl.gz`, then replaces the active file with a
schema-2 rollover marker. The uncompressed source is removed only after the
archive is verified and the new file is synced. Schema-2 or segmented ledgers
are refused. The archive retains historical events for offline recovery; the
new live `ReadPage` starts with the rollover marker. There is no automatic TTL
for the archive.

Scoped `ReadPage` returns at most 1000 events; `List` refuses histories that
require pagination. Schema 2 operation records distinguish authorization, execution and delivery;
legacy schema 1 records remain readable. `Ledger.Begin` persists admission and
reserves one maximum completion plus its record/segment budget before returning.
Reservations are recovered by every writer under the same lock. A matching
unsettled target/correlation blocks another admission; `Pending` reports the
scoped unknown intent without guessing whether another process is alive.

For exposure opens and SSH shell/tunnel opens, `cleanup_action` reserves two
additional maximum records (admission and completion). The hold survives a
successful open and is reconstructed by a reopened writer. A `cleanup_of`
record must match the opening action and complete scope, including generation;
it spends only that resource's hold. One cleanup is admitted per opening.
Only a durable succeeded cleanup releases the target fence. A prior unknown
opening remains unknown; a confirmed close does not rewrite its history.

Exposure uses this path: audit failure before open prevents the provider call;
after-effect failure gives uncertain, preserves known handles and prevents blind
reopen. List/status surface uncertain=true. A requested close still attempts to
stop access if audit is unavailable, and reports uncertainty instead of success.
SSH now admits exec, file, shell, tunnel and certificate operations before their
effects, with bounded safe receipts and unknown-outcome fences; cleanup still
attempts access termination if audit fails. Toolhub dispatch uses the same
durable admission and conservative completion
reserve for every call, including legacy projections. Missing audit prevents
backend dispatch; a logger/observer cannot replace the writer. Scope-bound
operation IDs fence repeated calls after recovery. Preflight failure completes
as not_started, business errors are execution outcomes rather than auth deny,
and post-dispatch/checkpoint/output/audit failures remain uncertain with their
intent/reserve protected. Delivery is not_started until the transport sends it.

The legacy owner credential service also admits batches, rotations, terminal
exposure and authorized injection before their effects. Its mutation scope is the
owner, so an unknown batch cannot be repeated under a different key list. Partial
writes retain ciphertext and are quarantined; an audit failure never compensates
by deleting the previous credential. Revocation continues through every access
path even when its receipt is unavailable. Activity exposes typed operation fields
without credential dictionary names. Broker storage and delivery remain separate.

MCP uncertainty is an IsError application result with status=uncertain,
operation_id and retryable=false; private backend payloads are withheld. The
internal read-tool HTTP route returns 409 with the same safe fields, not a
retryable gateway error. Reconcile the existing operation before any repeat.
OAuth setup can refresh/retry once only if trusted backend code proves no
business tools/call was dispatched. A post-dispatch HTTP status, provider error
string or lost response never grants that proof. TLS, Origin and auth controls
remain required. Receipts are reviewed producer identifiers/counts; auth-bearing
URLs, serialized payloads and credential labels are rejected. Toolhub additionally
accepts canonical identifier characters and the fixed validated CLI
isolation/cell/runtime receipt format. Unknown controller labels are not serialized. An interrupted operation with no
verified cleanup remains visible and fenced; no automatic provider inspection,
cleanup or destructive retention is inferred. CHG-0221 acceptance remains pending.
No rollout or live acceptance is implied.
