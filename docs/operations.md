---
description: Current operations and planned scale-to-zero runtime lifecycle.
last_verified: 2026-09-13
---
# Operations

Use `hubctl --user <id> --env dev|prod`; prod is the default. `--dir` remains an
explicit legacy alias. `render` writes generated files under
`spaces/<user>/generated/`, `up` builds and starts the selected runtime, `down` stops
it without deleting data, and `logs` tails the selected Compose project.

When Telegram is enabled, gateway mode supervises `hub-communication`. It maps numeric
sender IDs from the configured allowlist to the selected user scope, writes durable jobs
and replies under `/state/gateway`, and runs one fresh bounded Hermes process per job.
The current deployment uses one configured user; adding another is a configuration and
isolated-space operation, not a shared Hermes home.

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

An explicit owner `KEY=value` chat message is intercepted before Hermes, persisted
through that store, and best-effort deleted. Replies contain names and status only.
Groups reject credential entry. Set and delete also load the ToolHub registry
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

Back up all `spaces/<id>` homes and the `communication-hub-data` volume to encrypted
storage while services are stopped. Ciphertext backups use `hubctl secret backup` and
must not include the encryption key. Do not use `docker compose down --volumes` unless
deleting that user's data intentionally. Restore organization and user homes separately;
never merge memories, Telegram sessions or provider credentials.

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
cannot authorize any mutation. Projection changes write `toolhub-reconnect.request`
without restarting Hermes. `HUB_TOOLHUB_AUTOSTART=true` starts the shipped
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

Example from the repository root:

```text
HUB_SUPERVISOR_AUTH=<host-control-token> hubctl supervisor --spaces spaces
HUB_SUPERVISOR_AUTH=<host-control-token> HUB_RUNTIME_SUPERVISOR_URL=http://host.docker.internal:8765 hubctl render --dir spaces/alice
```

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

A verified private owner may enqueue a one-shot task with
`/runat <RFC3339 timestamp with timezone> <task>`. The communication spool saves
its occurrence before acknowledging it. Due occurrences become ordinary jobs
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
and current organization/tool authorization still apply. `/cancel <job_id>` is
durable during queueing, startup and execution and never starts a new task.

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
hubctl select-execution --dir spaces/alice --user alice --env prod --spool /mnt/alice-gateway --execution-mode supervisor --supervisor-url http://<private-host-address>:8765 --native-cron disabled --compatibility-release 0.2.1
hubctl select-execution --dir spaces/alice --user alice --env prod --spool /mnt/alice-gateway --execution-mode supervisor --supervisor-url http://<private-host-address>:8765 --native-cron disabled --compatibility-release 0.2.1 --apply
hubctl up --dir spaces/alice --user alice --env prod
```

The supervisor URL must be reachable from both the host CLI and the gateway container;
configure its listener on the intended private interface and provide
`HUB_SUPERVISOR_AUTH` to the CLI and Compose invocation. The default loopback listener
alone is not reachable from a Docker container through `host.docker.internal`.
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
