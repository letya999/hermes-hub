---
description: Operator runbook — stack lifecycle, user onboarding, diagnostics, secrets, links, broker ops, migrations, backups, image hygiene, supervisor, rollout, preflight.
last_verified: 2026-11-02
---

# Operations

The authorization model behind every command here is in docs/rbac.md; space
config vocabulary is in docs/spaces.md; per-service internals in
docs/services/. This file is procedures only.

## Stack lifecycle

`hubctl --user <id> --env dev|prod` (prod default; `--dir spaces/<id>` is the
explicit form). `render` writes generated files under `spaces/<user>/
generated/`; `up` builds and starts; `down` stops without deleting data;
`logs` tails the Compose project. Pass service names to scope:
`hubctl up --dir spaces/alice --env dev communication-hub toolhub` — scoped
`up` skips dependencies, list them explicitly. `hubctl build` then
`hubctl up --no-build` restarts on existing images.

`hubctl build`/`up` run `docker compose config --quiet` first; supervisor
mode also needs `HUB_SUPERVISOR_AUTH` or a provisioned `supervisor.auth`.
`up` rejects a second `communication-hub` on the same Telegram bot token and
a second cliproxy on the shared alias — stop the conflicting gateway
intentionally.

## User onboarding — the capability ceremony

A new space renders and boots, but **renders no tools until capability
records exist**: `workspace.yaml` wishes admit nothing, the shared ToolHub
store is the authority, and a managed runtime binds a profile published for
its exact `{principal, context, runtime, environment, generation}` tuple.
Skip the ceremony and the user gets an agent with an empty tool surface.

1. **Scaffold** — `hubctl init --user <id>` writes `spaces/<id>`:
   `agent.yaml` (operator-owned), `workspace.yaml` (capability wishes),
   `scope.yaml`, `secrets.{dev,prod}.env`, `SOUL.md`, state dirs. Init never
   overwrites; a fleet default lives in `policies/agent-default.yaml` /
   `policies/workspace-default.yaml` when present.
2. **agent.yaml** — set `model` and `model_url`. Managed mode requires
   `model_url: http://model-relay:8318/v1`, `capability_mode: managed`, the
   `capability_profile_id` you will publish below, and
   `capability_generation` (start at 1). Ports are allocated at init; pick
   free `oauth_port`/`browser_port` when they collide. Under managed mode
   only carve-out toolsets may go `via: native`, hooks/memory/native
   extensions are rejected, and raw MCP on a personal space is forbidden.
3. **workspace.yaml** — the wish list (`tools:` map). Keep the template:
   `file/documents/images/hh` via `toolhub`, `todo/memory/web/deep_research`
   native, `browser: mcp`. Wishes still admit nothing — step 5 does.
4. **Secrets** — fill `secrets.<env>.env` (`OPENAI_API_KEY` at minimum —
   the model relay authenticates upstream with it). Never commit or paste
   this file.
5. **Capability records** — publish into the infra space's protected store
   (`spaces/<infra>/runtime/toolhub/store.json`; the space whose agent.yaml
   has `infra: true` or unset):
   - ceiling policy: `hubctl capability --kind policy --file policy.json
     --toolhub-store <store> --confirm` (the `policy_id` the profile binds)
   - profile: review a draft first with `--kind preview --file profile.json
     --toolhub-store <store>` (read-only old→new diff), then
     `--kind profile --file profile.json --confirm`. The record's
     `principal_id`/`context_id`/`runtime_id`/`environment`/`generation`
     must equal the agent.yaml binding.
   - agent-tools surface: `hubctl capability --kind agent-tools --principal
     <id> --context <id> --runtime <id> --policy-version <v>
     --toolhub-store <store>` — registers the reviewed hub-tools catalog
     definition and enables it for the identity.
   Every record prints a review digest first and refuses without
   `--confirm`; the store re-checks issuer identity at admission.
6. **Channel routing** — for Telegram delivery add the user to the infra
   space's `communication.users.yaml` (`id`, `runtime_id`, `telegram_ids`,
   `features`, `policy_version`) — no entry, no routed jobs; an entry with
   empty `telegram_ids` still jobs but cannot notify.
7. **Bring up** — `hubctl doctor --user <id>`, then
   `hubctl up --user <id> --env dev|prod`. Up renders `generated/` (compose,
   `tool-policy`, `tool-manifest`, hermes config), mints `runtime.auth`,
   and enrolls sibling `toolhub-tokens.json`/`model-relay-admins.json`.
   The supervised runtime spawns on first demand (pin or job).
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
collector copies only lines naming that space's owner as a whole token.
Compose keeps Docker `local` rotation (10 MB × 3/container). Structured producers
allowlist metadata; collection and agent projection exclude bodies/unknown fields
before redaction and search. Source reads stream from oldest unread with timestamp
and tie ordinal, never last-5000 tail. Data sync precedes atomic cursor replacement;
replay keeps source_position. Source/cursor/rotation loss emits diagnostic_gap;
unreadable stopped containers are not marked drained. Opt out: `diagnostics: false` in the infra space's
`agent.yaml`, then `hubctl up`.

Rotation requires the dedicated `.local/diagnostics/` directory bind; a single-file
bind pins the obsolete inode. The directory contains diagnostics only, not host
auth/control files. Existing stacks need an explicit re-render/recreate during
CHG-0202 acceptance; do not deploy only a new collector against the legacy mount.
Container/volume-loss proof and catalog rebind/respawn remain deferred while the
owner's Docker restriction is active.

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

Operator ops surface (ADR-0077): `hubctl usage --spool <dir> [--days N]
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
divergence line. `hubctl apply -f` accepts a declarative `space:` block that
converges `agent.yaml`/`workspace.yaml` through full stack validation —
secrets keys are rejected by construction.

Storage accounting (SPEC-0085): `hubctl storage status --spool <dir>
--toolhub-store <path>` prints per-owner allowlisted aggregates only —
logical bytes, file counts, byte/file limits, completeness and a safe status
word per storage owner (`communication-spool`, `toolhub-state`); paths, object
names and raw errors never leave the process. `hubctl storage cleanup --spool
<dir> [--retention 72h]` is a dry-run by default: it emits a digest-bound plan
(scope, aggregate files/bytes, `sha256:` digest — never the candidate list
itself) over owner-regular files, rejecting planted symlinks. `--apply
--digest <sha256:...>` takes the spool writer lock, recomputes the plan and
refuses a stale or foreign digest before running the bounded terminal
compaction — aged done/failed records move to the archive, live
pending/running state and open approvals are never touched.

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
hubctl capability --kind group --file g.json --toolhub-store <path>   # validate+print digest only
hubctl capability --kind preview --file draft.json --toolhub-store <path>  # old→new diff, never writes
hubctl capability --kind tools --settings <space> \
    --set terminal=native,browser=mcp:playwright+ro --confirm          # surgical workspace.yaml write
hubctl governance --kind rule --scope user --user alice \
    --section native:terminal --effect allow --reason "reviewed" --confirm
hubctl governance --kind grant --user alice --section user_mcp --expires 72h --confirm
```

Connector verbs run identically on three surfaces — `connector_*` tools,
`hubctl connector <verb> --target <sel>` (`--profile`/`--env`/`--generation`),
and `POST /v1/connectors:*` on the control listener — all through
`ControlPlane.InvokeConnector`, one journal.

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

## Human-action links

`/credentials`, `/confirm`, `/remove`, `/connect`, `/device` are one-time
`?t=` URLs; spent/expired → 410. The stable wizard link `/connect/<id>?k=`
is the exception: one URL per onboarding that renders the current phase
(credentials form, broker hop, confirm, OAuth interstitial, progress,
result) and self-refreshes through the flow; it expires with the onboarding
and dies on terminal phases' TTL. Default target is loopback. For other-
device access publish TLS and set `HUB_PUBLIC_ORIGIN=https://<host>` (https
only — anything else mints loopback) + `HUB_PUBLIC_LISTEN=<addr>` on
hub-toolhub; that listener serves only human-action paths (control/gateway
→ 404) and the gateway accepts an https `form_url` only when host equals the
configured origin exactly. `hubctl connector link --onboarding <id>` prints a
pending URL; `--confirm` completes an awaiting-confirmation onboarding.

## Outbound exposure (tunnels)

Opt-in per environment: `features.exposure` + a reviewed
`connections/exposure/config.yaml` (provider `sish`, pinned relay host keys,
`zone`, `key_ref` file:/broker:, bounds, named endpoints). Render fails
closed without a valid config; the config mounts read-only at
`/state/exposure` and `HUB_EXPOSURE_CONFIG` is wired into the runtime env.
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
- File-delivery contracts (e.g. Google service-account JSON) need a shared
  tmpfs at the same absolute path on broker, controller and daemon — set as
  `credential_mount_root` in the controller config; requests reject without
  it.
- `proxy_environment` names must end `_PROXY` (split `*_PROXY_HOST/PORT`
  rejected). A pre-rule `store.json` fails startup with `invalid or duplicate
  proxy environment parameter` — move offending names to `environment` and
  restart toolhub.

## Migrations

All explicit, dry-run first, never overwrite:

```text
hubctl migrate-spaces --user artem --org acme [--apply]
hubctl spool migrate --spool <dir> --state <dir> [--apply]   # legacy knowledge tree
hubctl context backup|restore|export|purge [--spool <dir>] [--confirm]
```

`migrate-spaces` rejects symlinks, ID collisions, unrelated data and active
runtimes; reports JSON paths/counts/checksums, no secret values. `spool
migrate` copies (never moves) knowledge into `<state>/knowledge/<principal>`;
other spool dirs (`schedules`, `occurrences`, `watchers`, `digests`, `tasks`,
`events`, `outbox`, `mappings`, `conversations`, `inbound`, `callbacks`) keep
their paths. `context restore` refuses another user and never reactivates
revoked credentials; plaintext secrets stay on `hubctl secret backup`,
never in the general archive. `down` never deletes homes; `purge` needs
`--confirm`.

Memory: native files under `spaces/<id>/hermes/memories` (`MEMORY.md`,
`USER.md`) — `hubctl memory list` / `hubctl memory delete --name`. Honcho is
optional (`honcho` + `honcho_url`); its failure must not break ordinary
Hermes memory. Skills install into `spaces/<id>/hermes/skills` without
rebuild; `hubctl skill install --consent` scans SKILL.md and refuses
provider-mutation text.

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

See [runtime operations](runtime-operations.md#images-and-builds).

## Supervisor and scale-to-zero

See [runtime operations](runtime-operations.md#supervisor-and-scale-to-zero).

## Per-context execution rollout

See [runtime operations](runtime-operations.md#per-context-execution-rollout).

## SSH capability operations

`ssh` entry in `workspace.yaml` `tools:`; `ssh_write`/`ssh_shell`/`ssh_tunnel`
stack on it. Config lives in `connections/ssh/config.yaml` mounted read-only
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
`hub-media` (execution `dedicated`, policy_version `hub-media` — stable
service-level identity), write the grant id to `spaces/<u>/media.grant`,
re-render. Fallback for standalone runs: `HUB_MEDIA_UPSTREAM_KEY`/
`OPENAI_API_KEY`/`FAL_KEY` env; a fal engine with neither refuses to boot.

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
  through `compose config`; part of `just check`/`just build`/image CI.
- The native container gate starts a real MCP stdio child against a local
  fixture — no Telegram contact. CI publishes `cli-tools`/`cell-proxy`
  stages; `just docker-check-prebuilt` pulls them.
- Never `hermes update` in prod — bump the reviewed pin and rebuild.
- `HUB_TOOLHUB_AUTOSTART=true` starts the shipped `hub-toolhub` binary.

## M16 runtime preparation and group scopes

See [runtime operations](runtime-operations.md#m16-runtime-preparation-and-group-scopes).

## Site hosting

Configure reviewed `hosting.slots` and exposure before render/doctor. Dynamic hosting requires an approved digest-pinned minimal image for the fixed go-http binary and explicit runsc/Kata; no runtime fallback. Preserve site and coordination volumes during recovery. See [hosting operations and recovery](services/hosting.md); native runtime/sish/browser checks are separate prerequisites.
