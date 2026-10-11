---
description: Current operations and planned scale-to-zero runtime lifecycle.
last_verified: 2026-10-02
---
# Operations

Use `hubctl --user <id> --env dev|prod`; prod is the default. `--dir` remains an
explicit legacy alias. `render` writes generated files under
`spaces/<user>/generated/`, `up` builds and starts the selected runtime, `down` stops
it without deleting data, and `logs` tails the selected Compose project.

Diagnostics are on by default. Each stack's ToolHub collects new
stdout/stderr lines every 15 seconds — scoped to its own Compose project
label plus runtime containers attached to that user's agent network — and
appends them to `spaces/<user>/diagnostics/hermes-diagnostics.txt`. No
export command is needed. The file rolls over at 100 MB, and its small
cursor file keeps the collector from replaying lines after restart. Each
`docker ps`/`docker logs` call is individually deadline-bound and one
collection pass is capped, so a stalled daemon or an oversized log fetch
cannot starve the cycle; stopped containers are drained once and marked,
never retried into failure spam. The host supervisor mirrors its own
request/lifecycle logs to `.local/supervisor.log` (10 MB cap), which each
collector reads read-only and copies only lines naming that space's owner
as a whole token. Compose and supervised runtimes
retain Docker's rotating `local` logs (10 MB × 3 files per container).
Later cycles take at most 5000 lines per container, and one container
whose `docker logs` fails is skipped without stalling the others. Secrets are
redacted at collection time, so the combined file never stores tokens or
passwords in plaintext. To opt out, set `diagnostics: false` in the
infra-owning space's `agent.yaml` and run `hubctl up` again.

Gateway message logs carry IDs, status and text length — never message
bodies. Bracketed class markers (`[voice message]`, `[file message]`,
`[credential input redacted]`) still print, so a crash or restart is
diagnosable by `update_id`, `job_id`, `delivery_id` and status alone. When an
incident genuinely needs message text, set `HUB_LOG_CONTENT_UNTIL` to an
RFC3339 instant in the channel env file and restart: inside that window the
same lines include text after the credential redactor runs, and the value is
clamped to at most one hour ahead so a typo cannot pin capture open. Once it
expires the lines return to `text_len=` automatically — verify by grepping a
collected line for `text_len=` after the deadline.

Users with an explicit `control-operation` grant for `diagnostics` inspect their
own diagnostics through that ToolHub operation. It returns the caller's connector workloads plus bounded,
redacted log lines from their runtime and workload containers. Scope comes
from the authenticated principal, context and live binding state — never from
tool arguments — so host logs, platform containers and other users' data stay
invisible, and revoke/remove shrinks visibility immediately. Queries accept
`tail` (1-500), RFC3339 `since`/`until`, a `search` substring, `severity`
(info|warn|error) and a `workload` selector (workload, binding or definition
ID, or `runtime`). Secrets are redacted again at projection and host-supervisor
lines appear only when they name the caller's own runtime container as a whole
token. Requests are capped per principal and globally, so a single caller
cannot starve diagnostics for others.

ToolHub control operations are denied by default, including `discover`, `status`,
`prepare_source`, `required_credentials`, `confirm`, `enable`, `rotate`, `disable`,
`revoke`, `remove`, `diagnostics` and generic `invoke`. The host operator grants
each selected operation separately; a catalog or self-install grant does not
grant control operations. Self-install additionally requires its own explicit
active grant. `prepare_source` also admits `remote_url`: an owner-scoped hosted
MCP endpoint (public HTTPS only — SSRF and redirect targets deny), probed for a
real MCP handshake, with header credentials collected through the protected form
(SPEC-0045). A matching disabled/revoked grant wins over a matching active grant.
For example, using the protected registry path for the selected deployment:

```text
hubctl grant --user alice --toolhub-store /protected/toolhub/store.json --kind control-operation --operation status --confirm
hubctl grant --user alice --toolhub-store /protected/toolhub/store.json --kind control-operation --operation status --status revoked --revision 2 --confirm
```

Grant revision 1 is the default; changing a record requires a higher revision.
Older writes cannot undo a revoke. The next request on an existing MCP session
refreshes the projection and checks the current grant. These principal-scoped
grants are the legacy path. Managed profiles below do not inherit them.

Every operator mutation requires the explicit `--confirm` human act. Without it
the command prints the canonical review digest and refuses to write; with it the
command stamps `{digest, issued_by, confirmed_at}` onto the record before the
store runs its own checks. The digest is recomputed at admission over the record
minus its confirmation, so a confirmation never covers bytes it did not review,
and `confirmation.issued_by` must equal the record's `issued_by` — one operator
identity cannot stamp for another, and `model`/`hermes` can never mint one.
`--issuer` (default `operator`) names the confirming identity. Unconfirmed
records are rejected by the store itself, not just by the CLI, so persisted
history proves the human act for every policy, profile and grant change.
Grant changes are now durable lifecycle records alongside policy and profile
changes and are replayed on restart in commit order.

### Managed capability policy (implementation primitive)

`hubctl capability` publishes an operator-reviewed JSON policy or profile to the
protected registry. It is a host command, with no corresponding agent MCP method:

```text
hubctl capability --kind policy --file reviewed-policy.json --toolhub-store /protected/toolhub/store.json --confirm
hubctl capability --kind profile --file reviewed-profile.json --toolhub-store /protected/toolhub/store.json --confirm
```

`--kind group --file reviewed-group.json` validates a capability group for
authoring: it prints the canonical JSON and its review digest and never writes
the registry. Groups have no standalone authority; the enclosing policy or
profile revision's confirmation covers them.

A policy contains `schema`, `policy_id`, optional `organization`, explicit
`members`, `revision`, `issued_by`, `issued_at` (RFC3339), `reason`, `status`,
`ceiling`, `defaults` and optional `denies`. Each allow carries the exact
`capability_id`, `implementation_digest`, `action`, `resource`, optional
`connection_id`/`path_prefix`, positive `limits.output_bytes` and
`limits.timeout_seconds`, and optional `expires_at`. Denies carry the same tuple
with zero limits. Defaults must fit within the ceiling; an empty policy grants
nothing. A personal policy has exactly one member and no organization.
Optional `default_groups` hold reviewed snapshots with `group_id`, positive
`revision` and explicit `members` containing the same complete allow tuples.
Members must fit the ceiling. A changed list needs a new group and policy
revision; the group name never authorizes future members.

A profile binds `profile_id`, `principal_id`, `context_id`, `runtime_id`,
`environment` (`dev`/`prod`), `generation` and `policy_version` to `policy_id` and
`policy_revision`. It has the same schema/revision/issuer/time/reason/status
metadata, optional personal `allows`/`denies`, and explicit `selections`.
Optional `allow_groups` use the same fixed snapshot format and cannot exceed
the current ceiling. A changed personal group needs a new group and profile
revision. Existing snapshots are not widened by a later group.
Each selection names one capability, definition ID/version/digest, upstream tool
name, unique projected `name`, and optional connection. A definition's reviewed
tool contract declares its capability, required action/resource pairs, relative
path arguments and any fixed string argument constraints. The digest covers the
whole immutable definition, including schemas and that contract.

The authenticated envelope opts into this evaluator with `capability_profile`,
`environment` and `generation`. A missing/stale profile, membership or policy
denies admission. Organization/default and personal rules intersect the ceiling;
matching denies win. Names and installed credentials do not grant access. All
required action/resource pairs must pass before credential injection. Bare binding
calls, old projected names and legacy control operations are denied for
managed profiles unless the profile itself admits them: `control_operations`
lists the reviewed connector lifecycle operations (discover, prepare_source,
status, required_credentials, confirm, enable, rotate, disable, revoke,
remove, diagnostics) and `self_install` admits the GitHub-source install grant;
both are profile fields, so a principal without them still fails closed.
Call admission requires a durable audit writer
(`HUB_AUDIT_LEDGER` in the endpoint); disk failure prevents dispatch.

Policy/profile publication and its complete issuer/scope/revision history share
one atomic registry snapshot. Reload reconstructs current authority from ordered
history; stale writers and duplicate/conflicting revisions cannot resurrect it.
Publication records contain metadata, never credentials. Call records include
capability/profile revisions, implementation digest, environment and generation;
they omit private arguments and response contents.

`--kind preview --file draft-profile.json --toolhub-store <path>` prints the
human-reviewable old-to-new diff a migration or profile change needs before
apply. It evaluates the unconfirmed draft with the same evaluator and
admission checks publication and dispatch run: each selection is either
admitted with its scopes and tightened limits or quarantined with its deny
reason, and the diff lists `added`, `removed` and `changed` projected names.
The old side is evaluated under its own pinned policy revision (replayed from
capability history when superseded), so a policy bump shows as `changed`
scopes rather than an empty old set. Preview never writes the store and never
requires `--confirm`; applying the identical confirmed record admits exactly
the previewed surface. `--kind connectors` prints the versioned connector
recommendation manifest (SPEC-0041 CP-07): every family is `opt_in`, including
the reviewed `hh` connector, which exists only as the agenttools `hh`
capability behind an explicit grant — never a core or default feature.

These primitives are not a completed deployment boundary. Rendering does not yet
provision managed identities or isolated roots, legacy deployments remain outside
the guarantee, and human consent/migration still need CHG-0065.
Path prefixes are logical authorization constraints, not filesystem containment.
Admission currently holds the existing store lock and a shared disk fence through
the bounded call; a revoke waits for that call before committing. Executor leases,
cancellation and confirmed process-tree stops remain P4 work. Do not roll this out
as full SPEC-0041/0042 compliance.

### Agent-tools executor (hub-owned catalog)

Managed runtimes expose exactly one MCP server — the ToolHub relay. The local
hub tools (files, documents, images, artifacts, services, routines, HeadHunter,
SSH) are not a second MCP surface inside the runtime: they are the built-in
`hub-agent-tools` ToolHub definition, dispatched over a private per-call
executor. Install is an operator act on the protected registry:

```text
hubctl capability --kind agent-tools \
  --toolhub-store /protected/toolhub/store.json \
  --principal alice --context alice --runtime alice --policy-version policy-abc12345
```

The command registers the compiled-in definition and creates its binding for
the supplied identity; nothing is reachable until a confirmed profile selects
individual tools. Dispatch resolves to the `agent-tools` transport and reaches
the executor over `docker exec` (`HUB_AGENT_EXEC_MODE=supervisor` derives the
supervisor container name; `HUB_AGENT_EXEC_CONTAINER=<name>` pins a fixed
container). Render emits the supervisor mode for every `execution.mode:
supervisor` space — managed and unmanaged alike — because the runtime's
`hubctl tools-exec`/`tools-daemon`/`exec-scratch` entrypoints enforce the same
governance snapshot mounted read-only at `/config/tool-policy.json`. Calls travel over `hubctl tools-daemon` — one persistent framed
channel per owning runtime container, multiplexed by frame id, so an
admitted call costs a frame rather than a process spawn. The daemon is
per-user by construction (sessions key on the resolved container); a
resolver that returns a shared container name is the documented extension
point for group- or deployment-wide executors. `hubctl tools-exec` remains
the one-shot form for the pack/apply legs of sandboxed runs. Both carry the
same contract — bounded framed requests with verbatim admitted capability
scopes, bounded results, no authority fields — and the channel fails closed
when unconfigured. Admitted path prefixes become `os.Root` sub-roots inside
the executor, so a handler bug cannot widen a grant into a traversal;
projected tool descriptions name the admitted scope. An agent cannot reach
the Docker socket, so it cannot invoke the executor or forge scopes.
`hubctl tools` remains the unmanaged local stdio surface only.

### The `tools:` surface (operator level)

A space keeps two YAML files with different owners. `agent.yaml` is
operator-owned runtime config — identity, model, timezone, ports,
capability mode/profile pin, hooks, memory and infra flags. `workspace.yaml`
is capability intent — the `tools:` map, `ingress:` channels, workspace
mounts and raw `mcp:` server definitions. Legacy `settings.yaml` keeps
reading and migrates into the same model; keys placed in the wrong file of
the pair are rejected, not merged.

One `tools:` map in `workspace.yaml` is the whole capability surface —
toolsets, ToolHub families, MCP connectors and explicit denies share one
vocabulary. Each entry picks a backend, scalar or mapping:

```yaml
tools:
  terminal: native            # upstream toolset inside the runtime
  file: toolhub               # managed executor, per-call admission
  browser: mcp:playwright     # ToolHub-mediated connector (slack, github, ...)
  github: mcp                 # connector capability (via ToolHub when managed)
  gitea: mcp-raw:gitea        # raw mcp: definition rendered into mcp_servers
  code_exec: off              # explicit deny; absent means denied
  ssh:
    via: toolhub
    access: ro                # write-effect tools denied inside the executor
    tools: {shell: false, tunnel: false}
  file:
    via: toolhub
    only: [file_read, file_list]
    paths: [docs, inbox]
    limits: {output_bytes: 65536, timeout_seconds: 30}
ingress: [telegram]           # transport channels, never agent tools
workspace:
  mounts:
    - {from: docs, to: /docs, mode: ro}
    - {from: org:docs, to: /orgdocs}   # organization dirs are always ro
mcp:
  gitea: {url: https://gitea.example/mcp}   # referenced by via: mcp-raw only
```

The embedded `internal/stack/defaults/{agent,workspace}.yaml` pair seeds
every new space; `init` substitutes `user`/`organization`, allocates ports
and never overwrites. Schema-1 `settings.yaml` files keep reading:
`features`, `native_toolsets` and `disabled_mcp` migrate into this surface
at parse time and are rejected when mixed with the new keys.

`tools:` declares visibility, not authority — ToolHub profiles and grants
in the protected store still govern what an admitted call may do.

### Tool governance (issue 139)

A second host-owned document, `governance.json` beside `store.json` in the
ToolHub dir, decides which sections a space may emit at all. Sections cover
every surface: `native:<toolset>`, `hub:<family>`, the dynamic `toolhub`
projections, `hub:tools` (the executor server), `hub:browser`, and the two
MCP catalogs `user_mcp`/`org_mcp`. Each entry carries deterministic axes —
owner, official, dynamic, prepared, effect — and the trust tier is derived
from them, never stored. Rules are scoped `global`/`org`/`user`/`default`
with `allow`, `deny` or `cap:read`; grants are the only per-principal
widening path: host-created, expiring, nonce-acknowledged, revocable.

Evaluation is fixed: a deny at any real scope wins; then an unexpired grant;
then rules in user→org→global→default precedence (exact sections before
attribute matches); then the inventoried section default; unknown sections
deny. The shipped posture denies `native:terminal`, `native:code_execution`,
`user_mcp` and external/unprepared dynamic installs; everything else ships
allowed. A missing file seeds that posture, preserving a pre-governance
space's already-selected deny-default sections as recorded user-scope
migration allows — `user_mcp` is never preserved by the reader (only the
migration tool records it, once, and never over an existing document).

Enforcement is at every plane: `stack.Read` refuses denied explicit wishes
and filters implicit emissions; ToolHub drops denied tools from `tools/list`
and re-evaluates after admission, before credential injection, on every
call; and the hub executors enforce a rendered `tool-policy.<env>.json`
snapshot (capability ids only — no rules, reasons or other principals' data)
mounted read-only at `/config/tool-policy.json`.

Operator surface — host only, the agent has no write path:

```sh
hubctl governance                              # status for the pinned/derived document
hubctl governance --kind rule --scope user --user alice \
    --section native:terminal --effect allow --reason "reviewed" --confirm
hubctl governance --kind grant --user alice --section user_mcp \
    --expires 72h --reason "host approved" --confirm
hubctl governance --kind revoke --grant <grant-id> --reason "cleanup"
```

Mutations require `--confirm` (the same digest gate as `hubctl grant`).
`--governance` pins the file; otherwise it derives from `--toolhub-store`,
`--dir`, or `HUB_TOOL_GOVERNANCE`/`HUB_TOOLHUB_STORE`. Unlocking `user_mcp`
for a principal is the grant ceremony: the host creates a pending grant, the
agent calls `grant_request`, the user opens the loopback-only
`/grants/<id>?nonce=` URL and confirms — GET renders the grant details, POST
activates. The nonce rotates on each request and expires in 15 minutes, so
a stale link can never activate, and returning the URL activates nothing on
its own.

* `native` re-enables a reviewed upstream toolset for that runtime. The
  reviewed set is the same carve-out list as before — platform adapters,
  `delegation`, `bot_room`, `a2a`, `coding`, `hermes-*` and
  `context_engine` are never approved (they open listeners inside the
  agent or spawn sub-agents with literal toolsets that bypass the
  denylist). Native entries render into `agent.disabled_toolsets` and are
  attested in-runtime via `HUB_NATIVE_TOOLSETS`. They bypass ToolHub
  admission on every call: no per-call check, no call audit, revocation
  only on respawn. A carved-out `terminal` runs without the `code_exec`
  disposable sandbox — bounded by the container's own isolation but with
  full workspace/state reach. Grant it only to runtimes whose operator
  accepts that weaker boundary.
* `toolhub` marks a managed family (files, documents, images, artifacts,
  routines, services, hh, ssh, image_gen, code_exec) as visible intent;
  admission still needs a matching ToolHub grant.
* `mcp[:server]` selects a ToolHub-mediated connector. The connector runs
  on the hub side and calls are admitted through ToolHub — it never
  appears in the runtime's `mcp_servers` section.
* `mcp-raw[:server]` renders a `mcp:` definition (or an organization-
  provided one) directly into the runtime's `mcp_servers`. Managed mode
  only permits organization-scoped definitions.
* `terminal: toolhub` routes the terminal through a bounded-cli cell
  instead of the runtime's upstream toolset: the model gets a `bash -c`
  surface inside a sibling container — read-only root, dedicated cell
  network, no `/state`, no Docker socket, egress cut to the cell's
  declared allowlist. A space that selects it may not also keep
  `code_execution: native` (that combination is rejected at render).
  Register the warm-cell definition once per space through the standard
  `prepare_source` flow with a `cli` spec
  (`command: bash`, `args: ["-c"]`, `lifecycle: binding`,
  `workspace_scope: principal`, `workspace_access: rw`); `bash` must sit
  in the operator allowlist (`HUB_CLI_ALLOWLIST` / controller
  `user_commands`). The cell keeps a private `/cellhome` tmpfs across
  calls while it is warm (age/reuse limits apply), so `install_packages`
  tools keep what they install; the workspace under `/work` is the
  principal's real workspace. Because the definition is an owner
  publication, its projection needs a user-scope governance allow rule
  (`hubctl governance --kind rule --scope user --user <p> --match
  definition_id=<def> --effect allow`).
* `settings` exposes the reviewed self-settings surface. Values live in a
  JSON overlay (`HUB_SELF_SETTINGS_PATH`, rendered at `self-settings.json`
  under the managed runtime state dir) that both the supervisor
  materialization and the runtime-side attestation load, so the effective
  config stays identical on both sides of the boundary. `settings_get`
  returns only reviewed keys; `settings_set` validates types, applies the
  overlay atomically and returns `restart_required` keys — the runtime
  restarts itself rather than mutating live state. Arbitrary config keys
  are never accepted; the allowlist lives in `internal/selfsettings`.

Managed effective config enables Hermes compression
(`compression.enabled`, `in_place`, `micro_compact`) with a fixed
`threshold_tokens` budget so sessions compact predictably regardless of
model window size. Compression is safe there because managed runtimes run
the api-server path — the detached hygiene/curator sub-agents that bypass
`disabled_toolsets` never spawn, and `ContextCompressor` is an auxiliary
LLM call with no tool surface.

`access: ro` makes an entry read-only where the boundary can prove it:
ToolHub entries deny every write-effect tool inside the executor
(`HUB_TOOLS_RO`), connector write actions drop out of `HUB_ORG_ACTIONS`,
browser loses its mutation tools, and an `mcp-raw` server gains a
`tools.exclude` filter (reviewed mutation table, or a mandatory explicit
`except:` for servers the hub has not reviewed). `access: ro` on a native
toolset is a validation error — upstream grants toolsets whole, so the
hub would be promising a filter it cannot enforce.

```text
hubctl capability --kind tools                                   # backend vocabulary + reviewed native set
hubctl capability --kind tools --settings <space>                # compiled plan
hubctl capability --kind tools --settings <space>/workspace.yaml \
  --set terminal=native,browser=mcp:playwright+ro --confirm      # surgical write
```

`--set name=backend` upserts entries; `--set name=` removes them; `+ro`
sets `access: ro`. The write edits only the `tools:` mapping, re-validates
the whole file pair through the spawn parser and swaps atomically. Native
changes take effect on restart or re-spawn.

The gateway records authorized private Telegram input and delivered replies,
including job IDs and user IDs where available. HTTP operations in Hermes
runtime, ToolHub, supervisor, communication hub and Credential Broker record
method, bounded route and duration; request bodies and query strings are not
logged. Credential input (`KEY=value`, `/credentials`, `/broker-approve`) and
one-time credential replies are redacted. Treat the combined file as private:
it contains personal conversations and service output. `.local/` and
`spaces/` are ignored by Git; review and redact before sharing them.

When Telegram is enabled, gateway mode supervises `hub-communication`. It maps numeric
sender IDs from the configured allowlist to user scopes, writes durable jobs and replies
under `/state/gateway`, and runs one fresh bounded Hermes process per job. The default
single-space deployment maps one user; a host-owned multiuser configuration maps each
verified numeric ID to a separate `spaces/<user>` home and runtime.
The runtime includes the deployed `SOUL.md` digest in its deterministic conversation
session ID, so changed system instructions start a fresh chat session on the next
message while durable owner memory remains intact.

Long Telegram turns (for example, ToolHub source review/build) use Hermes' FIFO
`display.busy_input_mode: queue`; a new ordinary message is kept for the next turn
instead of cancelling the active MCP call. `HERMES_AGENT_NOTIFY_INTERVAL` is rendered
as 60 seconds, so the native Hermes gateway sends a periodic working heartbeat while
the call is running. ToolHub's generated MCP timeout is 1800 seconds, matching the
isolated builder's 25-minute safety ceiling plus startup margin. `/stop` and `/cancel`
remain explicit cancellation controls.

Treat `scope.yaml` IDs as immutable. Moving or renaming a home does not change its
principal or context identity. Today Telegram links are provisioned only in trusted
host configuration after the operator verifies the numeric account ID; there is no
self-linking endpoint. A future Slack/OIDC flow must authenticate the existing
principal and require fresh issuer proof. Never link from a username, email string or
message request. Unlinking stops routing but does not transfer or delete the principal.

Change settings, then run up. The runtime copies the generated Hermes config at startup;
existing memory and installed skills/hooks persist. SOUL.md initializes a new runtime's
instructions; edits made inside an established Hermes home remain there. To replace it,
explicitly copy the owner's new SOUL into that selected container and restart.
Managed connector secrets are stored as ciphertext under
`spaces/<user>/runtime/credentials/store.enc`. The encryption key lives in
`HUB_CREDENTIAL_KEY` or `HUB_CREDENTIAL_KEY_FILE` (default
`%APPDATA%/hermes-hub/credential.key` on Windows, `~/.config/hermes-hub/credential.key`
elsewhere) and must stay outside the store directory and ciphertext backups.

```text
hubctl secret set --user <id> --name GOOGLE_TOKEN          # value on stdin or --from-file
hubctl secret list --user <id>
hubctl secret delete --user <id> --name GOOGLE_TOKEN
hubctl secret expose --user <id> --name GOOGLE_TOKEN --terminal
hubctl secret backup --user <id> --out credentials.backup
hubctl secret restore --user <id> --in credentials.backup
hubctl secret rotate-key --user <id> --from-file new.key
hubctl secret activity --user <id>
```

An explicit owner `KEY=value` chat message is intercepted before Hermes, rejected and
best-effort deleted; it is never persisted. The gateway returns a one-time protected
loopback form generated from the selected connector or MCP Connection Recipe; its fields,
types, order, delivery metadata and OAuth alternatives are derived at request time.
Alternative groups stay separate submits of the field names the recipe or the server named. The shortest set is open and the others are collapsed. A tools/list probe speaks stdio when the image command selects sse or http, and a fresh form link is delivered even if an older notice for the same onboarding already exists. The prepare notice includes that loopback form URL. A credential-gate submit with no reviewed broker contract stores the secret in the local encrypted store and finishes confirm and enable from that store; a reviewed contract still requires a broker credential. A failure after that secret is stored says the data was saved and sends the user back to chat confirmation. When that definition version is already a user publication of another principal, the submit keeps the next free patch version and leaves the existing publication in place.
There is no static credential form. Groups reject credential entry. Set and delete also load the ToolHub registry
(`HUB_TOOLHUB_STORE` or `spaces/<user>/runtime/toolhub/store.json`) so rotate and
revoke cut an already-open MCP session and stop affected workloads. Personal-terminal
exposure copies selected names into that owner's `self-env.json` overlay and is
reversible. The legacy `env_update` path is not used for managed secrets. Failed
rotates leave the connection `degraded` until a successful rotate or an explicit
revoke. ToolHub records `X-Hub-Job-ID` and `X-Hub-Run-ID` on MCP requests into the
audit ledger together with a generated tool-call id. The communication worker
appends matching job events when `HUB_AUDIT_LEDGER` or a credential store is
configured. `HUB_CREDENTIAL_STORE` must be set on `hub-toolhub` for decrypt-and-inject.
Never run hermes update in prod: update reviewed source pins and rebuild instead.

To enable the Credential Broker path, configure the control, approval and
runtime URL/key/ID/issuer variables documented in
[integrations](integrations.md). All three audiences are required. Start the
copied Broker with its own config and provider/contract directories; the Hub
only receives the Broker origin and role-specific private signer files. For a
pending onboarding, send `/credentials <request-id> <code>` through the
authenticated Communication Hub chat. Existing local references are not
auto-migrated when Broker mode is enabled.

For a contract that delivers a file (for example a Google service-account JSON),
the Broker materialization directory must be a dedicated shared tmpfs visible
at the same absolute path to the Broker, ToolHub/controller and Docker daemon.
Set that path as `credential_mount_root` in the generic controller config. The
controller passes it as a read-only `--volume` and tears down the workload after
the call; without this shared path the request is rejected.

The Broker's own state lives in one external volume per environment,
`hermes-credential-broker-<env>` — a dev stack renders `-dev` and can never
attach prod credential material. The volume is `external`, so create it once
per environment (`docker volume create hermes-credential-broker-prod`).
Deployments that still rely on the pre-split singleton pin it explicitly with
`HUB_BROKER_STATE_VOLUME=hermes-credential-broker-real-prod-20260918` in the
render environment.

The broker does not bootstrap an empty volume — it exits with
`cannot read configuration` until `config.json` and its key/ledger material
exist. Migrating a stack off the pre-split singleton therefore means
copying the volume content, not just creating the volume:

```sh
docker run --rm --user root \
  -v hermes-credential-broker-real-prod-20260918:/from \
  -v hermes-credential-broker-dev:/to \
  --entrypoint sh hermes-hub:0.3.0-dev -c 'cp -a /from/. /to/'
```

Run it while the broker is stopped, then `docker compose up -d`. To stay on
the singleton instead, pin `HUB_BROKER_STATE_VOLUME` and re-render — no copy
needed. Do not point dev and prod renders at the same volume again.

A related upgrade seam: current ToolHub validates `proxy_environment` names —
every entry must end in `_PROXY` (split `*_PROXY_HOST`/`*_PROXY_PORT` pairs are
rejected because the controller fills proxy params with the egress URL, and a
HOST field receiving a URL is meaningless). A store written before that rule
(`runtime/toolhub/store.json`) can hold now-invalid names and the service will
refuse to start with `invalid or duplicate proxy environment parameter`.
Move the offending names from `proxy_environment` to `environment` and restart
toolhub — the vars then carry user-supplied values instead of the egress URL,
which is what split host/port connectors expect.

The selected user home contains persistent Hermes, connection, workspace and archive
data. An organization home is mounted read-only for members. Dev/prod selects separate
generated env files and Docker targets but does not create another user namespace. Never
copy production secrets or memories into dev.

Existing installations are migrated only explicitly:

```text
hubctl migrate-spaces --user artem --org acme       # dry-run
hubctl migrate-spaces --user artem --org acme --apply
```

The command rejects symlinks, ID/kind collisions, unrelated destination data and an
active runtime. It stages and verifies files, leaves source directories and volumes in
place, and prints a JSON report with paths, counts and checksums but no secret values,
message text, cookies or session contents. Rollback is restoring the untouched source
into a new scope home; do not delete it until the new runtime is verified.

Context data uses `hubctl context backup|restore|export|purge`. A verified snapshot
covers Hermes home, workspace, connection metadata, ToolHub bindings, schedules and
the communication delivery ledger. Plaintext secrets stay on `hubctl secret backup`
and are never placed in the general archive. Restore refuses another user and does
not reactivate revoked credentials. Pass `--spool` so schedules and mappings return
to the gateway spool; occurrences and outbox are skipped so cron deliveries are not
duplicated. `hubctl down` stops compute without deleting
homes; purge requires `--confirm` matching the user and reports removed paths
without secret values. Sensitive native memory lives in `spaces/<id>/hermes/memories`
(`MEMORY.md`, `USER.md`); inspect with `hubctl memory list` and delete with
`hubctl memory delete --name`. Honcho is optional: set `honcho` and `honcho_url` to
the official local or hosted base URL so the hub writes `$HERMES_HOME/honcho.json`.
Honcho failure must not be required for ordinary Hermes memory.

User skills install into `spaces/<id>/hermes/skills` without rebuilding the image.
Organization and global skills are read-only `skills.external_dirs` mounts.
Every rendered runtime mounts the repository `config/skills` directory as the default
global skill source; it includes the ToolHub-first MCP/connector onboarding skill.
`hubctl skill install --consent` scans SKILL.md and refuses provider-mutation text.

Back up all `spaces/<id>` homes and the `communication-hub-data` volume to encrypted
storage while services are stopped. Ciphertext backups use `hubctl secret backup` and
must not include the encryption key. Do not use `docker compose down --volumes` unless
deleting that user's data intentionally. Restore organization and user homes separately;
never merge memories, Telegram sessions or provider credentials.

The hub has a shared core runtime (apt Chromium, Hermes with the `mcp` extra)
and a control target used only by ToolHub and the workload controller. The
control target adds Docker CLI and ToolHive. Slack MCP is an opt-in ToolHub
connector and is never fetched by the hub Dockerfile. Playwright browsers, Hermes
`messaging`/`google`/`voice` extras, the Go toolchain, and the Google /
Atlassian / Telegram-account Python trees stay out of the default image.
Those MCP trees are `docker/telegram-account.Dockerfile`,
`docker/mcp-atlassian.Dockerfile`, and the official Google Workspace remote
MCP (ADR-0019). Repeating `build:` on each service made `docker compose build` run that
Dockerfile once per service. Combined with the classic builder
(`DOCKER_BUILDKIT=0`) this materialized every stage as a separate image and
added roughly one full copy of the image per command.

Generated compose files carry one core `build:` section (`cliproxy`) and one
control `build:` section (`toolhub`); other services reference those image
tags. BuildKit shares their common layers and builds CLIProxy independently
from the hub's Go services. Go module and compiler cache mounts survive source
edits and are bounded by the same builder cache cap. `just` recipes and
`hubctl build`/`up` export `DOCKER_BUILDKIT=1`, `COMPOSE_DOCKER_CLI_BUILD=1`
and `COMPOSE_BAKE=true`, so a stale `DOCKER_BUILDKIT=0` cannot silently
downgrade those entry points. Before and after each compose build attempt,
`hubctl` prunes dangling images left by retagging; the next invocation also
recovers after a killed process. Intermediate BuildKit stages never
materialize as extra images. Setting `DOCKER_BUILDKIT=0` globally is still
discouraged because raw `docker compose` outside hubctl/just would use the
classic builder.

Pass service names after the flags to build or restart only those services,
for example `hubctl up --dir spaces/alice --env dev communication-hub toolhub`.
Scoped `up` skips dependencies; select them explicitly if they also need a
restart. This keeps unrelated runtime, speech and media images out of a
gateway-only rebuild. After a successful `hubctl build` of the selected
services, `hubctl up --no-build` starts those existing images without another
build attempt.

Rebuilds of the tagged hub image used to leave the superseded generation
dangling at full unique-layer size. `hubctl build` and `hubctl up` now remove
stopped `hermes-hub-*` containers that pin dangling generations, prune the
released images and cap the local BuildKit cache at 8 GB. They leave running
containers, named volumes and other projects' containers intact; the cache
cap applies to the selected shared Docker builder. `just docker-clean` (also the last step of
`just docker-check`) removes stale `hermes-hub:*` tags not referenced by any
`spaces/*/compose*.yaml` or `spaces/*/generated/compose*.yaml`, stopped `hermes-*` containers that pin dangling images,
dangling image layers and orphan `hermes-build-*` containers/networks/volumes.
Ordinary cleanup caps the local BuildKit cache at 8 GB; `just docker-clean --deep`
fully prunes it and additionally drops
`hermes-build-state-shared`, the opt-in persistent builder cache that
`HUB_BUILD_CACHE=1` recreates on the next artifact build. Run it only while no
artifact build is in flight.

`devcheck docker-build` accepts optional `HUB_DOCKER_CACHE_FROM` and
`HUB_DOCKER_CACHE_TO` environment variables; each non-empty value is forwarded
to `docker build` as `--cache-from`/`--cache-to`. The build always passes
`--load` so the tagged image lands in the local daemon even when the selected
builder uses the `docker-container` driver. CI sets these to
`type=gha,scope=hermes-hub-<target>` so GitHub-hosted runs reuse remote
BuildKit cache instead of rebuilding the base layers; local runs without the
variables behave exactly as before.

CI publishes hub images to GHCR so consumers pull instead of building. The
`image` workflow job runs `devcheck docker-publish <target>` per matrix leg
(both `<target>` and `<target>-control`), pushing the immutable
`ghcr.io/<owner>/<repo>/hermes-hub:<sha>-<target>` tag and moving
`edge-<target>`. The `containers` job then runs `just docker-check-prebuilt`,
which resolves the image for `HEAD` via `devcheck docker-pull` — the sha tag
first, `edge-<target>` as fallback — retags it as `hermes-hub:test*` and runs
the same smoke/contract/canary chain without a build stage. Every published
image carries the `org.opencontainers.image.revision` label (from
`ARG GIT_SHA`); `docker-pull` warns when the pulled revision differs from
local `HEAD`, which is the signal to either push or build locally.

Local use needs a one-time `docker login ghcr.io` (a PAT or
`gh auth token` with `read:packages`). `just docker-pull` alone only stages
the image; `just docker-check-prebuilt` stages and runs the full gate.
`HUB_REGISTRY_REPO` overrides the owner/repo resolution for mirrors. A weekly
`prune-images` workflow deletes GHCR versions beyond the newest eight while
keeping `edge-*` tags, so published image storage stays bounded.

To reclaim physical Windows disk space after Docker cleanup, double-click
`scripts/reclaim-docker-disk.cmd` and accept the administrator prompt. It
stops Docker Desktop and WSL, removes three previously identified temporary
swap VHDX files by exact path, then runs `Optimize-VHD` on Docker's data VHDX.
The Hyper-V PowerShell module must be available. The script shows free C:
space before and after; Docker remains stopped until you start it again.

Health checks prove process liveness and, where enabled, Chromium CDP readiness. They do
not prove OAuth, model, Telegram or provider access. Live acceptance requires the
operator's separate login and provider instructions.

Self-service `env_update` and `service_enable` persist only under the selected user
connection state and request a supervisor restart. When `HUB_TOOLHUB_STORE` is
explicitly set, `service_catalog`, `service_enable` and `service_disable` operate on
manifest-backed exact-owner bindings and persist those bindings to the store;
`hub-toolhub` reloads the snapshot on list/call. Organization scope may disable a
binding and cannot enable one. Otherwise the legacy self-service path remains
active. They cannot edit host settings or runtime-control variables. Provider content
cannot authorize any mutation. The ToolHub endpoint reloads projection changes
and notifies the owner's open MCP session without restarting Hermes.
`HUB_TOOLHUB_AUTOSTART=true` starts the shipped
`hub-toolhub` binary.

## Scale-to-zero operation (initial implementation)

ADR-0014 and SPEC-0012 define the runtime mode. The trusted host-side `hubctl supervisor`
now starts the selected context runtime for an accepted job, reuses it for a five-minute
default warm window and stops only its compute after all leases end. The supervisor
proxies the authenticated private execution endpoint, applies resource limits and
mounts only the selected context. The communication and Hermes containers do not receive
the Docker socket. Enable it explicitly with `HUB_RUNTIME_SUPERVISOR_URL` while keeping
native static per-user Compose as an explicit alternative. The published v0.2.1
artifact provides the retired one-shot rollback after drain/stop/audit.

In supervisor mode, keep the selected owner's sidecar services running. The runtime
joins `hermes-hub-<user>-<env>_default` and mounts that project's
`broker-secrets-runtime` volume read-only; the default shared runtime network is not
used for owner jobs.

### Capacity measurement (dev host, 2026-10-07)

Best-effort M6 capacity data for #46, measured on the author's Windows dev
host — **not** the Linux VPS target. Treat absolute numbers as dev-host
samples; rerun the protocol on the target VPS before fixing SLOs.

Host: Windows 11 Pro 22631, 16 cores / 32 GiB; Docker Desktop on WSL2
(kernel 6.6.87.2), engine 29.4.3 linux/amd64, VM budget 15.47 GiB.
Runtime image `hermes-hub:0.3.0-dev`, id `sha256:dc8df91990b8…`, 1.19 GiB.
Commit `baeafd4`. Commands: `POST /v1/leases` / `DELETE /v1/leases/<id>`
against `hubctl supervisor --spaces spaces --env dev --runtime-image
hermes-hub:0.3.0-dev --supervisor-listen 0.0.0.0:8876`, plus
`docker stats --no-stream` / `docker inspect` point samples.

Measured (single samples, one dev space):

| phase | result |
|---|---|
| cold lease → runtime Ready | 59.4 s |
| restore after scale-to-zero → Ready | 78.0 s |
| warm lease on running runtime | 0.58 s |
| idle deadline → container removed | <45 s (reaper ≤5 s + stop grace ~40 s) |
| warm TTL | 5 min (default `--warm-ttl`) |
| managed runtime RSS (warm) | ~395–403 MiB (cap 1 GiB, 2 CPU, 256 pids) |
| runtime ctl sidecar RSS | ~8 MiB |
| runtime CPU during cold start | ~100 % of 2-core cap for ~20–40 s |

Always-on footprint for one dev stack (`hermes-hub-<user>-dev`),
point-in-time RSS: communication-hub 13.7 MiB, credential-broker 10.3 MiB,
toolhub 73–80 MiB, workload-controller 8.8 MiB, cliproxy 15.1 MiB,
hub-media 7.3 MiB, hub-stt 2.7 MiB, hub-tts 2.8 MiB, three relays ~6.3 MiB
each, supervisor host process ~25.6 MiB → **≈160 MiB per user stack**, plus
`hermes-context-*` per active/warm context (~410 MiB each, bounded by
`--max-runtimes 8`).

Registered-user model (20–200 users, this host class): always-on memory
scales linearly with registered stacks — 20 users ≈ 3.2 GiB, 100 ≈ 16 GiB,
200 ≈ 32 GiB — so a ~16 GiB VM fits roughly 90 stacks before headroom for
warm runtimes (8 slots × ~410 MiB ≈ 3.3 GiB) and burst CPU disappears.
The concurrency bound is what makes scale-to-zero viable: 200 registered
users never mean 200 resident Hermes processes — `--max-runtimes` (8) caps
live contexts and the lease semaphore queues the rest; crash-loop denial
stops retry storms after 3 crashes inside the backoff window
(`next_retry_at`, covered by `reconcile_test.go`). Cold-start latency
(59–78 s here, dominated by image-side init and connector startup inside
the 2-core cap) is the user-visible cost; deriving production SLOs requires
repeating this protocol on the actual VPS and adding the document/image and
connector-reconnect runs this session could not cover.

Rollback: the v0.2.1 one-shot runtime artifact remains the drain/stop/audit
rollback; degraded mode is native static per-user Compose (no supervisor).

### Docker socket boundary

Only ToolHub and the workload controller ever mount `/var/run/docker.sock`,
and only when the rendered surface can use it: a space with no
`via: toolhub`/`via: mcp` capability, no managed mode and `diagnostics: false`
gets no socket mount at all — diagnostics itself is a Docker consumer (the
collector reads `docker ps`/`logs`), so opting out also disables collection.
Inside the socket-bearing services every `inspect`, `exec`, `logs`, `rm`,
`cp`, `start`/`stop` and `network`/`volume` operation is scope-checked
(`HUB_DOCKER_SCOPE` = `hermes-hub-<user>-<env>`): an object must carry the
compose project label, the `hermes-hub.scope` label the hub stamps on
spawned networks, volumes and containers, or sit on that space's managed
agent network (`HUB_DOCKER_AGENT_NET`). ToolHive cannot stamp labels, so the
controller registers names it just spawned; anything else — including a
caller-shaped workload id that collides with another project's container or a
shared network like `hermes-hub-control` — is denied before the socket sees
it and logged as `docker-scope: denied`.

Example from the repository root:

```text
HUB_SUPERVISOR_AUTH=<host-control-token> hubctl supervisor --spaces spaces
HUB_SUPERVISOR_AUTH=<host-control-token> HUB_RUNTIME_SUPERVISOR_URL=http://host.docker.internal:8876 hubctl render --dir spaces/alice
```

Two watchdog knobs bound long-running jobs. `HUB_SUPERVISOR_JOB_TIMEOUT`
(default `130s`) is the supervisor's per-request HTTP timeout toward the
runtime: when it elapses the job stream ends and the durable job is marked
`uncertain`, while the runtime keeps executing and reconciliation observes it
to a terminal state. Raise it for routinely long jobs. `HUB_RUN_STALL_TIMEOUT`
(default `15m`) lives inside the runtime: if the Hermes run `updated_at`
timestamp and the event stream both stay silent for that interval, the runtime
issues a confirmed stop and fails the job as stalled instead of polling until
the hard run timeout. Both accept Go durations (`130s`, `10m`) or bare
seconds; the supervisor passes `HUB_RUN_STALL_TIMEOUT` into spawned runtime
containers.

The runtime uses pinned Hermes `/api/sessions` and `/v1/runs` for each accepted job.
Persistent execution supports normalized event streaming, durable admission metadata
and a spool event journal. Back up the spool's `events/` directory together with
jobs, mappings and outbox; receipts repair incomplete result handoff on restart.
Do not delete uncertain mappings or their leases to force a retry: the original run
may already have performed an external action. Recovery observes that run through
the private supervisor API and can report `interrupted` after a Hermes restart.
Cancellation/approval control, complete reconciliation and routine migration remain
tracked in CHG-0017 and issues #17-#19/#33. Full migration requires their real Docker
and pinned-Hermes acceptance evidence.

Routine schedules will be stored by the hub and create ordinary idempotent jobs that
wake sleeping contexts. A context with active unmigrated native Hermes cron must remain
explicitly pinned on; operators must never enable both schedule owners for one routine.

Supervisor health observations distinguish container state from authenticated Hermes readiness. Unknown container state and nonterminal durable jobs block idle reaping. Failed runtime starts retain a durable crash counter: at most three failures per ten-minute window, with two-, four- and eight-second retry pauses. Restarting the supervisor preserves this budget. Automatic replacement and operator pins use the same durable crash budget; these observations do not prove external provider integration.

New supervisor runtime containers carry hermes-hub.owner, hermes-hub.context and hermes-hub.generation labels. The owner is derived from the supervisor state path. Periodic inventory persists stale or orphaned generations and excludes containers with another owner. A failed scan preserves the previous inventory. Only verified old generations with terminal job ledgers may be removed; unknown and uncertain generations remain for inspection.

Confirmed disappearance of a context runtime with current durable work consumes the same crash budget as a failed start. Repeated observations of that disappearance do not consume additional attempts. Recovery waits for the persisted backoff and preserves the original admitted run; it does not repeat the input. An idle registered context is not recreated. Owned exited containers follow the same recovery path, and terminal results use durable delivery handoff.

Runtime reconciliation now also replaces verified owned exited/dead containers with current durable work after the persisted backoff. It removes the old container by verified immutable ID before launching a replacement generation. Foreign containers and idle registered contexts do not trigger replacement. A failure is counted once per generation, even if later observations change between exited and missing. Original run observation and delivery recovery remain separate from infrastructure replacement.

The private supervisor `POST /v1/pins` accepts the verified task identity fields
and an explicit boolean `enabled`. Pins persist independently of container
generations and keep the selected context available for an operator requirement
or an unmigrated native Hermes cron. Disabling a pin does not cancel current
jobs or remove user files; normal idle retention resumes. Pins require the
private supervisor token and the same saved owner envelope for removal.

A verified private owner may enqueue one-shot and recurring tasks through the
schedules HTTP API (`/v1/routines`). The communication spool saves each
occurrence before acknowledging it. Due occurrences become ordinary jobs
with `trigger=cron`; the normal supervisor execution path wakes the context.
Duplicate ticks and restart reuse the same occurrence key. Inputs containing
credential assignments are rejected. Catch-up is limited to one hour and each
tick settles at most sixteen occurrences. Recurrence CRUD/DST and native cron
migration remain the separate schedule-owner contract; native cron requires an
explicit supervisor pin until migrated.

Final replies longer than Telegram's 4096-character message limit arrive as one
`response.txt` document, with the complete UTF-8 result and a 2 MiB ceiling.
Cached supervisor results use the same durable delivery receipt as streamed
results. A send whose acknowledgement was lost remains uncertain: inspect the
outbox rather than blindly retrying and risking a duplicate external message.

Runtime startup prepares the configured `HOME` and `HERMES_HOME` directories and
copies the mounted Hermes configuration into `HERMES_HOME/config.yaml`. These
directories may be separate from `HUB_STATE`, as they are in supervised contexts.

The supervised private gateway enables native `HERMES_EXEC_ASK` so flagged
commands can wait for `/approve <job_id> <request_id> <choice>` in the initiating
private conversation. The saved request expires after two minutes; the supervisor
confirms native cancellation before releasing its approval hold. A lost decision
acknowledgement is observed without repeating approval. Native deny/hardline rules
and current organization/tool authorization still apply.

A verified owner keeps several independent task sessions inside one private DM.
`/new [name]` creates a task and switches to it; a nameless task carries a
placeholder name flagged `name_auto` and adopts the upstream session title
after its first real exchange. `/use <id|name|default>` switches the chat's
current task, `/delete [id|name]` removes the task resolved for the message's
audience (the local registry record and routing only; the durable Hermes
session stays upstream for audit), and `/sessions` lists live tasks with their
creation dates. `/session` reports the current task's name, creation date,
bound topic and durable session id. A message posted in a Telegram
direct-messages topic resolves to its bound task; a first post in an unbound
topic adopts one, and root-DM messages follow the current pointer. Forum-group
topics are supported the same way: the bot accepts group messages only from
verified users, topic messages carry `message_thread_id`, and replies return
to the originating topic — group jobs deliver to the group chat rather than
the sender's DM. Scheduled occurrences remain private-DM only. Unknown slash
commands are answered with the supported-command list and never reach the
model, and the gateway registers that same list as the bot's command menu on
every startup across all Telegram scopes, so a foreign adapter's stale menu
cannot shadow it. The implicit `default` task keeps the legacy
`telegram-<chat>` conversation, so pre-task history and mappings remain valid.
Each task owns its own durable Hermes session because the session id derives
from the task conversation id; jobs, replies, streams, artifacts, voice and
continuation notices carry the originating task and topic id end to end.
Task records may carry per-task presentation guidance (`style`, up to 1024
characters, no control characters) that rides each admitted run as Hermes
`instructions`; the admitted run's snapshot is pinned on its durable mapping,
so a later style change affects subsequent runs only and never touches
authorization or tool policy. `/usage` reports the task session's measured
model, title and
start time, cumulative tokens, calls, cost and session-rotation count read
back from the pinned Hermes session API; fields the upstream does not
authoritatively expose — current-prompt context and the context window —
render `unknown` instead of an estimate. When the operator configures
`HUB_CLIPROXY_MGMT_URL`, `HUB_CLIPROXY_MGMT_KEY` and optionally
`HUB_CLIPROXY_AUTH_INDEX` in the channel env file, `/usage` additionally
queries the pinned CLIProxyAPI management surface (`auth-files`, then an
authenticated `api-call` to the provider quota endpoint) and prints measured
subscription limits with reset times; OAuth tokens stay inside the proxy. The
result is cached for five minutes and failures render `недоступно` rather than
a guess.

Reconciliation reserves capacity for every restored non-stopped runtime. A
missing idle container releases that capacity only after TTL and a durable
stopped-state write. A lost Docker launch acknowledgement keeps capacity until
verified cleanup or authoritative absence. A saved lifecycle hold can recover
compute and retains its original opaque release ID; its expiry timestamp is not
permission to discard an uncertain operation. Legacy holds without a saved
binding remain available for inspection instead of guessing a new authority.

The supervisor inventories at most 256 owner-labelled containers and compares
container name, context and generation. An old generation can be removed only
when its job ledger is known terminal and immutable ownership is verified.
Ownership also requires the actual `/scope` bind source to match the selected
context directory; copied labels cannot authorize another context mount.
Unknown or uncertain generations remain visible in the authenticated runtime
inventory and block additional cold starts. Inspect and resolve them explicitly;
automatic cleanup never removes volumes or context data. Failure counts and
2/4/8-second retry deadlines persist, with at most three failed generations in
a ten-minute window. Process state, authenticated Hermes readiness and native
platform health are reported separately; external lazy connectors remain
`not_probed` until their own authorized health contract is exercised.
Docker commands have a thirty-second ceiling. A background sweep has a
150-second ceiling and stops admitting new lifecycle mutations after its deadline.

Pinned Hermes keeps a durable session-turn lease for up to 300 seconds after an
unclean exit when Docker reuses the former gateway PID. A new task in that same
conversation can therefore remain admitted while waiting for the native lease.
Request timeout keeps the task uncertain and held; recovery observes the same
run until it settles. The hub never deletes native locks, sessions or history to
shorten that wait, and never submits a second copy of the input.


## Per-context communication rollout

`hubctl select-execution` persists a host-owned execution selection and renders
only that context's Compose configuration. The selected user and environment must
match its settings. A saved static selection overrides a global supervisor env.
The scope root is read-only inside supervised Hermes; writable mounts preserve the
same `/state`, Hermes, connection, home, workspace and archive layout as static Compose.
The provider env is injected only into its owning runtime, never the communication hub.

Drain work first, then stop the selected communication/static services. Inspect the
actual mounted gateway spool, including mappings, events and uncertain deliveries.
The CLI refuses claimed/admitted jobs and refuses apply with uncertain outcomes.
Supply the actual mounted spool path; arbitrary empty-directory audits are not a
substitute for the selected communication volume. On Windows with Linux Docker
volumes, run the read-only audit in a container mounting that volume or use the Linux
deployment host for the apply workflow. Apply verifies that this path matches the
stopped selected gateway's actual `/data` mount. It also invokes the pinned upstream
cron SDK against a disposable read-only home snapshot and requires zero enabled native
jobs. The selection does not disable schedules itself.

Example with a spool mounted on the Linux deployment host:

```text
hubctl execution-audit --spool /mnt/alice-gateway --user alice
hubctl select-execution --dir spaces/alice --user alice --env prod --spool /mnt/alice-gateway --execution-mode supervisor --supervisor-url http://<private-host-address>:8876 --native-cron disabled --compatibility-release 0.2.1
hubctl select-execution --dir spaces/alice --user alice --env prod --spool /mnt/alice-gateway --execution-mode supervisor --supervisor-url http://<private-host-address>:8876 --native-cron disabled --compatibility-release 0.2.1 --apply
hubctl up --dir spaces/alice --user alice --env prod
```

The supervisor URL must be reachable from both the host CLI and the gateway container;
configure its listener on the intended private interface and provide
`HUB_SUPERVISOR_AUTH` to the CLI and Compose invocation. The default loopback listener
alone is not reachable from a Docker container through `host.docker.internal`.
Give each local Hub installation its own supervisor port. A port occupied by another
installation may answer health checks but reject this gateway's bearer with HTTP 401;
the containers can still appear healthy while every bot job fails before Hermes starts.
When changing ports, update the selected `supervisor_url`, render the context again,
and recreate Communication Hub and ToolHub so both use the new address.
The supervisor creates its runtime network on demand; runtime ports stay loopback-only.

For rollback, drain and stop the selected context again and select
`--execution-mode static` without `--supervisor-url`, retaining the same spool/home.
An unmigrated native scheduler additionally requires `--native-cron unmigrated`; this
selects a static native Gateway and forbids scale-to-zero, preserving its clock.
A stopped supervisor runtime for the rollback context cannot be awakened by the
supervisor while the host selection remains static. Another user's routing is unchanged.
Do not reactivate native definitions already migrated to hub occurrences.

The automatic supervisor sweep checks idle deadlines at most five seconds apart.
The isolated full gateway proof can be repeated with
`go run -tags integration ./cmd/devcheck gateway-lifecycle hermes-hub:test`; it waits
five real minutes and uses local Telegram/model fixtures with real pinned Hermes.
Five minutes after the last lease settlement, an idle runtime is gracefully stopped
and its container removed. Unknown operations, active approvals and pins keep it alive.
A new job racing shutdown either retains that generation or starts one replacement.
Supervised self-environment changes reload on the next cold start, rather than sending
an unscoped restart after a final reply. Private `GET /v1/jobs/<id>` exposes durable
status and saved audience to the authenticated host operator/gateway.

The published v0.2.1 compatibility release remains the artifact rollback boundary.
v0.3.0 removes the one-shot executor: static selection also uses native Hermes APIs.
Existing `legacy` selections are read as `static`; CLI-only containers idle without
a resident Gateway. To restore the old executor, deploy the accepted v0.2.1 artifact
after the same drain/stop/audit sequence. Its release binaries match the real Docker
acceptance image; a selection's release name alone is not publication evidence.
See SPEC-0016 and CHG-0018 for acceptance and current evidence.

## SSH capability

`ssh` in `workspace.yaml` `tools:` exposes bounded SSH to owner-configured
hosts. `ssh_write`, `ssh_shell` and `ssh_tunnel` stack on it; each requires
`ssh`. Render fails when `connections/ssh/config.yaml` is missing or invalid;
`doctor` reports the same gap. The directory is mounted read-only at
`/state/ssh`, so rotate hosts/keys by editing it and running `hubctl up`.

```yaml
schema: 1
defaults: {max_sessions: 4, max_shells: 2, max_tunnels: 4}
hosts:
  prod-web:
    host: 203.0.113.10
    port: 22
    user: deploy
    host_keys: ["ssh-ed25519 AAAAC3NzaC..."]   # or "sha256:<fingerprint>"
    key_ref: "file:keys/prod-web"              # or "broker:<grant-id>"
    certificate_ref: "file:keys/prod-web-cert.pub"  # optional
    commands: ["systemctl status *", "docker ps", "tail -n * /var/log/*"]
    write_commands: ["systemctl restart app"]
    paths: ["/etc/app/*", "/var/log/app/*.log"]
    write_paths: ["/srv/app/config/*"]
    sudo: never                # or passwordless -> effective command via sudo -n
    timeout_seconds: 30
    max_output_bytes: 65536
    tunnels:
      db: {remote_host: 127.0.0.1, remote_port: 5432}
```

Host keys are mandatory and pinned per alias; collect them with
`ssh-keyscan -t ed25519 <host>` on the host itself, not through the runtime.
Prefer full public-key pins: they restrict the negotiated host-key algorithm
to the pinned type, while a bare `sha256:` fingerprint matches whatever type
the server offers — pin one fingerprint per offered key type (ed25519, rsa,
ecdsa) or the dial fails closed with "host key is not pinned".
`file:` key refs stay inside `connections/ssh/` (regular files, <=64 KiB,
unencrypted OpenSSH/PEM keys; use `broker:` refs for anything sensitive).
`broker:` refs name a grant ID on a credential created through the `ssh-key`
contract (`services/credential-broker/examples/contracts/ssh-key.json`);
the runtime adapter acquires, materializes and releases a short-lived lease
per dial under the `ssh-<alias>` binding, so the provisioned runtime key needs
both `broker:control` and `broker:runtime` audiences for broker refs.

Allowlists are anchored patterns where `*` is the only wildcard. Commands and
sudo are evaluated per host: with `sudo: passwordless` a `sudo <cmd>` request
runs `sudo -n <cmd>` only when `<cmd>` matches an allowlisted pattern. File
reads return bounded UTF-8 text; writes are atomic temp+rename.

`ssh_shell` shells and `ssh_tunnel` forwards live inside the runtime process:
a restart or scale-to-zero stop closes them, and the reaper expires them by
idle/lifetime bounds. Tunnel listeners bind to loopback inside the runtime and
are reachable only by workloads sharing that runtime — they are never
published to the host or the network. Revoking a Broker grant or deleting a
host entry takes effect at the next dial; nothing caches keys or sessions.

Every operation appends a bounded `ssh` event to the owner audit ledger under
`spaces/<user>/runtime/audit/ssh.jsonl`; receipts carry exit codes, byte
counts and operation names, never command output or key material.

### Local smoke without the full stack

`hubctl tools` serves the same MCP surface over stdio, so a real sshd (for
example `lscr.io/linuxserver/openssh-server` in Docker) is enough to exercise
it end to end:

```sh
HUB_USER_ID=me HUB_STATE=/abs/state HUB_SSH_CONFIG=/abs/ssh/config.yaml \
  HUB_SSH_WRITE=true HUB_SSH_SHELL=true HUB_SSH_TUNNEL=true \
  hubctl tools --workspace /abs/ws --archive /abs/arch
```

Send `initialize`, `notifications/initialized`, then `tools/call` JSON-RPC
lines, or attach any MCP client (Inspector, an IDE). `HUB_USER_ID` and
`HUB_STATE` matter on a bare host: the audit ledger needs an absolute path
and drops events whose principal is empty; the rendered runtime always sets
both. This path exercises `file:` refs only — `broker:` refs need a running
Credential Broker deployment.

## Media provider credentials (hub-media)

`image_gen` renders the `hub-media` sidecar. Provider keys are never written
to a space env file: the service materializes the Credential Broker grant
named by `HUB_MEDIA_BROKER_GRANT` on each call (a five-minute in-process
cache bounds lease churn; a broker outage fails the next call closed and
`/healthz` reports the engine not ready). Provisioning is one-time, like the
other `broker-secrets-*` volumes:

1. Generate the ed25519 keypair (`media.private` 64 bytes, `media.public` 32
   bytes, raw). Seed `media.private` and the broker's `server.crt` into the
   project's `broker-secrets-media` volume with mode 0600 owned by uid 10001 —
   the same procedure used for `broker-secrets-runtime`/`-toolhub`/`-communication`.
2. Register the public key in the broker `trusted_keys`:
   `{"id": "media", "issuer": "hermes-media", "public_key_file": ".../keys/media.public",
   "audiences": ["broker:control", "broker:runtime"]}` — acquire needs
   `broker:control`, materialize/release need `broker:runtime`.
3. Install the `media-provider-key` contract
   (`services/credential-broker/examples/contracts/media-provider-key.json`)
   into the broker contracts dir; it delivers the secret as
   `HUB_MEDIA_UPSTREAM_KEY`.
4. Create the credential through the broker form (paste `FAL_KEY` or the
   OpenAI-compatible upstream key), then create a grant bound to workload
   `hub-media` via `brokerctl` (`POST /v1/credentials/<id>/grants` as the
   `toolhub` control identity) with `binding_id`/`workload_id` `hub-media`,
   `execution` `dedicated` and the signing actor `policy_version`
   `hub-media` — the service presents that stable service-level policy, so
   grants survive settings changes that would rotate the per-config policy
   hash. Note the grant id.
5. Write `HUB_MEDIA_BROKER_GRANT=<grant-id>` into `spaces/<user>/media.grant`
   (not a secret — just the grant ID) or export it on the host, then
   re-render (`hubctl render`/`up`). Revoking the grant or credential takes
   effect at the next call — nothing caches beyond the five-minute TTL.

With no grant set, `hub-media` falls back to `HUB_MEDIA_UPSTREAM_KEY`,
`OPENAI_API_KEY` or `FAL_KEY` in its own process env — for standalone runs
outside the rendered stack. The rendered compose does not mount a media env
file; a fal engine with neither grant nor env key refuses to boot.
