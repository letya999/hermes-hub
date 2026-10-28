---
description: Operator runbook — stack lifecycle, user onboarding, diagnostics, secrets, links, broker ops, migrations, backups, image hygiene, supervisor, rollout, preflight.
last_verified: 2026-10-28
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
hermes-diagnostics.txt` (100 MB rollover, cursor prevents replay; bounded
per-pass and per-call so a stalled daemon cannot starve the cycle). Host
supervisor mirrors its own logs to `.local/supervisor.log` (10 MB); the
collector copies only lines naming that space's owner as a whole token.
Compose keeps Docker `local` rotation (10 MB × 3/container). Secrets are
redacted at collection. Opt out: `diagnostics: false` in the infra space's
`agent.yaml`, then `hubctl up`.

Gateway message logs carry IDs, status and `text_len` — never bodies;
bracketed markers (`[voice message]`, `[credential input redacted]`) still
print. Temporary content window: `HUB_LOG_CONTENT_UNTIL=<RFC3339>` in the
channel env + restart; clamped ≤1 h ahead, auto-reverts. Treat the combined
file as private; `.local/` and `spaces/` are git-ignored — redact before
sharing.

Structured logs are JSON: `time`, `level`, `msg`, `component` plus
`job_id`/`run_id`/`principal`/`status`. `hubctl trace <job_id> [--user]`
merges supervisor + space logs + container logs by job identity, redacted;
cross-principal jobs are refused. `/status` reports caller-scoped health:
p50/p95 receive→deliver latency, unconfirmed/failed counts, restart counter,
average input tokens.

Agent-visible diagnostics: `mcp__toolhub__diagnostics` returns the caller's
own bounded, redacted lines and workload health, plus `excluded` projection
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
spool's `events/` with jobs/mappings/outbox — receipts repair incomplete
handoff on restart.

## Images and builds

Four role images share the staged Dockerfile (SPEC-0082):
`hermes-hub:0.4.0-<env>-core` (control-plane daemons: cliproxy,
communication-hub, supervisor, relays), `…-control` (adds Docker CLI +
ToolHive; only toolhub + workload-controller), `hermes-hub:0.4.0-<env>`
(the agent runtime — Python venv + node, no browser or voice stack), and
`hermes-hub:0.4.0-<env>-browser` (per-space sidecar: headed Chromium under
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
asleep or restarting — transient, clears on wake.

Entry points (`just`, `hubctl build`/`up`) force `DOCKER_BUILDKIT=1`,
`COMPOSE_DOCKER_CLI_BUILD=1`, `COMPOSE_BAKE=true`; `hubctl` prunes
retag-orphans before/after and caps builder cache at 8 GB.
`HUB_DOCKER_CACHE_FROM`/`_TO` forward `--cache-from/--cache-to` (CI uses
`type=gha`); builds always `--load`.

`dev-rebuild` (dev only, service must already run):
`go run ./cmd/hubctl dev-rebuild --dir spaces/alice --env dev <service>` —
compiles one Linux binary, overlays the dev image, recreates only that
service; restores prior image on failure. Names: `communication-hub`,
`credential-broker`, `hub-media`, `toolhub`, `workload-controller`,
`supervisor`, `hermes-runtime` (supervisor mode records it for next spawn;
a full `hubctl up` returns to base). Verify with `docker ps --filter
name=hermes-hub-<user>-dev-<service>-1` → `…-dev-<service>:local` + `Up`.

Cleanup: `just docker-clean` drops stale `hermes-hub:*` tags unreferenced by
`spaces/*/compose*.yaml`, stopped `hermes-*` containers pinning dangling
images, orphan `hermes-build-*` objects; `--deep` also drops
`hermes-build-state-shared` — run only with no artifact build in flight.
Windows disk reclaim: `scripts/reclaim-docker-disk.cmd` (admin; stops Docker
Desktop/WSL, Optimize-VHD).

CI: `image` job runs `devcheck docker-publish <target>` → immutable
`ghcr.io/<owner>/<repo>/hermes-hub:<sha>-<target>` + moving `edge-<target>`;
`containers` job runs `just docker-check-prebuilt` (`docker-pull` resolves
sha tag, `edge-*` fallback, warns when pulled revision ≠ local HEAD). Local
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
cold start ~60–80 s, warm lease ~0.6 s; `--max-runtimes` (8) is the
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

Spool retention: hourly compaction past `HUB_SPOOL_RETENTION` (336 h) —
live work, open approvals and in-flight receipts never removed. `occurrences`
archive holds 48 h detail + dedup receipt for the retention period; catch-up
≤1 h, ≤16 occurrences per tick; malformed → `occurrences/quarantine`
(investigate before clearing). `HUB_MOCK_DIR` keeps mock/e2e capture out of
the production spool. Never delete uncertain mappings/leases to force a
retry — the original run may already have acted externally.

Gateway/session ops: `/new`, `/use`, `/delete`, `/sessions`, `/session`
manage per-task sessions (each task = own durable Hermes session; forum
topics supported; slash-command list enforced + registered as bot menu).
`/usage [session]` reads the journal/quota; session mode reads the pinned
Hermes session API and renders `unknown` where upstream cannot report.
Long Telegram turns queue via `display.busy_input_mode: queue`; heartbeat
`HERMES_AGENT_NOTIFY_INTERVAL=60s`; `/stop`, `/cancel` are explicit.
Replies >4096 chars arrive as `response.txt` (≤2 MiB). Lost send
acknowledgement = uncertain — inspect outbox, never blind-retry.
`scope.yaml` IDs are immutable; Telegram links are provisioned only in
trusted host config after verifying the numeric account ID — never link
from username/email/message-request.

## Per-context execution rollout

`hubctl execution-audit --spool <dir> --user <u>` then
`hubctl select-execution --dir spaces/<u> --env <e> --execution-mode
supervisor --supervisor-url <url> --native-cron disabled
--compatibility-release 0.2.1 --apply`, then `hubctl up`. Drain first; the
CLI refuses claimed/admitted jobs and uncertain outcomes; apply verifies the
audited path is the stopped gateway's actual `/data` mount and requires zero
enabled native cron jobs (checked against a disposable read-only home
snapshot). Container deployment can select `http://supervisor:8876` —
no host ports, private DNS (ADR-0040). Host mode needs the listener on a
private interface reachable from both host CLI and gateway container, with
`HUB_SUPERVISOR_AUTH` to both; on port change re-render and recreate
communication-hub + toolhub. Rollback: drain, `--execution-mode static`
without URL (+`--native-cron unmigrated` if needed), keep same spool/home;
do not reactivate native definitions already migrated to hub occurrences.

## Skill git installs

`hubctl skill install --dir spaces/<u> --url <https github/gitlab url>`
fetches through the controller's bounded `/cli-exec` cell (plan
`skillgit@1.0.0`, `git` on the cli-tools image — never an owner command;
egress is exactly the repository host, hooks/protocol.file/submodules/LFS
are off, fetch is `--depth=1 --filter=blob:none` into the per-owner bare
mirror `skills/.sources/<id>.git`). The result is a scanned **draft** —
approve it with `hubctl skill approve --id <d>` or the owner card. A repo
URL without `/tree/<ref>/<path>` returns `candidates` instead of picking
one; retry with `--path`. `--ref` pins a branch/tag to its resolved commit,
`--name` overrides the draft name. `hubctl skill update --name <s>` (or
`--all`) refetches tracked skills into new drafts and reports
`force_push:true` on rewritten upstream history. `hubctl skill sources
list` shows mirrors and the skills each feeds; `sources remove --id <id>`
deletes the mirror — installed skills keep working but lose update
tracking.

Sources are https GitHub/GitLab only; a self-hosted GitLab needs
`gitlab_host: <host>` in `workspace.yaml` — render adds its egress and the
third `skillgit` approval tuple, then `hubctl up`.
Authentication: agents name a broker credential with `grant` (the gateway
leases it into `GIT_TOKEN` for the cell); the operator path takes
`GIT_TOKEN` from the environment. Tokens never enter URLs, argv, git
config, logs or `.origin.json`.

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
and `evals/evals.json` inside the skill, materializes an isolated temp
space containing only that skill, and scores validation accuracy through
`HUB_SKILL_EVAL_URL` — an operator eval endpoint that runs each prompt
against a real model and returns the run receipt (`skill` tool calls mark
activation). Reports land under `.work/tmp/`; without the endpoint every
leg reports `model_unavailable` — never a fabricated pass. Platform
skills ship their trigger suites under `config/skills/*/evals/` and
`internal/systemguide/evals/`; lint-cleanliness and fixture validity are
enforced by tests.

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
closes them; tunnel listeners are loopback-only, never published. Every op
appends a bounded receipt to `runtime/audit/ssh.jsonl` (exit codes, byte
counts — never output or key material). Revoking a grant or deleting a host
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

Optional managed-sandbox preparation lives in `workspace.yaml`:

```yaml
workspace:
  goal: Prepare the workspace tools needed for this project.
  bootstrap_revision: 1
```

Before an ordinary job, the runtime runs that goal once with its existing
sandbox and egress grants. No host installer is invoked. `runtime/bootstrap.json`
records `running`, `ready` or `failed`, the revision key, changed/added/deleted
workspace paths and `changes_available`. `runtime/bootstrap-history/<key>.json`
retains each goal/revision receipt across an explicit retry, including session/run
IDs and the first 4096 runes of redacted output or failure. Detailed upstream
history stays in that owner's Hermes session. Use increasing revisions for
new attempts; never reuse a prior revision for a different preparation history.
A workspace diff is bounded to 4096
entries and 64 MiB of file content. The shared run budget bounds preparation;
waiting callers can cancel independently. Failure blocks ordinary jobs and
retains a partial diff when available. Inspect and repair deliberately, then
increment `bootstrap_revision` to request a new attempt. Removing `goal` disables
preparation; no data is migrated or deleted. The default manifest revision is 0.

A configured group runs in one of two modes, chosen per chat in the host
communication configuration:

```yaml
groups:
  -1009999: topics         # personal interface: each forum topic = a session
  -100123:                 # mention mode (the default)
    space: team
    members: [11, 22]      # optional; empty = every configured Telegram user
    mention: '@helper'     # optional; empty = the bot's own @username
```

The same map accepts only the scalar mode (`topics`) when nothing else is
needed, and keeps the legacy list-of-objects form working. An unconfigured
group is always ignored.

`topics` mode makes the group a personal interface: the sender is resolved
through the ordinary `telegram_ids` user mapping, each forum topic adopts its
own task/session inside the sender's personal home, and messages plus slash
commands behave exactly like a private chat — no mention needed. Jobs carry
the usual `user:<id>` authority and write the sender's own memory.

`mention` mode keeps the bot a passive participant and requires a dedicated
group home. Create it with
`hubctl group-init --dir spaces/team --user team --env dev`.
It refuses existing scopes and never converts a populated personal profile.
In the host communication configuration, add a dedicated enabled `users` entry
for `team` with its own state/workspace and no personal Telegram/Slack identity.

Only configured members contribute to the bounded passive transcript (last
16 excerpts, at most 512 runes each); with an empty `members` list every
configured Telegram user counts as a member. A literal mention — the
configured `mention` tag or, when unset, the bot's own @username resolved
through `getMe` — starts an ordinary group job; slash commands retain their
explicit command behavior. Passive messages do not wake the model or write
personal memory. Recent source-event receipts are bounded to 256; admitted
jobs retain durable idempotency mappings. The Telegram bot must actually
receive the messages needed for this transcript; the Hub does not change
account/privacy settings automatically. Edits do not restart work, and groups
reject credential input.

Group runtime authority is `group:<space>` and requires a matching group
`scope.yaml`. Replies stay in the original group/topic; approval taps require
the original actor and audience. Membership/policy are checked again before
dispatch and delivery. Personal homes/memories are never copied into a group.
Active execution, observation streams and pending approvals retain the existing
typed leases; the idle deadline begins after the last release.

Source-only verification never proves that a running image contains these
changes. M16 local work does not build, deploy, restart or contact a real account;
image/channel/provider acceptance must be recorded separately after an authorized
rollout.

## Site hosting

Configure reviewed `hosting.slots` and exposure before render/doctor. Dynamic hosting requires an approved digest-pinned minimal image for the fixed go-http binary and explicit runsc/Kata; no runtime fallback. Preserve site and coordination volumes during recovery. See [hosting operations and recovery](services/hosting.md); native runtime/sish/browser checks are separate prerequisites.
