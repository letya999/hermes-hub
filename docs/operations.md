---
description: Operator runbook — stack lifecycle, user onboarding, diagnostics, secrets, links, broker ops, migrations, backups, image hygiene, supervisor, rollout, preflight.
last_verified: 2026-11-17
---

# Operations

The authorization model behind every command here is in docs/rbac.md; space
config vocabulary is in docs/spaces.md; per-service internals in
docs/services/. This file is procedures only.

## Stack lifecycle

### Kubernetes control plane

`deploy/helm/hermes-hub` packages five single-replica services: communication-hub,
supervisor, ToolHub, credential-broker and cliproxy. Images, runtime class, storage,
ingress, network policy and monitoring come from values, without assumptions about
registry, ingress controller, storage implementation or secret provider. Keep real
environment overrides private; `values-kind.yaml` is a synthetic local example.
Verify templates with `just helm-check`.

Prepare these namespaced Secrets and persistent state before installation:

- `hermes-hub-control`: distinct high-entropy `runtime-token`, `supervisor-token`,
  `communication-token`. Never pass values through Helm values, `--set`, argv or
  release metadata. `secretEnv` selects individual keys rather than importing all.
- `hermes-hub-communication`: published `communication.yaml`, with user roots
  under `/data/spaces/<id>` and spool at `/spool`. Set gateway `HUB_USER_ID` and
  ToolHub identity to the provisioned principal. Multi-user installations also
  need their enrolled token file and corresponding service env paths.
- `hermes-hub-channels`: `telegram-bot-token` for the current gateway's required
  Telegram transport. Add Slack bot/app Secret references only when enabling Slack.
- `hermes-hub-broker`: reviewed `config.json`, `master.key`, `tls.crt`, `tls.key`;
  add trusted signing public keys and reviewed contracts through `configItems`
  paths. Use native TLS, the correct public origin, trusted callers and approved
  providers. Place durable state below `/var/lib/credential-broker`, the independent
  witness below `/var/lib/credential-broker-recovery/state`, and ephemeral runtime
  material under `/tmp`. Never enable development HTTP for Pods. The chart copies
  projected files into an owned private memory directory as regular `0600` files.
- `hermes-hub-cliproxy`: reviewed `config.yaml`, auth directory `/cliproxy/auths`;
  provision sessions through the approved credential path.
- Hydrate the spaces PVC with published spaces and the existing ToolHub store at
  `HUB_TOOLHUB_STORE`. Configure ToolHub's two broker clients and the gateway's
  approval client through their existing `HUB_CREDENTIAL_BROKER_*` env/file contract
  when enabled. The chart does not invent accounts, grants or a blank production store.

Alternatively `externalSecrets` accepts remote references and an existing SecretStore.
The ExternalSecret v1 CRD/operator must already exist; the chart installs neither
operators nor store credentials. Rotate Secrets externally, then restart affected
Deployments to refresh env and private file copies.

```bash
helm upgrade --install hub deploy/helm/hermes-hub --namespace hermes-hub \
  --create-namespace --values /private/control-plane-values.yaml --wait --timeout 5m
kubectl -n hermes-hub get deployments,pvc,networkpolicy
```

PVCs survive uninstall; `existingClaim` reuses prepared storage. Single writers use
Recreate. Default co-location with supervisor permits shared RWO spaces/spool claims;
use compatible RWX storage and `colocate: false` to distribute Pods. This is not HA
or multi-writer storage. Back up state and independent broker recovery before upgrades.
Empty `runtimeClassName` omits the field and uses the cluster default (normally runc);
a nonempty value requires an installed RuntimeClass and reaches these control-plane Pods.

Ingress is opt-in and serves human routes only, using ToolHub's separate public
listener. Set private host, TLS, controller authentication annotations, permitted
paths and ingress-controller selectors. Keep `/v1` private. NetworkPolicy permits
DNS, explicit service edges and public HTTPS for gateway/cliproxy only. It requires
an enforcing CNI. Standard policies cannot allowlist FQDNs: for mandatory egress,
set `publicHTTPS: false`, configure `HUB_EGRESS_PROXY`, and permit only its exact
destination/port through per-service `extraEgress`. Telegram polling and Slack Socket
Mode (`SLACK_TRANSPORT=socket`, bot/app tokens via `secretEnv`) need no public webhook.

ServiceMonitor requires its CRD and matching monitoring selectors. It scrapes the
implemented gateway/supervisor `/v1/metrics` with separate Secret Bearer references;
other services have no invented metrics endpoint. TCP probes prove listener
availability. Authenticated `/v1/status` and metrics remain separate acceptance gates.

For local acceptance create an isolated kind/k3d cluster, build source images through
scoped `hubctl build`, and load core/control images with `kind load docker-image
--name hermes-helm <core-image> <control-image>`. Install synthetic state/Secrets and
the chart with `--kube-context kind-hermes-helm -f deploy/helm/hermes-hub/values-kind.yaml`.
Use explicit context on all commands. Record source SHA/image IDs; verify authenticated
status/metrics, unauthenticated 401, restart persistence, DNS/TLS channel API egress
and policy denial on an enforcing CNI. TLS reachability does not prove account delivery;
real channel acceptance requires provisioned accounts and explicit sends.

The supervisor currently spawns Docker containers. Native runtime Pods (#58), runtime
RuntimeClass propagation, per-space model relays, workload-controller and media
deployments remain separate work. This chart grants no Kubernetes API authority,
mounts no Docker socket/hostPath and runs no privileged compatibility daemon.
An empty-runtime status response does not prove a functioning agent deployment.

`hubctl --user <id> --env dev|prod` (prod default; `--dir spaces/<id>` is the
explicit form). `render` writes generated files under `spaces/<user>/
generated/`; `up` builds and starts; `down` stops without deleting data;
`logs` tails the Compose project. Pass service names to scope:
`hubctl up --dir spaces/alice --env dev communication-hub toolhub` — scoped
`up` skips dependencies, list them explicitly. `hubctl build` then
`hubctl up --no-build` converges the stack on existing images without forcing
healthy containers to restart. For a single Compose
service, use `hubctl build --dir spaces/alice communication-hub` followed by
`hubctl up --dir spaces/alice --no-build communication-hub`; this does not
rebuild runtime or cell images. Build a spawned runtime alone with `hubctl
build --dir spaces/alice --image runtime`; `cli-tools` and `cell-proxy` are
the other standalone image choices. Unscoped `build` still builds the full
stack for first setup.
`space apply` publishes authored passport changes; editing Go code alone does
not require it. After a ToolHub binary edit in a running dev stack, use
`hubctl dev-rebuild --dir spaces/alice --env dev toolhub`. For a base-image
build, select `hubctl build --dir spaces/alice toolhub`, then `hubctl up --dir
spaces/alice --no-build toolhub`. Neither path should recreate unrelated
services. Agent-tool contract changes additionally require catalog rebind
and managed runtime respawn.
`hubctl connector link` reads only a live action link and refuses expired/spent
confirmation. Use the explicit `hubctl connector resend --target <onboarding-id>`
write action to renew a pending link; it requires connector lifecycle authority.
Changing `config.web.backend`, `search_backend`, `extract_backend`, or
`search_providers` also recreates that space's egress relay before the
managed runtime respawns, so the configured provider hosts become reachable.
For a stack service change, `space apply` uses the same scoped image builder
and then recreates that service. A successful image build alone is not a
successful service activation; managed Compose files have no build sections.
The base-image Dockerfile still shares a Go build stage and a `cliproxy` binary
across control services. A cold `build toolhub` can therefore rebuild those
stages even though Compose selects only ToolHub; use `dev-rebuild` for Go-only
iterations while the dev base image exists.
The common Broker binary is built with SQLCipher so existing activated
encrypted stores remain readable. Its SQLCipher dependency layer is independent
of the hub Go source tree, avoiding recompilation after a ToolHub-only edit.
The generated model registry is group-readable (`0640`, mounted directory
`0750`) for model-relay's `10001:<host-gid>` identity on Linux. Credential
auth/env files retain their private modes.

`hubctl build`/`up` run `docker compose config --quiet` first; supervisor
mode also needs `HUB_SUPERVISOR_AUTH` or a provisioned `supervisor.auth`.
`up` rejects a second `communication-hub` on the same Telegram bot token and
a second cliproxy on the shared alias — stop the conflicting gateway
intentionally.

M32 lifecycle acceptance runs separately with `go run ./cmd/devcheck
m32-lifecycle <HEAD-image> dev|prod`; both exhaustive Docker recipes and CI
include it. The runtime image and `hermes-hub:test-core` script/relay image
must both match HEAD; CI pulls the selected environment's core role first.
The gate also requires clean committed source; modified, staged or untracked
source files cannot be combined with images labeled as the old HEAD.
Its group leg uses a dedicated group home, simultaneous private/group runs
and checks actual model prompts. Private runs also exercise right revocation
and confirmed regrant through already-open MCP sessions.
It needs isolated host ports 8090/8081 and creates only disposable
users, runtimes and relays. Use CI for this fault test; local service iteration
continues to use existing dev spaces and scoped rebuilds.
The clock/outage/delivery fence can also run without Docker:
`go test -tags integration -short -run '^TestM32ReminderClockAndDeferredDelivery$'
./cmd/toolhub`. It uses real ToolHub/gateway code and temporary storage with a
local channel fixture; it does not exercise agent execution.
Assignment source reconcile, pause/resume, CAS and restart recovery can run
with `go test -tags integration -short -run
'^TestM32AssignmentReconcileAndRecovery$' ./cmd/toolhub` without Docker.
Dreaming's real relay/PDP/vault path, two timezones, spring DST gap,
commit-time revoke, restart deduplication and deferred fixture delivery run with
`go test -tags integration -short -run
'^TestM32DreamingRelayIsolationAndRecovery$' ./cmd/toolhub`. This uses temporary
owners and controlled clock ticks; it does not prove process-crash recovery.
Managed agent-tools calls acquire their own runtime lease, so direct file
calls and watch probes do not require a preceding model run to warm the
executor. ToolHub re-authorizes the tool after startup and releases the lease
when the call finishes.
On communication restart, replay keeps historical run receipts but skips a
delivery whose channel identity is invalid. The rejected audience is never
queued for sending; the recovery log records its job ID for investigation.

## User onboarding — the capability ceremony

**Fast path (standard spaces).** With an infra space already running, a
reviewed provisioning profile and per-space secrets in place, one command
takes a principal to a running managed runtime:

```
hubctl provision --user <id> --env dev            # dry-run plan + digests
hubctl provision --user <id> --env dev --confirm  # commit and start
```

It scaffolds `spaces/<id>` from `policies/profiles/<name>/` (default
`standard-user`) when passports are absent, materializes unique per-space
ports, runs aggregate preflight (all six passports parse, secrets present,
grants vocabulary inside the pinned rights catalog, infra store reachable)
before any write, then commits the capability bundle — ceiling policy,
bound profile, governance right grants and the agent-tools binding — as
one confirmed batch, runs `space apply`, starts the space's compose on
existing images (provisioning never rebuilds; `hubctl build` stays an
explicit action), and enrolls the sibling runtime/model-relay tokens with
the infra owner. Re-running converges: identical records replay as no-ops,
already-active right grants are skipped, missing pieces are filled in.
`--skip-up` stops after `space apply`.

The `standard-user` profile ships shared cliproxy/infra services with
per-space browser/egress-relay/model-relay/toolhub-relay, the reviewed
capability ceiling in `policy.yaml` (stamped with the bound definition
digest at commit time) and a grants ask equal to the minted right ceiling.
Fill `secrets.<env>.env` with at least `OPENAI_API_KEY` before running —
preflight reports every missing requirement in one list.

**Manual ceremony** (custom policies, org ceilings, non-standard toolsets)
stays the documented path below.

A new space has exactly six authored configuration inputs under
`spaces/<id>/passports/`: stack/config/models/workspace/tools/grants.yaml.
Only a validated published revision is consumed. Issued rights, credentials,
channel links and generated outputs are separate protected or operational state.
Tool selection grants no authority; a managed profile binds the exact
`{principal, context, runtime, environment, generation}` tuple.

1. **Scaffold** — `hubctl init --user <id>` creates the six authored passports,
   `secrets.{dev,prod}.env`, `SOUL.md`, profile metadata and state directories.
   Init never overwrites an existing space. Provisioning profiles use those
   same six passport templates; no retired root configuration is created.
2. **Stack/config/models** — author identity and service modes in
   `passports/stack.yaml`; timezone, ports, channels and memory in
   `passports/config.yaml`; default route, service endpoints, model IDs and
   credential references in `passports/models.yaml`. Managed model traffic
   reaches the per-owner model relay. Raw provider names are not logical route
   IDs. Missing or malformed published configuration refuses startup.
3. **Workspace/tools/grants** — declare filesystem boundaries in
   `passports/workspace.yaml`, toolsets and profile/generation binding in
   `passports/tools.yaml`, and requested operations in `passports/grants.yaml`.
   Selection stays independent from issued rights; requested grants must fit
   the operator policy and current issued ledger. These documents do not
   activate until `space apply` publishes them.
4. **Secrets** — provision the declared credential references through the
   Broker/provider enrollment flow and fill required local deployment secrets.
   Never put credential bytes in passports, commit secret files or paste them.
5. **Capability records** — publish into the infra space's protected store
   (`spaces/<infra>/runtime/toolhub/store.json`, or the activated `store.db`;
   infra ownership comes from the published stack passport):
   - ceiling policy: `hubctl capability --kind policy --file policy.json
     --toolhub-store <store> --confirm` (the `policy_id` the profile binds)
   - profile: review a draft first with `--kind preview --file profile.json
     --toolhub-store <store>` (read-only old→new diff), then
     `--kind profile --file profile.json --confirm`. The record's
     `principal_id`/`context_id`/`runtime_id`/`environment`/`generation`
     must equal the published stack identity and tools profile binding.
   - agent-tools surface: `hubctl capability --kind agent-tools --principal
     <id> --context <id> --runtime <id> --policy-version <v>
     --toolhub-store <store>` — registers the reviewed hub-tools catalog
     definition and enables it for the identity. On a policy rotation,
     `DeriveRights` (the passport-derived rights writer) repins eligible
     credential-free bindings onto the new `policy_version` automatically —
     the binding follows the pv the platform already moved the profile to.
     The manual `--version <v>` flag repins the binding onto an
     already-registered definition when the profile's selections pin that
     version instead of the binary's compiled catalog; it remains the
     operator path for credential-bearing bindings and catalog-version
     moves. A pv-stale binding projects zero tools.
     A new immutable definition digest also needs reviewed policy-ceiling and
     profile-allow rules for the same permitted actions. `space apply` repins
     selections but does not approve a new implementation digest; preview the
     profile before confirming it. A digest mismatch hides those tools.
     For a catalog-version move, run `hubctl connector catalog-enable
     --toolhub-store <absolute-store> --definition hub-agent-tools
     --version <v> --principal <id> --context <id> --runtime <id>
     --policy-version <pv> --profile <profile-id> --env <env>
     --extend-ceiling` after registering the definition. This operator step
     adds the reviewed digest's rules and repins sibling profiles; repeat for
     each principal that selects the new version.
   Every record prints a review digest first and refuses without
   `--confirm`; the store re-checks issuer identity at admission.
6. **Channel routing** — for Telegram delivery add the user to the infra
   space's `communication.users.yaml` (`id`, `runtime_id`, `telegram_ids`,
   `features`, `policy_version`) — no entry, no routed jobs; an entry with
   empty `telegram_ids` still jobs but cannot notify.
   The enrolled sibling runtime token is authoritative for its current
   `policy_version`: communication-hub resolves it when routing each request,
   and the generated automation routing uses it after sibling `space apply`.
   A missing or ambiguous enrollment disables that sibling route. After a
   manual channel edit, render the infra space and recreate only
   communication-hub so its loaded channel IDs agree with ToolHub's routing file.
7. **Bring up** — `hubctl doctor --user <id>`, then
   `hubctl up --user <id> --env dev|prod`. Up renders `generated/` (compose,
   `tool-policy`, `tool-manifest`, hermes config), mints `runtime.auth`,
   and enrolls sibling `toolhub-tokens.json`/`model-relay-admins.json`.
   The supervised runtime spawns on first demand (pin or job).
   Missing credentials for an optional connector (including a selected MCP
   server) produce a warning in `doctor` and `up`; operations requiring that
   credential may fail, while the rest of the space can start.
   Missing model or channel credentials and invalid security configuration
   remain blocking errors.

   A dedicated CLIProxy's init companion retries in the same container after
   a temporary Broker failure; its healthcheck only passes after both the
   session file and generated config exist. A persistent missing or revoked
   grant still keeps the proxy unavailable and needs credential enrollment.

   A published infra owner has two generated Compose plans: `compose.shared.<env>.yaml`
   contains only `shared` services in the standalone `hermes-hub-shared-<env>`
   project and preserves its original volume names; `compose.user.<env>.yaml`
   contains that owner's `per-space` services in a separate project. `hubctl up`
   starts the shared plan first, then the user plan. Other users keep one
   per-space Compose project each. Run `hubctl` for lifecycle operations so a
   later up cannot rejoin the two plans.

   A *managed* space additionally needs, before `up`: an execution selection
   (`execution.<env>.json` — `mode: supervisor`,
   `supervisor_url: http://supervisor:8876`, `native_cron: disabled`),
   six authored passports published with `hubctl space apply --dir
   spaces/<id> --env <env> --confirm`, and reachable model credentials — either `cliproxy: shared`
   against the infra instance or `cliproxy: dedicated` with the space's own
   `cliproxy/` directory (`config.yaml`). Dedicated instances are enrolled
   with `hubctl models login --dir spaces/<id> --service <name>`: submit each
   reviewed link (OAuth session + client key), re-run to record the grants in
   `generated/model-grants.json`, and keep `OPENAI_API_KEY`,
   `HUB_CLIPROXY_MGMT_URL`=`http://cliproxy-<id>:8317`,
   `HUB_CLIPROXY_MGMT_KEY`, `HUB_CLIPROXY_AUTH_INDEX` in `secrets.<env>.env`.
8. **Verify tool surface** — `docker exec <runtime> cat
   /config/tool-manifest.json` shows the projected manifest;
   `hubctl tools --workspace <dir>` lists the reviewed vocabulary. A user
   whose tools are all missing means step 5's profile doesn't match the
   rendered generation/env — compare `policy_version` and `generation`.

**Revocation**: release runtime pins (`DELETE /v1/pins` on the supervisor
or let TTL reap), `hubctl down --user <id>`, remove the
`communication.users.yaml` entry, publish a new profile/policy revision
with `status: revoked` (revisions only ever move forward — never edit the
old record in place), then archive or delete `spaces/<id>`.

## Diagnostics

On by default. Each stack's ToolHub appends new stdout/stderr lines every
15 s — scoped to its own project label plus runtime containers on that
user's agent network — into `spaces/<user>/diagnostics/
hermes-diagnostics.txt` (100 MiB active + at most three rotated files, seven-day
retention; bounded
per-pass and per-call so a stalled daemon cannot starve the cycle). Host
supervisor mirrors its own logs to `.local/diagnostics/supervisor.log` (10 MiB active +
three rotated files); a persisted generation fences offset reuse. The
mirror stays disabled if a host bind mount rejects the required directory
permission change; supervisor still runs, but host diagnostics remain incomplete.
collector copies only lines naming that space's owner as a whole token.
Compose keeps Docker `local` rotation (10 MB × 3/container). Structured producers
allowlist metadata; collection and agent projection exclude bodies/unknown fields
before redaction and search. Source reads stream from oldest unread with timestamp
and tie ordinal, never last-5000 tail. Data sync precedes atomic cursor replacement;
replay keeps source_position. Source/cursor/rotation loss emits diagnostic_gap;
unreadable stopped containers are not marked drained. Published passports currently expose no collector-disable field. Editing
retired `agent.yaml` does not change collector configuration.

Rotation requires the dedicated `.local/diagnostics/` directory bind; a single-file
bind pins the obsolete inode. The directory contains diagnostics only, not host
auth/control files. Existing stacks need an explicit re-render/recreate during
CHG-0217 acceptance; do not deploy only a new collector against the legacy mount.
Container/volume-loss proof and catalog rebind/respawn remain pending for managed
spaces that have not been recreated with the new image.

Gateway message logs carry IDs, status and `text_len` — never bodies;
bracketed markers (`[voice message]`, `[credential input redacted]`) still
print. Temporary content window: `HUB_LOG_CONTENT_UNTIL=<RFC3339>` in the
channel env + restart; clamped ≤1 h ahead, auto-reverts. Treat the combined
file as private; `.local/` and `spaces/` are git-ignored — redact before
sharing.

Structured lifecycle logs are JSON: `time`, `level`, `event`, `component` plus
`job_id`/`run_id`/`principal`/`status`. `hubctl trace <job_id> [--user] --env dev|prod`
merges only authority-bound supervisor/home/container metadata by exact job identity;
cross-principal jobs are refused. `/status` reports caller-scoped health:
p50/p95 receive→deliver latency, unconfirmed/failed counts, restart counter,
average input tokens.

Agent-visible diagnostics: `mcp__toolhub__diagnostics` returns the caller's
own bounded lifecycle metadata across rotated files and workload health, plus
`excluded` projection
reasons (`policy_mismatch`, `disabled`, `revoked`, `stale`, `degraded`,
`not_found`). Needs a `diagnostics` control-operation grant; scoped by
authenticated identity, never arguments.

Operator ops surface (ADR-0077): `hubctl status [--dir <space>]
[--supervisor-url <url>] [--gateway-url <url>] [--json]` answers "what is
running, what failed, how deep is the queue" from the two bounded
control-plane endpoints — supervisor `GET /v1/status` (runtimes as
phase+named conditions from the closed #209 vocabulary, holds, crash
budgets, job counts) and gateway `GET /v1/status` (spool queue counts,
outbox depth/age, routine next-due/overdue). Read-only and
bearer-authenticated (supervisor.auth token chain); unreachable planes
degrade to an `unreachable` line rather than a fabricated section, and it
never wakes runtimes. `hubctl usage --spool <dir> [--days N]
[--actor u] [--model m]` aggregates the durable `usage.jsonl` journal per
scope and actor — read-only, tolerant of a torn tail. `hubctl watch
(--job <id> | --scope <user>) (--spool <dir> | --supervisor-url <url>)
[--timeout <d>]` prints timestamped transitions until terminal; job mode
additionally reports outbox delivery (pending/delivered/uncertain) and waits
through the completion→outbox gap, and `--scope` falls back to per-job spool
mappings when no supervisor URL is given. `hubctl image
promote|revert|canary|status --dir <space> --env <env>` moves the runtime
image pin only behind a passing smoke and always keeps the last-known-good
pin for one-command revert. `hubctl replay export --job <id> --spool <dir>
-o <dir>` writes a sanitized incident bundle (digest-verified `--prompt-file`
for sensitive jobs); `hubctl replay run --bundle <f> --replay-dir <dir>`
replays it on an isolated spool with the channel forced to mock, printing a
divergence line. Export with `--space` hashes the current complete published
six-passport bundle; incomplete activation and document symlinks are refused.
Space configuration is published with `hubctl space apply`
from the six authored passports. The retired declarative `apply -f space:`
flow is not an agent configuration authority.

Storage accounting (SPEC-0088): `hubctl storage status --spool <dir>
--toolhub-store <path>` prints per-owner allowlisted aggregates only —
logical bytes, file counts, byte/file limits, completeness and a safe status
word per storage owner (`communication-spool`, `toolhub-state`); paths, object
names and raw errors never leave the process. `hubctl storage cleanup --spool
<dir> [--retention 72h]` is a dry-run by default: it emits a digest-bound plan
(scope, aggregate files/bytes, `sha256:` digest — never file names)
over the owned tree, rejecting planted symlinks. `--apply
--digest <sha256:...>` takes the spool writer lock, recomputes the plan and
refuses a stale or foreign digest before running the bounded terminal
compaction — aged done/failed records move to the archive, live
pending/running state and open approvals are never touched. Offline apply
also uses the gateway's configured terminal-file, archive-byte and usage-entry
limits, so overflow is archived even when its file mtime is recent.
Orphaned routine crash-replay sidecars older than retention are removed only
when their live occurrence is absent; quarantined occurrences keep theirs.
For an overfull schema-1-only ToolHub audit, stop the selected ToolHub and run
`hubctl audit rollover --ledger <absolute audit.jsonl>` to review safe counts
and the digest. Apply with `--apply --digest <sha256:...>` while it remains
stopped. The command verifies the mode-0600 gzip archive before replacing the
active ledger; keep that archive for historical investigation. The new live
ledger begins with a rollover marker. Do not apply this path to schema-2 or
segmented audit files; they require reconciliation of operation outcomes.

## Overload admission

ToolHub treats overload as explicit state: an exponential-moving-average of
interactive request latency (decide, memory-context, tool-call, MCP) over a
40 ms threshold sheds background endpoints (`/v1/memory-capture`,
`/v1/admin/diagnose/`) with `429` + `Retry-After` before any work runs; a
rising-edge log line marks the transition. The in-process automation tick
defers its whole pass while the gate holds — due work keeps its catch-up
window. Communication-hub's capture pump applies the matching client-side
adaptive throttle (SRE `P = max(0,(requests−2·accepts)/(requests+1))` over a
decayed 2-minute window, capped at 0.9 so a recovery probe always passes)
plus a fail-stop of three consecutive transport or 429 refusals; per-event
refusals never hold up siblings, and events older than 24 h settle as
expired (the ack record is the audit trail). Depth beyond 32 pending
captures logs `capture outbox depth=… oldest=…`.

## Operator mutations require `--confirm`

`hubctl grant`, `hubctl capability`, `hubctl governance`, `hubctl secret`,
remove/revoke paths: without `--confirm` the command prints the review digest
and refuses; with it the record carries `{digest, issued_by, confirmed_at}`.
The store re-checks at admission — one identity cannot stamp for another,
`model`/`hermes` can never mint. Grant revision 1 is default; changing a
record needs a higher revision — older writes cannot undo a revoke.

```text
hubctl grant --user alice --toolhub-store /protected/toolhub/store.json \
    --kind control-operation --operation status --confirm
hubctl capability --kind policy|profile --file reviewed.json \
    --toolhub-store <path> --confirm
hubctl profile grants --dir spaces/<user> --profile <name> \
    --policy org-policy.json --out draft.json                          # grants.yaml → draft defaults, ceiling untouched
hubctl profile diff --dir spaces/<user>                                # 3-way plan via profile.lock
hubctl profile apply --dir spaces/<user>                               # untouched files update; customized kept+reported
hubctl org sync-members --org acme --org-dir spaces/acme \
    --spaces spaces --dry-run                                          # report, then drop --dry-run to provision
hubctl capability --kind group --file g.json --toolhub-store <path>   # validate+print digest only
hubctl capability --kind preview --file draft.json --toolhub-store <path>  # old→new diff, never writes
hubctl space apply --dir spaces/<user> --env dev --confirm             # publish passports/tools.yaml with separate grants.yaml
hubctl governance --kind rule --scope user --user alice \
    --section native:terminal --effect allow --reason "reviewed" --confirm
hubctl governance --kind grant --user alice --section user_mcp --expires 72h --confirm
hubctl governance --kind right --user alice --cap workspace --op write --expires 168h --confirm
hubctl governance --kind revoke-right --user alice --grant right-alice-workspace-write --reason "offboard" --confirm
```

`--kind right`/`revoke-right` mint and revoke RBAC capability grants
(`capability.operation` from `rights.yaml`; `--bind` scopes path/model/
toolset/target). Issuers are enforced against the vocabulary — cross-subject
rights are never operator-minted. Passport-derived `right-<user>-*` rows are
rewritten on every publish; manual grants live alongside and stay bounded
by the published ceiling.

Connector verbs run identically on three surfaces — `connector_*` tools,
`hubctl connector <verb> --target <sel>` (`--profile`/`--env`/`--generation`),
and `POST /v1/connectors:*` on the control listener — all through
`ControlPlane.InvokeConnector`, one journal.

Against a running ToolHub prefer `hubctl connector <verb> --service`: it posts
the verb to `/v1/connectors:invoke` so the live listener owns the store write
and the CLI never trips the save-side stale fence. The bearer resolves from
`--token`/`HUB_TOOLHUB_TOKEN` or the matching principal in the
`toolhub-tokens.json` found walking up from `--toolhub-store`;
`--toolhub-url`/`HUB_TOOLHUB_URL` overrides the default `127.0.0.1:8090`.
A bare `status --service` reads `GET /v1/connectors`. `link` and `confirm`
stay file-mode — they mint and read protected nonces, which never cross the
service surface.

Team connectors (ADR-0058) are operator-published, not conversational:

```text
hubctl capability --kind team --file reviewed-team.json \
    --toolhub-store <path> --confirm                     # publish {team_id, members, revision}
hubctl capability --kind team-share --toolhub-store <path> \
    --team <team-id> --definition <id> --version <v> \
    --backend credential-broker --locator <locator> \
    --broker-context <broker-context-id> [--keys k1,k2]  # mint the team-owned shared connection
hubctl capability --kind team-drop --toolhub-store <path> \
    --connection-id <conn-id>                            # revoke + finalizers, zero shared resources
```

`team-share` requires a `shareable: credential|connection` definition and
stores the operator-supplied Broker contract (`backend`, `locator`,
`broker-context`) on the credential reference; member bindings lazily mint
one Broker grant each under that context. `team-drop` revokes and runs the
same finalizer ladder as a single-owner remove — member bindings, grants
and workloads are all retired. Team credential rotation goes through
`Store.RotateCredential`/`RotateCredentialRecord` on the team connection —
the same store op the control layer uses for credential resubmission; it
bumps the reference revision and retires every member grant, and each
member's next call lazily re-mints under the team context with no member
reconnect. Membership
changes go through a new `team` record revision; a removed member's grants
and workload are purged on write.

`policy_version` upgrade procedure: inventory active managed profiles and
runtime envelopes first; binding migration does not reissue profiles — an
old profile can fail startup with `capability runtime scope`. Back up the
registry, publish a new revision bound to the rendered policy version under
the real operator identity, verify startup + each principal's tool manifest.
Never bypass scope validation or silently remint authority during render.
Rollback needs the matching old rendered config + envelopes + registry
backup as a set.

## Secrets

Ciphertext at `spaces/<user>/runtime/credentials/store.enc`; key in
`HUB_CREDENTIAL_KEY`/`_FILE` (default `%APPDATA%/hermes-hub/credential.key` /
`~/.config/hermes-hub/credential.key`) — outside the store dir and backups.

```text
hubctl secret set|list|delete|expose|backup|restore|rotate-key|activity --user <id> [--name N]
```

An owner `KEY=value` chat message is intercepted, rejected, best-effort
deleted — never persisted; the gateway returns a one-time protected loopback
form generated from the connector recipe. Secret mutations load the ToolHub
registry so rotate/revoke cut live MCP sessions and stop workloads. Failed
rotates leave the connection `degraded`. `HUB_CREDENTIAL_STORE` must be set
on `hub-toolhub` for decrypt-and-inject; it is inert on `hub-communication`
(the gateway never opens the store or audit ledger).

Legacy owner credential writes require the configured audit ledger. A Set batch
uses one durable owner-scoped admission and completion reserve; validation runs
before any write. An interrupted batch fences further credential mutations for
that owner until reconciliation; another owner does not inherit that fence.
Post-effect audit/backend/projection failures return `uncertain`, preserve
ciphertext and quarantine attempted records as degraded, never delete them as
rollback. Updating one value preserves siblings in a readable multi-key object;
unreadable siblings are not implicitly replaced. Rotation must target a connection
owned by the same owner. Invalid rotation still closes credential access; only a
verified downgrade is a known failed execution. Delete/terminal-disable continue
access revocation even if audit fails, and unverified cleanup returns uncertainty.
`secret activity` shows operation/phase/auth/execution/delivery fields without
credential dictionary names. This is not a migration or a live provider receipt.

The shared runtime enrollment and model-admin maps stay on the same inode for live bind mounts and use mode `0640` with the operator group, matching the agent UID plus operator GID used by Compose. Resetting them to owner-only `0600` after a root render makes the service unable to read its authorization map.

## Effective system instructions

Skill descriptions are truncated at Unicode character boundaries. Materialization rejects invalid UTF-8 before replacing the previous effective SOUL. This prevents Hermes from silently skipping the whole system contract on a decoding error. After a prompt rollout, verify the mounted file with Hermes' own `load_soul_md`, including the onboarding section; tool endpoint readiness alone does not prove the model loaded its instructions.

## Human-action links

`/credentials`, `/confirm`, `/remove`, `/connect`, `/device` are one-time
`?t=` URLs; spent/expired → 410. The stable wizard link `/connect/<id>?k=`
is the exception: one URL per intake cycle that renders the current phase
(credentials form, broker hop, confirm, OAuth interstitial, progress,
result) and self-refreshes through the flow; it expires with the onboarding
and dies on terminal phases' TTL. Explicit `connector_edit action=reauth` rotates the wizard capability and opens credential entry; it never sends the old confirmation page as a new credential form. Default target is loopback. For other-
device access publish TLS and set `HUB_PUBLIC_ORIGIN=https://<host>` (https
only — anything else mints loopback) + `HUB_PUBLIC_LISTEN=<addr>` on
hub-toolhub; that listener serves only human-action paths (control/gateway
→ 404) and the gateway accepts an https `form_url` only when host equals the
configured origin exactly. `hubctl connector link --onboarding <id>` prints a
pending URL and renews an expired confirmation before printing it; `--confirm`
completes an awaiting-confirmation onboarding through the operator CLI grant.
The confirmation result prints only phase, status, onboarding, binding and
connection state; it does not print recipe or credential metadata.

## IAM enrolment and channel linking (SSO)

Opt-in ADR-0100 surface on communication-hub. Declare it in the published
passport `config.yaml` — the `iam:` block is compile-validated and renders
into the gateway env and (bundled only) two extra shared services:

```yaml
iam:
    mode: external        # or: bundled
    issuer: https://sso.example.com/application/o/hermes
    client_id: hermes-hub
```

- **`mode: external`** — an upstream OIDC provider you run yourself:
  Authentik, Keycloak, ZITADEL Cloud, Entra ID. `issuer` (https outside
  loopback; path issuers like Authentik's `/application/o/<slug>` are
  legal) and `client_id` are required; declaring `identity-hub` or
  `hub-postgres` stack services at the same time is a compile error —
  external means the hub runs no IdP of its own.
- **`mode: bundled`** — the optional in-stack IdP: `identity-hub`
  (ZITADEL v4, digest-pinned) plus `hub-postgres` (the one shared
  postgres — only the IdP consumes it). Declare both in `stack.yaml`
  with `mode: shared`; anything less fails compile and render.
  `issuer` may be omitted in dev — the render derives
  `http://idp.localhost:<ports.identity-hub|8091>` and puts the
  `idp.localhost` network alias on the IdP, so the browser and the
  gateway container resolve one issuer string. An explicit issuer must
  be a host-level URL (ZITADEL serves issuers without a path) and makes
  the deployment publish `127.0.0.1:<issuer port>:8091`.
- Absent `iam:` → channel-mapped local mode, unchanged.

`identity-hub.auth` materializes at render (postgres password,
`ZITADEL_MASTERKEY`, session cookie secret). The masterkey encrypts the
IdP's secrets at rest and never rotates — losing it bricks the store, so
back it up with the other auth files.

Direct env override still works for unmanaged stacks: `HUB_OIDC_ISSUER`
+ `HUB_OIDC_CLIENT_ID` compose passthrough — a published `iam:` block
takes precedence, and a bundled mode without `client_id` renders empty
env so the gateway boots in local mode until bootstrap finishes (it
never starts half-configured). `HUB_PUBLIC_ORIGIN`/`form origin`
supplies the `/iam/oidc/callback` redirect target — register exactly
`<form origin>/iam/oidc/callback` in the provider client (OIDC code
flow + PKCE, `openid` scope; no client secret is stored).

**Bundled bootstrap (manual, once).** ZITADEL initializes itself via
`start-from-init`; after the container is healthy:

1. Open `http://idp.localhost:8091/ui/console` and sign in as
   `zitadel-admin@zitadel.localhost` with the generated password ZITADEL
   prints in its logs on first boot (`docker compose logs identity-hub`
   — the line appears once; rotate the password immediately).
2. Create a project and an application: type **web**, auth method
   **PKCE**, redirect URI `<form origin>/iam/oidc/callback`. The
   application **client id** goes to `iam.client_id` — re-apply the
   passport to project it into the gateway.
3. Create users; each user's **subject** (the stable `sub` claim —
   ZITADEL's opaque user id, not the login name) goes into
   `communication.users.yaml` → `oidc_subject:` for enrolment.
4. Backup scope grows: `hub-postgres-data` volume (the IdP's store) in
   addition to `<spool>/iam/`; `identity-hub.auth` alongside the other
   auth files. Removal: `/unlink` every linked account, drop the `iam:`
   block plus the two stack services, re-apply — the postgres volume
   stays behind for manual deletion, it is never dropped silently.

Two flows, both one-time and 10-minute TTL:

- **Link** — a mapped user runs `/link` in Telegram or Slack; the bot
  replies with `…/iam/link/<nonce>`. The page offers an SSO button,
  the callback verifies issuer/audience/signature/RS256/`kid`/expiry/
  nonce, then binds that channel account to the user's principal.
- **Enrol** — an unmapped Telegram user messages the bot; instead of a
  bare denial it gets the same link. After SSO the subject must match a
  user's provisioned `oidc_subject` in `communication.users.yaml`;
  zero or ambiguous matches deny. Slack accounts enrol through the same
  link minted for their `slack-<team>-<user>` transport.

`/unlink` drops the channel account's binding. Links persist under
`<spool>/iam/` (`iam-links.json` + hashed-subject audit journal) — back
them up with the spool. A linked channel account resolves to its
principal at ingress and its envelopes carry `Issuer`/`Subject`
provenance; unlinked transports deny. One subject cannot bind two
principals — conflicts answer 409 and are audited.

Limitations: provider verification is fixture-tested against the OIDC
contract, not yet against a live ZITADEL tenant; enrol links are offered
on the Telegram no-access path only (Slack enrol via an issued link);
`oidc_subject` is provisioned manually per user — no group/SCIM sync;
SAML stays outside Hermes (front it through the provider if needed).

## Outbound exposure (tunnels)

Opt-in per environment: `features.exposure` + a reviewed
`connections/exposure/config.yaml` (provider `sish`, pinned relay host keys,
`zone`, `key_ref` file:/broker:, bounds, named endpoints). Render fails
closed without a valid config; the config mounts read-only at
`/state/exposure` and `HUB_EXPOSURE_CONFIG` is wired into both Compose and
supervisor-spawned runtimes. `exposure_list` reports the declared endpoint names
and target classes even when no tunnel is open; it does not prove a service's
content is safe to publish.
The mounted directory must be traversable and its config/key files readable by
the runtime's effective group; keep the private key inaccessible to others.
Render and supervised spawn now validate the reviewed config and each `file:`
credential as uid 10001 with the runtime's primary gid before starting. The
same connection spec supplies both Compose and supervisor mounts and env paths.
Managed sish uses the per-space egress relay: its only non-HTTPS CONNECT target
is the exact host and port from the reviewed exposure config. The stack
passport must declare `egress-relay: {mode: per-space}` for this space; the
relay host does not gain generic HTTP egress. The executor never falls back
to a direct relay dial after proxy refusal. Render and supervised spawn reject
a managed sish configuration when that per-space relay is undeclared.
An edit to `connections/exposure/config.yaml` is not hot-reloaded by an
already-running managed runtime. Respawn that owner's runtime after the edit;
verify `exposure_list` again before relying on the new endpoint set.
`hubctl exposure enrol --dir <space>` materializes the relay keypair
(file refs) or stages a broker grant (broker refs) and prints VPS enrolment
steps — it never prints the private half.

The agent gets `exposure_list/status/open/close` — name-only: bind,
target and public name are composed by the hub from the reviewed config
(`<endpoint>.<zone>.<relay.domain>`), never from tool arguments.
`access: ro` keeps list/status and denies open/close. Every transition is
audited (`audit/exposure.jsonl`); opens are capped by `max_exposures` and
reaped at `lifetime_seconds` (background reaper; idle reap deferred until a
provider-side traffic signal exists).
Open requires durable audit admission and completion reserve. An uncertain error
or list/status `uncertain=true` requires reconciliation before repeating open;
unknown effects still consume capacity. Requested close still attempts to stop
access if audit fails and returns uncertainty instead of a confirmed receipt.
Open also holds audit space for closing that exact resource. Verified close uses
the hold, preserves an unknown opening as unknown, and permits a fresh resource
admission. A failed close retains its admitted cleanup for a later requested stop;
an unverified close never releases the durable target fence.

`hub_public_listener` targets resolve through `HUB_PUBLIC_TARGET` —
set it to toolhub's public-listener address (e.g. `HUB_PUBLIC_LISTEN=0.0.0.0:8091`
on hub-toolhub plus `HUB_PUBLIC_TARGET=toolhub:8091` in `secrets.<env>.env`
so it reaches the runtime env). It fronts the same restricted human-action
surface as the public listener; it is not a second public surface. Live
sish acceptance requires the operator VPS + domain and is tracked in
CHG-0166.

Relay image pin: upstream sish sends a hostname in the `forwarded-tcpip`
originator field, which strict Go SSH clients reject — every delivered
connection fails until it is fixed (upstream issue 387 / PR 388). Deploy
the relay from the reviewed fork image
`ghcr.io/letya999/sish:v3.0.5-originaddr.1`
(digest `sha256:cdb3195c71d4c69dcc5447def23fb552b9f933324843dc184c955b1b370470a1`,
source branch `letya999/sish:fix/forwarded-tcpip-origin-addr`) until a
released upstream contains the correction; then repin to the upstream
release tag.

## Per-user DNS/TLS addressing

Opt-in per environment: a reviewed `connections/dns/config.yaml` selects
the profile — `provider` (`regru`, `digitalocean`, `gcloud`), `zone` (the
DNS zone the provider API writes), `base` (public suffix for names),
`credential_ref` (`file:` map or `broker:` grant; materialized per call,
released after — never in argv/env/logs/ledger), plus quirks:
`static_egress_ip` for pure reg.ru (REG.API requires a panel IP
allowlist; the profile refuses to load without it — delegate
`hub.<domain>` instead on dynamic IPs), `name_servers` for delegated
profiles, `project`/`managed_zone` for gcloud.

Supported profiles: pure reg.ru (zone on reg.ru NS, static-IP executor),
pure DigitalOcean (whole zone on DO NS), reg.ru-parent + `hub.<domain>`
delegated to GCP Cloud DNS or to DO nameservers. For delegated profiles
`zone` is the parent domain, `base` is `hub.<domain>`; one-time bootstrap
is the parent-side NS records, then `hubctl dns delegate-check` verifies
live NS answers against `name_servers`.

Commands (all `--dir` owner dir): `dns check` prints the profile without
secrets; `dns ensure --ip <addr> <user>` writes the onboarding wildcard
`A` record `*.<user>`; `dns remove --ip <addr> <user>` tears it down;
`dns records` lists zone records; `dns cert --acme <dir-url> <user>`
issues the per-user wildcard `*.<user>.<base>` over ACME DNS-01
(`_acme-challenge` TXT via the same contract) into
`connections/dns/certs/`; `dns push --alias <ssh-host> [--cert-dir
<dir>] <user>` delivers `<user>.{crt,key}.pem` into the sish
`--https-certificate-directory` over the reviewed `connections/ssh`
capability (atomic SFTP write, audited `ssh` events).

The `<user>` positional is bound, not free-form: it must equal the `zone`
label in the space's reviewed `connections/exposure/config.yaml`, so one
space's operator run cannot write or certify another user's names.
Denials are audited (`dns` kind, outcome `denied`). Exposure open/close
makes no DNS call — wildcard records and certs exist independently of
tunnel lifecycle.

TLS lifecycle: `dns cert` is the renewal driver — it skips issuance while
the stored leaf has more than 30 days of validity and re-issues inside
that window; a failed renewal keeps the live cert and appends a
`renew-failed` audit event. The ACME account key is persisted once at
`connections/dns/acme-account.key` (0600). Provider calls carry an
explicit timeout (`dns.Options.Timeout`); propagation to the zone's own
nameservers is polled by `dns.WaitAuthoritative` to a caller deadline.
Live acceptance — a real domain per profile plus a deployed sish — is
separate evidence and is not asserted from httptest fixtures.

## Broker operations

Enablement env layout is in docs/integrations.md; provisioning detail per
service in docs/services/credential-broker.md. Ops-critical facts:

- Broker state lives in one **external** volume per environment
  (`hermes-credential-broker-<env>`); create once (`docker volume create`),
  never point dev and prod renders at it simultaneously. Pre-split
  deployments pin `HUB_BROKER_STATE_VOLUME=<old-volume>` explicitly.
- Initialization evidence lives in the separate external volume
  `hermes-credential-broker-recovery-<env>`, mounted only into Broker at
  `/var/lib/credential-broker-recovery`. `hubctl up` creates it before starting
  infra (or explicitly selected Broker), after configuration/build preflight.
  The updated image seeds an empty volume with service UID 10001 and mode0700;
  existing nonempty volumes are not recursively chowned or modified. Direct
  Compose users must create the external volume first. Keep it
  across primary-state replacement and do not merge dev/prod volumes. The new
  image must support `serve --state-witness-dir`. This marker detects empty-state
  replacement; it is not a credential backup or a rollback fence.
- The broker does not bootstrap an empty volume — it exits `cannot read
  configuration` until config.json + key/ledger material exist; migrating
  means copying volume content while stopped (`docker run --rm --user root
  -v old:/from -v new:/to … cp -a`), then `compose up -d`.
- Broker durable store runs on encrypted SQLCipher (ADR-0087): `primary.db`
  beside the ledger plus `recovery.db` on the recovery volume, one ATTACH
  transaction per commit, sealed manifest + `sqlcipher.activated` markers.
  Migrate with `hubctl store-migrate --dir spaces/<user> --env <env> --broker
  --apply` (stops the service, runs `credential-broker store-migrate` in the
  service image against its volumes) then `hubctl store-activate … --broker
  --apply` (verifies the manifest, flips the markers, retires `ledger.bin`
  into `substrate-backup/` and restarts the service). The dev image build
  needs a CGO SQLCipher toolchain — `hubctl dev-rebuild credential-broker`
  uses the pinned `build` stage of `services/credential-broker/deploy/Dockerfile`.
- Bounded hot audit retention (SPEC-0087 R3/R5): set `audit_hot_max_bytes`
  and `audit_archive_dir` together in the broker config — an audit append
  crossing the byte cap fails as a visible `audit retention full` (HTTP 507)
  instead of silently deleting history. `credential-broker store-retain
  --config PATH` exports the hot audit prefix into an identity-sealed
  `.seal` archive plus an HMAC-authenticated `.manifest.json` and prints a
  JSON report; `--destroy` additionally prunes the verified source prefix in
  one transaction across both legs and persists `audit_floor`. Existing
  archives are verified, never overwritten — a tampered archive or manifest
  fails closed and never authorizes a prune. `/v1/events` answers report
  `earliest_available`/`gap` when a client's cursor predates the floor.
- Records history rotation (SPEC-0087 R5.2): `store-retain --config PATH
  --records --keep-bytes N` exports the records-chain prefix into a sealed
  `records-LO-HI.seal` archive + manifest that re-verifies the internal hash
  chain standalone; `--destroy` deletes the verified prefix across both
  legs, re-anchors `records_floor`/`records_floor_prev` on the archived
  tip hash and VACUUMs the files — the physical shrink the byte cap alone
  never delivers. Reopen verifies the retained chain from the floor; a
  witness head below the floor (or lost on a rotated store) fails closed.
- Archive budgets bound the archive directories themselves:
  `{audit,records}_archive_max_bytes` and `{audit,records}_archive_max_age_days`
  make the tick permanently delete the oldest **verified** archive pairs
  over the limit — except the newest pair per directory, which carries the
  provenance link to the retained floor. Foreign or tampered manifests are
  never deleted (fail-closed). Zero = unbounded (default).
- Automatic high-water maintenance: `audit_auto_retain` /
  `records_auto_retain` are the explicit destructive policies. On the
  service tick, hot audit bytes over 80% of the audit cap export (and prune
  under the destructive flag); hot records bytes over 80% of the ledger cap
  rotate to a half-cap retained suffix (and prune + compact under the
  destructive flag). Without the flags the same checks only export —
  admission still stops visibly at the cap, nothing is silently deleted.
  The outcome persists in journal meta (`retain_last_ok`/`retain_last_error`)
  so a failing cleanup stays visible across restarts; idle and
  already-in-state ticks write nothing.
- Ledger byte cap is a visible admission stop, not a poisoned store:
  `max_ledger_bytes` overflow answers ErrFull (HTTP 507 via
  `ErrRetentionFull`), the journal stays healthy and rotation frees
  headroom for later writes.
- Provider object cleanup retries with exponential backoff (30s base,
  doubling, capped) and durable `attempts`/`next_at` — a persistently
  failing provider is not hammered at a fixed rate.
- `credential-broker store-sweep --config PATH` runs the same bounded
  object-cleanup pass offline with the service stopped — drains abandoned
  and retired provider objects and prints a JSON `{"swept": N}` report.
- File-delivery contracts (e.g. Google service-account JSON) need a shared
  tmpfs at the same absolute path on broker, controller and daemon — set as
  `credential_mount_root` in the controller config; requests reject without
  it.
- `proxy_environment` names must end `_PROXY` (split `*_PROXY_HOST/PORT`
  rejected). A pre-rule `store.json` fails startup with `invalid or duplicate
  proxy environment parameter` — move offending names to `environment` and
  restart toolhub.

## Space publication and data maintenance

Space publication is an explicit dry-run followed by `--confirm`:

```text
hubctl space apply --dir spaces/artem --env dev [--confirm]   # compile six authored passports, publish + reconcile
hubctl spool migrate --spool <dir> --state <dir> [--apply]   # legacy knowledge tree
hubctl context backup|restore|export|purge [--spool <dir>] [--confirm]
```

The publisher checks all six authored files, current issued rights and the
organization ceiling before switching the revision. `spool migrate` copies
(never moves) knowledge into `<state>/knowledge/<principal>`;
other spool dirs (`schedules`, `occurrences`, `watchers`, `digests`, `tasks`,
`events`, `outbox`, `mappings`, `conversations`, `inbound`, `callbacks`) keep
their paths. `context restore` refuses another user and never reactivates
revoked credentials; plaintext secrets stay on `hubctl secret backup`,
never in the general archive. `down` never deletes homes; `purge` needs
`--confirm`.

Scheduled backups (issue #200): the supervisor captures a contextlife
archive per space every `HUB_BACKUP_EVERY` (default 24h; `0`/`off`
disables) into `<space>/archive/backups/`, retaining the newest
`HUB_BACKUP_KEEP` (default 7) archives — the same redaction contract as
the manual command (secret envs and `.enc` stores never enter the zip).
Every `HUB_BACKUP_DRILL_EVERY` (default 7d; `0`/`off` disables) the
newest archive is restored into a scratch dir under the supervisor state
dir and every manifest hash re-verified — the auditable result lands in
`<state>/backups.json` and on `hubctl status` as
`backup <user> last=<ts> retained=<n> drill=<ok|class> checked=<n>`.
`hubctl context restore-drill --dir <space> [--in <archive>]` runs the
same drill on demand; a cross-owner restore stays denied at Verify.

A `config.supervision` change needs no respawn: `space apply` re-renders
`runtime.<env>.policy.json` and the supervisor picks it up on its next
decision sweep — `idle_stop` bounds warm retention, `restart: never` or
`watchers.stack-health.protected` keep the container observed but never
remediated, `watchers.stack-health.every` throttles the health-probe
cadence. Port and channel changes (`config.ports`, `config.channels`) are
recreate-class: only the owning container restarts — `hermes-runtime` is
supervised, so the apply asks the supervisor's `/v1/respawn` to destroy and
re-spawn it (env-baked config needs a new container, unlike a deferred
restart's remount); the respawn reads the current published policy version
before spawning, so a rotated policy does not reuse the stale runtime binding.
A shared gateway such as `communication-hub` recreates
in the infra space's project; a port leaf whose owner the revision doesn't
declare is dead config and plans no recreate. From the operator host the
supervisor is in-network-only — set `HUB_RUNTIME_SUPERVISOR_URL` to a
reachable endpoint or the respawn cannot be signaled.
During `space apply`, the operator sends the exact apply revision with the
respawn request. The supervisor accepts it only while the matching journal is
in its effects phase and the published pointer names the previous or new
revision. This lets the apply finish while the journal correctly blocks
ordinary ToolHub decisions; other runtime restart requests still use the
`agents.restart_self` decision. Keep the endpoint on the control network or
expose it only temporarily on local loopback for host-side apply.
If an interrupted effects phase included an image rebuild, verify the rebuilt
image tag and running containers first. An operator may then rerun
`space apply --confirm --recover-rebuilt-image` to restore the prior revision,
replay service effects, and retry publication. Without that flag, recovery
remains blocked for an uncertain image build.

Memory: native files under `spaces/<id>/hermes/memories` (`MEMORY.md`,
`USER.md`) — `hubctl memory list` / `hubctl memory delete --name`. The vault
(`spaces/<id>/vault`) adds three verbs: `hubctl memory import` parses the
native files into reviewable draft cards staged under
`vault/runtime/memory/import` (directive candidates are review-only — nothing
reaches `memory/CORE.md` automatically); only `import --apply` commits them
through the governed writer, and re-import never clobbers an existing card.
`hubctl memory eval --questions <set.json> [--out-dir .work/tmp]` runs the
live question set through the model relay (`HUB_MODEL_RELAY_URL`/
`HUB_MODEL_RELAY_AUTH`) with top-4 vault evidence as context and writes a
timestamped judged report — relay failures are recorded per question, mocks
never count as live. `hubctl memory vacuum` dry-runs the superseded purge
set; only `vacuum --apply` deletes, and git history keeps everything
recoverable. `hubctl memory prune` dry-runs the leaf lifecycle report
(fold/expire/keep predictions); only `prune --apply` executes, and every
action journals to `memory/REVIEW.md` plus the localized morning digest.

Daily leaves are bounded by two opt-in agent settings, both off by
default: `memory_leaf_fold_bytes` (>0) folds a *completed* oversized
leaf at the next ToolHub night tick — audit-kind block bodies (routine, system,
probe, compaction, untrusted, background replies) collapse to stub
lines while mem:id headers and interactive owner/agent blocks stay
byte-identical; oversized eligible days first get a `long_summary` from
the same rollup call so detail lands in `summaries/daily/`.
`memory_leaf_retention_days` (≥2) expires a leaf older than the window —
`git rm` + commit + prov.db row drop — but only after the day's rollup
state is complete and its daily summary exists; unverified days are
kept and reported, never deleted. Expired bytes still live in the
vault's git history — true erasure needs a history rewrite, not this
knob.

The ToolHub night clock reads each owner's published `memory.nightly_rollup`
and `runtime.timezone` (default 04:00 UTC). Its protected occurrence and
day receipt live under that owner's automation `system/dreaming/` subtree;
vault checkpoints live under `vault/runtime/memory/night/`. A disabled memory
passport or missing `memory.dreaming` right prevents a model call and vault
commit. The published revision must match the active capability profile;
an incomplete apply denies admission and each subsequent commit guard.
Failed work stays pending; a communication outage leaves the digest
in ToolHub's delivery outbox. `model-relay-caller.auth` is a rendered,
caller-only file mounted read-only for the matching owner; never inspect or
copy its contents into diagnostics. Rendering gives the service group read access
(0640); ToolHub joins the host file group so nightly work can read the mount.

An external memory backend rides the hub-owned provider seam, never the
upstream plugin path: select `memory_provider: {via: native}` in the
core/extended toolsets of `passports/tools.yaml`, behind the separate
`native:memory_provider` governance grant. Declare
`memory.provider: {adapter: hindsight, url: <absolute http(s)>, token_env: <name>}`
in `passports/config.yaml`; token_env names a secret variable, never its value.
The render writes `vault/runtime/memory/provider.json`; in
managed capability mode the endpoint host joins the egress allowlist, and
disabling the capability removes both. `hubctl memory replay` streams the
`prov.db` audit spine into the provider — each entry's bank id minted from
`runtime/memory/scope.key` (hub-generated, never in the space) over
env×principal×audience — checkpointed per entry at `runtime/memory/
replay.cursor` so an interrupted migration resumes and a re-run captures
nothing twice. `hubctl memory export-honcho --file <export.json>` converts
a Honcho conclusions export (the list Page or the piped array) into
`source: migrated` draft cards in the import staging lane — review, then
`--apply` commits. If the provider is down the vault is unaffected; the
adapter reports degradation honestly (capture/search/export and health are
the only lanes — recall and governed write are `unsupported`).

With `knowledge_ui` on (requires `memory_vault`), one pinned `open-knowledge`
container serves the OpenKnowledge editor over the vault — the space-private
network only by default, no host reachability at all. On a published
passport the stack doc must also declare `open-knowledge: {mode:
per-space}`; a declaration without the feature fails the
declared-but-unrendered check (the vault itself, `.ok/` state and history
are never deleted). Setting `ui_port` in `agent.yaml` opts into exactly one
host publish, `127.0.0.1:<ui_port>` → the container's 8080 (the editor is
unauthenticated, so loopback is the only safe edge). Reach it from another
machine by tunneling in: `ssh -L <ui_port>:localhost:<ui_port> <host>`, then
open `http://localhost:<ui_port>` in a browser. `ui_bind` pins the publish
address and is loopback-only (`127.0.0.1` or `::1`); a wider bind is a hard
validation error, never a warning — there is no public exposure path.
`ui_port` joins the other host ports in validation (1024–65535, no collision
with browser/oauth/slack events ports) and requires `knowledge_ui`;
`hubctl doctor` reports the edge, the tunnel hint and whether something is
already listening. `ui_expose` is the reserved M18 hook: setting it fails
honestly until the M18 edge service lands, which is where a shareable
one-time `?t=` UI link (auth + expiry decided there) would hang off — never
a silent partial exposure.
Coexistence contract with hub writes: when the governed writer or rollup
lands a commit while a document is open in the editor, OpenKnowledge keeps
both versions — a stale save is *rejected* and retained under
`.ok/local/stale-external-writes.json`, and the conflict surfaces via
`.ok/local/conflicts.json` (shadow versions live under `.git/ok/`). There is
never a silent overwrite in either direction. Owner UI edits stay as
working-tree changes until the next `vault.CommitExternalEdits` sweep (the
nightly rollup path) commits them as `hermes-owner` and wins the next
rollup (`truth_pending` flow). OpenKnowledge's own state (`.ok/`,
`.graph/`, `.git/ok/`) and every `runtime/` sidecar are `.okignore`d, so the
editor indexes knowledge only. If the container is down, vault capture,
recall and search are unaffected — nothing else depends on it.
The editor is a human surface only: no `mcp_servers.open_knowledge` entry
renders on either render path, so the agent never dials it (M32/#520 route
stays retired). Agent knowledge reads and edits go through the governed
ToolHub tools — `memory_search` reads and `memory_card op=propose` plus the
owner's verified confirmation is the only agent edit path;
`knowledge_ui_write` stays deferred (#422). The separate `memory_open` agent
tool still opens the read-only ToolHub memory view (one-hour bearer link,
rights rechecked per request). Honcho is
optional (`honcho` + `honcho_url`); its failure must not break ordinary
Hermes memory. Skills live in the owner store `spaces/<id>/skills` —
publication, archive, restore, rollback and promote rebuild the read-only
effective view (`generated/skills-effective`) in place, so a running runtime
sees the new catalog on its next call without respawn or manual render;
`hubctl skill install --consent` scans SKILL.md and records findings —
MVP advisory mode: the approval gate is the named approver, not the
scanner, until rule quality justifies a publish-time block.

## Backup

Stop services; back up all `spaces/<id>` homes + `communication-hub-data`
volume to encrypted storage. Ciphertext backups via `hubctl secret backup`
must not include the key. Never `docker compose down --volumes` unless
deleting data intentionally. Restore org and user homes separately; never
merge memories, Telegram sessions or provider credentials. Back up the
spool's `event-state/`, `run-events/`, `provider-dedup/`, cursor initialization
markers and `event-layout.json`/`events` format fence with jobs/mappings/outbox — receipts repair incomplete
handoff on restart.

## Images and builds

Four role images share the staged Dockerfile (SPEC-0082):
`hermes-hub:0.5.0-<env>-core` (control-plane daemons: cliproxy,
communication-hub, supervisor, relays), `…-control` (adds Docker CLI +
ToolHive; only toolhub + workload-controller), `hermes-hub:0.5.0-<env>`
(the agent runtime — Python venv + node, no browser or voice stack), and
`hermes-hub:0.5.0-<env>-browser` (per-space sidecar: headed Chromium under
Xvfb, authenticated x11vnc, static noVNC, playwright-mcp, google_meet
NodeServer). Optional trees (messaging/google extras, Google/Atlassian/
Telegram-account Python, voice STT/TTS) stay out of the role images — own
Dockerfiles under `docker/`. Generated compose carries one `build:` per
role (cliproxy/core, toolhub/control, hermes-runtime, browser-<user>);
everything else references those tags — raw `docker compose build` would
repeat the Dockerfile per service. CI's `image` job publishes all four
role tags and fails the build when `devcheck image-budget` finds a role
over its SPEC-0082 size ceiling or carrying a forbidden artifact.

The browser sidecar is a warm resource, not resident infrastructure
(SPEC-0083): capped at `cpus 2.0 / mem_limit 3g / pids_limit 256` (its 1 GB
shm counts against the memory limit), it sleeps once its space has no live
runtime — spawned or a compose `hermes-runtime` — for
`hub-supervisor -browser-idle-ttl` (default 15m). `docker stop` is a
graceful SIGTERM so Chromium flushes `connections/browser`; `unless-stopped`
honours the stop and the next spawn's `docker start` (issued by `Ensure` for
browser/meet spaces before readiness) revives it transparently — profile,
cookies and logins survive. `hubctl up` re-raises it as usual. Expected
noise: the runtime's container healthcheck probes CDP whenever the browser
marker is set, so the runtime shows `unhealthy` while its sidecar is
asleep or restarting — transient, clears on wake. Managed runtimes get
their `browser`/`browser_guest` MCP servers rendered directly into the
managed config from the reviewed `browser`/`browser_act` capability
(same `hub:browser`/`hub:browser_guest` governance gate, no connector
or catalog round-trip); `HUB_MANAGED_BROWSER` carries the grant to
in-container materialization so the attested config matches.
Managed `browser_open` creates its own Playwright tab in the chosen profile;
`browser_act action=close` closes that tab. `action=wait` uses `value` as
visible text or, when numeric, seconds in the interval (0, 30]. Captures
return a path under the shared workspace's `browser/` directory; the named
file must exist there before treating a capture as delivered.
Both Playwright MCP profiles allow 15 seconds for a browser action; the
upstream 5-second default timed out on screenshot rendering and footer
stability for a live page. A failed capture still needs a fresh call and a
verified file before it is reported as complete.

Entry points (`just`, `hubctl build`/`up`) force `DOCKER_BUILDKIT=1`,
`COMPOSE_DOCKER_CLI_BUILD=1`, `COMPOSE_BAKE=true`; full `hubctl` builds prune
retag-orphans before/after and cap builder cache at 8 GB. Scoped builds and
`dev-rebuild` retain the shared cache; run `just docker-clean` deliberately
when disk use needs reclaiming.
`HUB_DOCKER_CACHE_FROM`/`_TO` forward `--cache-from/--cache-to` (CI uses
`type=gha`); builds always `--load`.

`dev-rebuild` (dev only, service must already run):

The supervisor starts isolated Starlark helpers from the core image, which
contains `communication-hub`; the agent runtime image has no daemon helper.
`HUB_SCRIPT_IMAGE` selects an explicit core image for a pinned deployment.
`go run ./cmd/hubctl dev-rebuild --dir spaces/alice --env dev <service>` —
compiles one Linux binary, overlays the dev image, recreates only that
service, and copies the active immutable platform catalog into the overlay;
restores the prior image on failure. Names: `communication-hub`,
`credential-broker`, `hub-media`, `toolhub`, `workload-controller`,
`supervisor`, `hermes-runtime`, `model-relay` (supervisor mode records it for next spawn;
a full `hubctl up` returns to base). Verify with `docker ps --filter
name=hermes-hub-<user>-dev-<service>-1` → `…-dev-<service>:local` + `Up`.

Cleanup: `just docker-clean` drops stale `hermes-hub:*` tags unreferenced by
`spaces/*/compose*.yaml`, stopped `hermes-*` containers pinning dangling
images, orphan `hermes-build-*` objects. The supervisor's matching runtime
image is retained while a core/control Compose image references that release;
`--deep` also drops `hermes-build-state-shared` — run only with no artifact
build in flight.
Windows disk reclaim: `scripts/reclaim-docker-disk.cmd` (admin; stops Docker
Desktop/WSL, Optimize-VHD).

CI: `image` job runs `devcheck docker-publish <target>` → immutable
`ghcr.io/<owner>/<repo>/hermes-hub:<sha>-<target>` + moving `edge-<target>`;
`container-gates` runs independent contract scenarios; `containers` aggregates
them. Local `just docker-check` uses prebuilt images: `docker-pull` requires an
exact HEAD tag and matching revision label, failing before retag on mismatch. Local
pull needs one-time `docker login ghcr.io`. Weekly `prune-images` keeps
newest 8 versions + `edge-*`. `HUB_REGISTRY_REPO` overrides for mirrors.

## Supervisor and scale-to-zero

ADR-0014/SPEC-0012. The supervisor starts the selected context runtime per
accepted job, reuses within the warm window (`--warm-ttl`, default 30 min),
stops compute when leases end. Enable via `HUB_RUNTIME_SUPERVISOR_URL`;
native static per-user Compose remains the explicit alternative and the
v0.2.1 artifact the drain/stop/audit rollback (v0.3.0 removed the one-shot
executor — see SPEC-0016/CHG-0018).

In supervisor mode the runtime joins the space's project network and mounts
its `broker-secrets-runtime` volume read-only. Footprint (dev host sample,
2026-10-07): always-on ≈160 MiB per stack + ~410 MiB per warm runtime;
cold start ~60–80 s, warm lease ~0.6 s; `--max-runtimes` (16) is the
concurrency bound; crash-loop denial after 3 crashes/10 min window.
Re-run the protocol on the target host before fixing SLOs — CHG-0100.

Run the supervisor under a service manager, never a bare shell — it is the
lease authority for in-flight jobs. Listens `127.0.0.1:8876` by default;
wildcard/empty listen refused unless opted in (`-allow-wildcard-listen` /
`--supervisor-allow-wildcard-listen` / task installer flag) — required only
when containers reach the host via `host.docker.internal`, and then only
behind a firewall rule restricted to the Docker/WSL subnet: this endpoint
controls runtime lifecycle. Token via `-auth-file`/`HUB_SUPERVISOR_AUTH_FILE`
(owner-only regular file; symlinks/empty/world-readable rejected); `-auth`
argv still works but is deprecated — argv is world-visible.

systemd unit and Windows task examples are in the repo (`scripts/
install-supervisor-task.ps1` registers an at-logon task whose Go `-watch`
parent restarts a crashed child). Give each Hub installation its own
supervisor port — a foreign listener answers health checks but rejects the
bearer (401) while every job fails.

Runtime budgets, one per hop sharing a single job clock: `HUB_RUN_BUDGET`
(30 m) total, `HUB_STREAM_IDLE` (60 s) per-hop silence with ≤15 s
transport-only `run.heartbeat` frames, `HUB_COLD_START_BUDGET` (4 m)
submission retry while supervisor boots (1 s→20 s backoff, only provably
unadmitted requests retry), `HUB_SUPERVISOR_JOB_TIMEOUT` (130 s) non-stream
calls, `HUB_RUN_STALL_TIMEOUT` (15 m) in-runtime stall → confirmed stop.

Job phases: `queued → waking → running → waiting_for_approval →
uncertain → completed|failed|cancelled|interrupted` + `unconfirmed` flag.
Telegram sends one editable `queued` status with an elapsed timer as soon as
the durable spool accepts the update, before supervisor admission. A missing
runtime run ID must not suppress that status. Rights decisions use a bounded
20-second ToolHub client timeout and still deny on timeout.
The reconciler re-observes unconfirmed jobs ≥30 s (same run identity, or
`/v1/readmit` with the atomic `/state/admissions/` receipt — exact input +
session + idempotency key; receipts <23 h only). A job unsettled past its
run budget fails `run.timeout`. Malformed gateway updates quarantine to
`quarantine/<update_id>.json` (no content, ≤1000 records). Inventory: ≤256
owner-labelled containers; old generations removable only with verified
terminal ledgers + immutable ID + `/scope` bind match; unknown/uncertain
block cold starts and are never auto-cleaned. `POST /v1/pins` (private
token) pins a context available (operator requirement, unmigrated native
cron); disabling a pin does not cancel jobs. `POST /v1/restart-request`
applies refreshed config after the active lease, never interrupts.

Canary: `HUB_CANARY_INTERVAL` runs a periodic end-to-end mock-conversation
probe requiring exactly one `file_list` receipt + delivered reply; journal
`canary.jsonl` (1000 lines), one notice per new failure class + one recovery.

Spool retention: hourly compaction past `HUB_SPOOL_RETENTION` (default 72 h) —
live work, open approvals and in-flight receipts never removed. `occurrences`
archive holds 48 h detail + dedup receipt for the retention period; old
orphaned probe/digest/script receipts are cleaned after retention. Catch-up
≤1 h, ≤16 occurrences per tick; malformed → `occurrences/quarantine`
(investigate before clearing). `HUB_MOCK_DIR` keeps mock/e2e capture out of
the production spool. Never delete uncertain mappings/leases to force a
retry — the original run may already have acted externally.

Concurrency: in-flight serialization is per conversation lane
`(principal, context, conversation)` — forum topics and DM tasks of one
user run in parallel in up to five managed runtime containers, FIFO inside
each conversation (ADR-0090, amends ADR-0025). Each container has an exact
MCP job/run identity and shares Hermes session history with its owner.
`HUB_CONTEXT_MAX_PARALLEL` defaults to five and cannot raise the ceiling;
`HUB_COMMUNICATION_WORKERS` (32) bounds the pool; fair rotation remains
per scope. A sixth session waits. Shared workspace writes can conflict,
and the browser sidecar remains one per space. A lane restart affects its
own active run.

Gateway/session ops: `/new`, `/use`, `/delete`, `/sessions`, `/session`
manage per-task sessions (each task = own durable Hermes session; forum
topics supported; slash-command list enforced + registered as bot menu).
`/usage [session]` reads the journal/quota; session mode reads the pinned
Hermes session API and renders `unknown` where upstream cannot report.
Long Telegram turns queue via `display.busy_input_mode: queue`; heartbeat
`HERMES_AGENT_NOTIFY_INTERVAL=60s`; `/stop`, `/cancel` are explicit. The
gateway queues one editable status immediately on acceptance and updates its
elapsed time while the runtime is waiting to start; typing resumes only after
run admission. Out-of-process ToolHub rights checks retain fail-closed behavior
and allow up to 20 s for a decision under host load.
Replies >4096 chars arrive as `response.txt` (≤2 MiB). Lost send
acknowledgement = uncertain — inspect outbox, never blind-retry.
Exception: connector-prepare notices are the one bounded auto-retry — the
delivery loop sweeps `outbox/failed` every 15 s and requeues them with
NotBefore backoff (30 s → 2 m → 10 m), at most 4 sends and only while the
record is fresher than one hour. A newer prepare version supersedes the
record instead; an exhausted or aged record stays `failed` for audit, and a
requeue logs `gateway prepare requeue deliveries=N`.
Published stack identity/scope-kind bindings are immutable; Telegram links are provisioned only in
trusted host config after verifying the numeric account ID — never link
from username/email/message-request.

## Per-context execution rollout

See [runtime operations](runtime-operations.md#per-context-execution-rollout).

## Skill git installs

`hubctl skill install --dir spaces/<u> --url <https github/gitlab url>`
fetches through the controller's bounded `/cli-exec` cell (plan
`skillgit@1.0.0`, `git` on the cli-tools image — never an owner command;
egress is exactly the repository host, hooks/protocol.file/submodules/LFS
are off, fetch is `--depth=1 --filter=blob:none` into the per-owner bare
mirror `skills/.sources/<id>.git`). The shared workload-controller
authenticates only the infra-owner's admission token: `--dir` pointing at
a secondary space resolves the sibling that renders the control plane
(`infra:` absent/true) and presents its `toolhub.auth` — the target
space's own token is never accepted. The result is a scanned **draft** —
approve it with `hubctl skill approve --id <d>` or the owner card. A repo
URL without `/tree/<ref>/<path>` returns `candidates` instead of picking
one; retry with `--path`. `--ref` pins a branch/tag to its resolved commit,
`--name` overrides the draft name. `hubctl skill update --name <s>` (or
`--all`) refetches tracked skills into new drafts, reports
`force_push:true` on rewritten upstream history and `up_to_date:true`
without a draft when the tracked ref did not move. `hubctl skill sources
list` shows mirrors and the skills each feeds; `sources remove --id <id>`
deletes the mirror — installed skills keep working but lose update
tracking.

Sources are https GitHub/GitLab only. The current passport schema has no
self-hosted GitLab host field; retired `workspace.yaml` cannot enable that
override. Do not patch generated environment files to bypass admission.
Registry locators also
work: `https://www.skills.sh/<owner>/<repo>[/<skill>]` maps to the GitHub
repository and resolves `<skill>` against the discovered candidates — an
unmatched slug returns the candidate list instead of guessing.
The export itself is a bounded transport (32 MiB archive cap): repo
plumbing and vendored `node_modules` are filtered while streaming and
named in `dropped_dot_paths`/draft notes, so a vendored repo never exhausts
the skill-tree byte quota; an archive over the transport bound fails with
a quota error, not a bare `invalid skill source`.
Authentication: agents name a broker credential with `grant` (the gateway
leases it into `GIT_TOKEN` for the cell); the operator path takes
`GIT_TOKEN` from the environment. Tokens never enter URLs, argv, git
config, logs or `.origin.json`.

## Skill local installs

`hubctl skill install --dir spaces/<u> --from <path>` imports a package
already on disk — a directory containing `SKILL.md`, a `.zip` archive or a
standalone markdown/`SKILL.md` file — through the same imported-draft
pipeline the agent's `install_local` uses. The name comes from the
`SKILL.md` frontmatter (`--name` overrides; path basename is the
fallback). Without `--consent` the result is a draft the owner approves
with `skill approve --id`; `--consent` publishes immediately.

Imports are *foreign* packages, not authored trees: dot-prefixed paths
(`.github`, `.claude`, `.gitignore`, a spoofed `.origin.json`) are dropped
and summarized in draft `import_notes`; resource-file links that resolve
inside the package are admitted and audited in `notes` instead of
rejected — dead links out of `SKILL.md` itself stay hard errors. Zip
extraction is bounded (entry count/bytes, path traversal and special
files refused); binary payloads like images in `assets/` survive intact.
Inside a runtime the agent reaches the same path with
`skill_edit action=install_local` and a workspace-relative `path` —
inbox downloads staged by `inbox_fetch`, unpacked archives, checkouts.

## Skill quality: lint, stats, evals

Every draft carries advisory lint findings in `ScanFindings` (name/dir,
imperative description ≤1024 with ~250 advisory, ≤500-line body, ≤60
files, references one level deep, no incident-log shape) — they inform
the approval card but never block it; dead links stay hard errors.

`hubctl skill stats --dir spaces/<u> --name <s>` prints the hub-journaled
counters (uses, helped, failed, trigger_misses, last_used) from
`.state/usage/` — the agent cannot write these events; activations and
resource reads journal server-side.

`hubctl skill eval --dir spaces/<u> --name <s>` reads `evals/triggers.json`
and `evals/evals.json` inside the skill — `--id <draft>` evaluates the
unpublished `.drafts/<id>` tree instead, the pre-publish check the approval
gate exists for. It materializes an isolated temp
space containing only that skill, and scores validation accuracy through
`HUB_SKILL_EVAL_URL` — an operator eval endpoint that runs each prompt
against a real model and returns the run receipt (`skill` tool calls mark
activation). Reports land under `.work/tmp/`; without the endpoint every
leg reports `model_unavailable` — never a fabricated pass. Platform
skills ship their trigger suites under `config/skills/*/evals/` and
`internal/systemguide/evals/`; lint-cleanliness and fixture validity are
enforced by tests.

## SSH capability operations

Select the SSH toolset in `passports/tools.yaml` and grant its requested
operations in `passports/grants.yaml`; selection alone grants no access.
Config lives in `connections/ssh/config.yaml` mounted read-only
at `/state/ssh` — render fails when missing/invalid; rotate by editing +
`hubctl up`. Host keys are mandatory, pinned per alias (full public key
preferred — a bare `sha256:` fp needs one pin per offered key type).
`file:` key refs stay in `connections/ssh/` (≤64 KiB); `broker:` refs use
the `ssh-key` contract with a per-dial lease under `ssh-<alias>` (needs both
`broker:control` + `broker:runtime` audiences). Command/path allowlists are
anchored, `*` the only wildcard; `sudo: passwordless` runs `sudo -n` only
when the inner command matches. Shells/tunnels live in-runtime — restart
closes them; tunnel listeners are loopback-only, never published. Durable audit
admission/reserve precedes effects; bounded receipts contain counts/IDs, never
paths, output or key material. Uncertain outcomes require reconciliation before
repeating; close still attempts access termination when audit fails.
Successful shell/tunnel opens retain a scoped cleanup audit reserve until their
verified closure; the hold survives other writers and journal rotation.
Revoking a grant or deleting a host
takes effect at the next dial. Bare-metal smoke: `hubctl tools` serves the
same MCP surface over stdio against a real sshd
(`HUB_USER_ID`, `HUB_STATE`, `HUB_SSH_CONFIG`, `HUB_SSH_*` feature vars) —
`file:` refs only; `broker:` needs a running broker.

## Media credentials (hub-media)

Provider keys never reach a space env file: hub-media materializes the
broker grant named by `HUB_MEDIA_BROKER_GRANT` per call (5-min cache; broker
outage fails closed). Provisioning: seed `media.private`/`server.crt` into
`broker-secrets-media` (0600/uid 10001), register the public key with
audiences `broker:control`+`broker:runtime`, install the
`media-provider-key` contract, create credential + grant bound to workload
`credentialbroker.MediaBinding(<infra-owner>)` for both binding and workload
(execution `dedicated`, policy_version `hub-media` — stable
service-level identity), write the grant id to `spaces/<u>/media.grant`,
re-render. Fallback for standalone runs: `HUB_MEDIA_UPSTREAM_KEY`/
`OPENAI_API_KEY`/`FAL_KEY` env; a fal engine with neither refuses to boot.

Operational facts proven by live acceptance:

- Ten managed spaces on one infra (`m29-acc-01..10`, each with a dedicated
  cliproxy + model-relay) project distinct bounded tool surfaces and answer
  real provider calls independently: 10/10 `chat/completions` 200,
  8 distinct live MCP core projections from 10 passports, one 43-selection
  profile held to a 20-tool direct core, a non-admitted call denied as
  `unknown tool`, and unauthenticated MCP requests 401. Publishing the
  capability records requires policy → profile → `agent-tools` → sibling
  token enrollment (docs/services/toolhub.md); a profile pinning an older
  `hub-agent-tools` implementation digest than the bound version resolves
  to zero admitted tools, and a ToolHub image older than the profile schema
  (`core_tools`) fails its store load outright — rebuild via
  `dev-rebuild toolhub` before judging profile data.

- `media.grant` resolves from the infra owner for shared hub-media, including
  secondary-space apply/rebuild. It reaches the container through `hubctl`,
  which feeds the file into compose interpolation (`${HUB_MEDIA_BROKER_GRANT}`).
  A raw `docker compose up` renders the variable empty: hub-media then runs
  the `remote` engine with no credential, sends no `Authorization`, and the
  upstream answers 401 — surfaced to callers only as
  `"image generation failed"`. Same applies to `supervisor.auth` →
  `HUB_SUPERVISOR_AUTH`: a compose call without it fails interpolation
  outright. Always bring rendered stacks up through `hubctl up`, or export
  both variables explicitly. Verify post-recreate:
  `docker inspect` on the service shows the grant in `.Config.Env`.
- The env-key fallback is unreachable in a rendered stack: compose mounts no
  media env file and injects no `HUB_MEDIA_UPSTREAM_KEY`/`OPENAI_API_KEY` —
  without a grant the service simply has no upstream credential. There is no
  silent "works anyway" mode; treat upstream-401 symptoms as a missing grant
  first, a provider problem second.
- Gemini-family image models (`gemini-*-image`, e.g.
  `gemini-3.1-flash-image`) do not serve `/v1/images/generations`; cliproxy
  routes them through `/v1/chat/completions` with
  `modalities: ["image","text"]`. hub-media takes that path automatically
  for ids listed in `HUB_MEDIA_CHAT_MODELS` (the default list covers the
  cliproxy Gemini image ids) and parses `choices[0].message.images[]`. The
  plain `/v1/images/generations` contract is reserved for `gpt-image-*`,
  `grok-imagine-*` and openai-compatibility image models.
- Image generation deliberately runs on the **shared** cliproxy behind the
  broker-grant key — not the owner's dedicated cliproxy. Owner isolation for
  media is the grant+policy binding, not per-owner proxy auth.

## Additional managed owners (multi-principal bring-up)

Adding or renaming a managed principal touches more files than one
`hubctl init`: the pieces below were each proven load-bearing on a live
two-owner dev stack (renames `telegram-*` → named principals). Skipping any
of them fails silently — empty tool lists, `model_not_found`, 401s — rather
than loudly.

Files and records per owner:

- `spaces/<owner>/passports/stack.yaml` — `identity.user: <owner>` fixes the
  principal rendered everywhere. `passports/tools.yaml` binds its profile and
  generation; issued policy/rights remain protected records.
- `passports/models.yaml` — default/routes use logical names (`default`,
  `alt`); provider IDs belong in each service's models list. Each provider
  model must exist in **that owner's** catalog. Publish authored edits with
  `space apply`; the relay reloads the resulting generated registry. A
  dedicated `vision` route is used by managed media when published. If a
  models edit recreates `hermes-runtime`, supervisor authorizes that exact
  target from the apply journal and respawns it; unrelated targets remain
  denied.
- `spaces/local/communication.users.yaml` — hand-edited: `id`, `runtime_id`,
  `telegram_ids`, `policy_version`. The policy version must equal the
  rendered stack policy (`policy-<hash>` from the space's generated files);
  a stale value produces a silently empty `tools/list` — the runtime passes
  health checks and the agent sees zero tools.
- `communication.<env>.env` — `TELEGRAM_ALLOWED_USERS` must include every
  owner's numeric Telegram id; an absent id is rejected at bot level before
  any users.yaml routing.
- `spaces/<owner>/cliproxy/` (dedicated mode) — own `config.yaml`, own
  `api-key`, own `auths/` dir. Never copy another owner's keys or upstream
  auth material: the point of dedicated mode is independent accounts.
- `secrets.<env>.env`, `runtime.auth`, `model-relay.auth`,
  `media.auth` — per-owner bearers; `model-relay.auth` carries two distinct
  tokens: `HUB_MODEL_RELAY_AUTH` (runtime→relay chat/quota) and
  `HUB_MODEL_RELAY_ADMIN` (the owner-facing `PUT /v1/model` switch — the
  chat bearer gets 403 on it by design).
- Capability store — after a rename/re-render the stack policy id changes;
  `DeriveSpaceRights`/`space apply` reconfirms the profile and repins
  credential-free bindings onto the new `policy_version` in the same pass.
  Credential-bearing connector bindings stay an explicit step: re-create
  them via `hubctl capability … --confirm`. Stop toolhub during store
  writes: a running toolhub rewrites `store.json` between the CLI's load
  and save, and the write fails on drift.
- Infra re-render — re-render the infra space (`spaces/local`) after adding
  or removing a principal: `EnrollSiblingRuntimeTokens` rebuilds
  `toolhub-tokens.json` from the current sibling spaces, revoking stale
  bearers; comm-hub only accepts enrolled runtime tokens.
- Broker key volume — a freshly created owner stack gets an empty
  `broker-secrets-*` volume; the relay exits with
  `credential broker CA file is invalid` until `server.crt` and the key
  material (0600, uid 10001) are provisioned — same procedure as
  `broker-secrets-media` above.
- Compose interpolation — `hubctl up` additionally injects
  `supervisor.auth`/`media.grant` into compose env; see Media credentials
  for why raw `docker compose` breaks the stack.

Verification sequence (each step proved a real failure mode):

```text
# inside any runtime-network container:
getent hosts cliproxy cliproxy-<owner>        # cliproxy → exactly the infra
                                            # IP; dedicated cliproxy must
                                            # never claim the shared alias
curl -s -o /dev/null -w '%{http_code}' \
    http://cliproxy-<owner>:8317/v1/models    # 401 unauthenticated
# own cliproxy key → 200; sibling's key → 401
curl -s -H "Authorization: Bearer <relay-auth>" \
    http://model-relay-<owner>:8318/v1/models # own bearer → 200 catalog
# sibling relay bearer → 403; sibling admin on this relay → forbidden
PUT /v1/model {"model":"alt"} with the admin bearer
POST /v1/chat/completions → response's "model" shows the new ladder's
    upstream name; PUT back to default afterwards
POST /v1/mock/message (comm-hub, principal header) → outbox delivery
    proves wake→spawn→admission→reply end-to-end
```

Known traps from live acceptance:

- **Dedicated cliproxy must not claim `cliproxy` on the shared runtime
  network.** A service literally named `cliproxy` gets the implicit alias
  and DNS round-robins owner traffic to the wrong instance (random 401s).
  Owner-scoped names/aliases only; verify with `getent` as above.
- **Stale container images mask everything.** `dev-rebuild` the service
  before trusting evidence — a pre-change `hermes-hub:0.5.0-dev` supervisor,
  toolhub, relay or hub-media silently shows old behavior. Check
  `docker inspect` shows a `…:local` overlay image under `.Config.Image`.
- **Model relay timeouts** — defaults are five minutes per hop and six
  minutes total. Check operator overrides of `HUB_MODEL_RELAY_HOP_TIMEOUT`
  and `HUB_MODEL_RELAY_TIMEOUT` before attributing a timeout to the provider.
- **ToolHub restart interrupts MCP sessions** — verify calls after a
  recreate; respawn an affected runtime if reconnection fails. Catalog/contract
  changes still require rebind and managed respawn.
- **Renamed/migrated spaces can carry corrupt SQLite state** — a malformed
  `managed/<env>/hermes/state.db` lets the runtime boot and answer
  readiness while every job stalls `uncertain`. Check
  `PRAGMA integrity_check` on each `managed/<env>/hermes/*.db` after a
  rename or host migration.
- **ToolHub SQLite on Docker Desktop** — keep the stack's space directories on
  the Ubuntu filesystem, not under `/mnt/c` or a Windows `C:` bind. Confirm
  the actual `/state` mount with `docker inspect`; the container being Linux
  does not prove its database is on a Linux filesystem. To move an existing
  stack, stop both owner and managed-space Compose projects without deleting
  volumes, copy their space directories with numeric uid/gid preserved, and
  include `store.db` (plus `-journal`/`-wal` leftovers, if any) from the
  stopped source. Check SQLite
  `PRAGMA integrity_check` on the destination before starting services. Keep
  the ToolHub store private to uid 10001 (`0600`); the rendered routing and
  non-secret configs must be readable by the service, while controller and
  hosting keys remain limited to the service group (`0640`). The rendered
  supervisor runtime env files and `runtime.auth` must also be group-readable
  (`0640`): the supervisor's Docker CLI reads them through `--env-file`.
  `hubctl render` makes the generated skill views writable by the supervisor's
  group while retaining their mount inode; the generated parent also needs
  group write for the effective config and policy snapshot. Docker socket
  access uses its actual host GID, passed to supervisor and Docker-using
  services at render. Render and start from the Ubuntu path, then check all
  service restarts and a real MCP call.
  Assignment, routine and watch tools call communication-hub, which calls
  ToolHub back for rights; dispatch those after releasing the registry's
  SQLite admission transaction and reverify rights before execution. Holding
  that transaction through the callback makes `assignments.list` wait for
  its own single SQLite connection until the HTTP timeout.
  The ToolHub store runs SQLite in rollback-journal mode, not WAL: a WAL's
  shared-memory index cannot propagate through a Docker Desktop bind mount,
  so a WAL connection's view froze at map time, a second connection failed
  with `SQLITE_IOERR_SHORT_READ`, and blind writes/checkpoints physically
  erased foreign frames — observed live when a host-side `space apply`
  profile write vanished within seconds. The rollback journal needs no
  shared index: every reader sees the authoritative file and file locks
  serialize host and container writers. ToolHub's unchanged-store check
  still starts with SQLite `data_version`, then compares a rate-limited
  content hash of `store.db` (of `-wal` when a WAL-era file still exists):
  `data_version` only sees commits this connection can observe, while the
  file hash is a signal a foreign writer cannot mask. A real change loads a
  snapshot made from byte copies of the store files — never a second live
  connection — and adopts it; a snapshot overtaken by a mid-load commit is
  refused with a conflict and converges on the next probe. Symmetrically,
  every save compares the current file hash and, on drift, arbitrates with
  the persisted digest read from a byte copy: a foreign commit refuses the
  save with a conflict so the next reload adopts it, while the store's own
  out-of-band writes re-baseline and proceed. The in-transaction digest
  check stays as the last fence — in rollback mode the connection's view is
  truthful again.
- **`/model` chat command is currently dead for managed principals** behind
  the shared comm-hub: `HUB_MODEL_RELAY_URL`/`model-relay.auth` are rendered
  only into a gateway inside its own managed space, and managed spaces run
  no gateway — so the command replies "недоступно". Model switching works
  through the relay admin `PUT /v1/model` (see verification above); a
  per-principal relay resolution in comm-hub is a known gap, not a secret
  feature to debug.

## Preflight and misc

For M15 model changes, run offline `hubctl doctor --dir spaces/<owner> --env
dev|prod` first. It prints effective model/endpoint/proxy scope and compares the
merged registry, referenced env credentials and proxy placement against rendered
files. The apply plan names only the owner relay/proxy or next owner runtime;
provider entries denied to that owner do not trigger credential restarts.
Values and Broker grant IDs are not printed. The comparison is against generated
configuration, not a claim about a live container's loaded state. Render each
affected space, then deliberately apply only the services listed by the plan.
The first upgrade to the directory-mounted registry also requires recreating
the owner's relay to adopt that mount. Do not broadly restart other owners.

Broker model identity provisioning and account-specific grants are described in
[model-relay](services/model-relay.md#broker-provisioning-operator-action).
Media per-engine grants and opt-in workflow storage are in
[media services](services/media.md). E2E-021 now has a server oracle for two
random workspace images, comparison output and whole-set rejection of an
unavailable third image. Running it with a real channel sends messages and
requires the owner's concrete instruction; fixture tests do not establish live
acceptance. A two-owner identity/read check likewise needs actual documented
provider identity evidence; do not invent a universal identity endpoint.

- `hubctl doctor --dir … --live` queries the provider `/v1/models` exactly —
  needs host reachability (internal DNS may not resolve from host); for
  cliproxy it uses the loopback-published port. Offline `doctor` never
  claims live provider reachability.
- `just config-check` renders synthetic dev/prod static+supervised profiles
  through `compose config`; part of `just check-full`/`just build`/image CI.
- The native container gate starts a real MCP stdio child against a local
  fixture — no Telegram contact. CI publishes `cli-tools`/`cell-proxy`
  stages; `just docker-check-prebuilt` pulls them.
- Never `hermes update` in prod — bump the reviewed pin and rebuild.
- `HUB_TOOLHUB_AUTOSTART=true` starts the shipped `hub-toolhub` binary.

## M16 runtime preparation and group scopes

See [runtime operations](runtime-operations.md#m16-runtime-preparation-and-group-scopes).

## Site hosting

Configure reviewed `hosting.slots` and exposure before render/doctor. Dynamic hosting requires an approved digest-pinned minimal image for the fixed go-http binary and explicit runsc/Kata; no runtime fallback. Preserve site and coordination volumes during recovery. See [hosting operations and recovery](services/hosting.md); native runtime/sish/browser checks are separate prerequisites.
