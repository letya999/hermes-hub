---
description: Runtime images, supervisor lifecycle, per-context rollout and preparation runbook.
last_verified: 2026-11-10
---

# Runtime operations

Runtime-specific sections of the [operator runbook](operations.md).
Existing operations.md anchors retain links here.

## Images and builds

Two build targets share layers: core `hermes-hub:0.5.0-<env>` and
`-control` (adds Docker CLI + ToolHive; only toolhub + workload-controller).
Optional trees (Playwright browsers, messaging/google/voice extras, Google/
Atlassian/Telegram-account Python) stay out of the default image — own
Dockerfiles under `docker/`. Generated compose carries exactly one core
`build:` (cliproxy) and one control `build:` (toolhub); everything else
references those tags — raw `docker compose build` would repeat the
Dockerfile per service.

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
`supervisor`, `hermes-runtime`, `model-relay` (supervisor mode records it for next spawn;
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
Telegram shows the queued timer immediately after durable enqueue, including
during cold supervisor admission; the status becomes a running timer once a
run ID is assigned. ToolHub rights decisions allow up to 20 seconds before
denying on timeout.
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
`HERMES_AGENT_NOTIFY_INTERVAL=60s`; `/stop`, `/cancel` are explicit. An editable
status appears at acceptance and ticks through cold start; typing starts after
admission.
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

## M16 runtime preparation and group scopes

Optional managed-sandbox preparation lives in `workspace.yaml` (unmanaged
spaces only — the v2 passport vocabulary has no `goal`/`bootstrap_revision`
leaf yet, so migration reports them as preserved conflicts):

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
