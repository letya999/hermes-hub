---
description: User-scoped immutable static sites and isolated stateless go-http workloads, private publishing and sish serving.
last_verified: 2026-10-26
---
# Site hosting

The opt-in `hosting` feature adds a Go web/publishing service independent of
Hermes. Source: `internal/hosting`, `internal/stack/hosting.go`,
`internal/agenttools/hosting.go`, and the hosting-only ToolHub controller.
Decision: [ADR-0081](../adr/ADR-0081-user-scoped-site-hosting.md); requirements:
[SPEC-0079](../../specs/active/SPEC-0079-user-scoped-site-hosting.md).
The implementation has offline contract tests; native runtime, browser and
sish acceptance remain separate deployment prerequisites. Do not interpret
rendered Compose or inspect fixtures as proof of isolation.

## Operator configuration

Enable `hosting` and `exposure` in the space's feature policy. Configure an
owner-bound sish exposure connection with the existing per-user DNS/wildcard
certificate setup. Hosting consumes it; publishing makes no DNS mutations.
Declare up to four slot names in `hosting.slots` in `agent.yaml`. Slots and
manual exposure endpoints must be distinct and fit the exposure connection cap.
The resulting origin is `https://<slot>.<user>.<relay-domain>`; for a relay domain
`hub.example.test` this is `page.alice.hub.example.test`.

```yaml
hosting:
  slots: [page, app]
```

Static-only mode needs no app image. Dynamic mode additionally requires
`hosting.image` with an operator-reviewed digest-pinned minimal image and
`hosting.runtime: runsc` or `kata`. There is no default runtime fallback.
Image/runtime/profile changes enter the rights policy digest; existing dynamic
revisions with a different admission digest are revoked during reconciliation.
Do not supply a guessed image digest. The image only provides a rootfs that can
exec a static Linux binary — a distroless static or alpine-class image is
enough; no language toolchain is required inside it.

Rendering writes `generated/hosting.<env>.json`, optional hosting controller
configuration and separate per-environment control token files under `runtime/`.
Use the normal render/doctor workflow; never print tokens or environments.
The web tier runs on Linux, UID 10001, with the selected workspace read-only
and its own site volume writable. It has no Docker socket or full space mount.
Only the private controller receives the socket. No host ports are published.
Existing Broker runtime authentication is used for the sish credential when its
reviewed exposure configuration selects Broker delivery.

## Agent workflow and access

Build using the existing admitted workspace/CLI workflow. Publishing only
copies prebuilt regular files; it never installs dependencies or runs hooks.
A `dynamic` publish additionally requires `app` at the source root: a static
Linux ELF built in the workspace (`GOOS=linux go build -o app`); missing or
non-ELF `app` fails before any snapshot work.
`site_publish` accepts `name`, workspace-relative `path`, `request_id`,
`expected_revision`, optional `mode`, `profile`, `access`, `lifetime_seconds`.
A new slot starts with expected revision 0; replacements use `site_status`'s
revision. Names must be operator-declared slots. It returns the origin, revision
and, for a newly issued share, the one-time returned fragment URL.
`site_list`, `site_status` and `site_unpublish` operate on the same trusted
owner/environment binding. No tool argument chooses owner, image, command,
mount, environment variables or runtime. Repeated request IDs do not reissue
share tokens. Failed preparation consumes a revision; callers use the current
published revision for their next replacement, not an assumed counter.

The default access is `link`: a random fragment token is exchanged for a
host-only `__Host-hub-site` Secure/HttpOnly/SameSite cookie. Only a token hash is
stored. Bootstrap clears the fragment before loading application content.
Links expire after 24h, sessions after at most 1h, bounded by site lifetime.
Every asset, HEAD, range and conditional request passes the access gate.
`public` requires explicit `authorized: true`; `private` creates no public route.
Unpublish, replacement and expiry invalidate old access. Already downloaded
content cannot be recalled. All site responses forbid caching, workers and
framing; dynamic pages may connect only to their own origin.

Dynamic server cookies are rewritten to a Secure/HttpOnly/SameSite host-only namespace tied to
the site revision. Only those cookies are decoded for the app; parent-domain
Hub credentials, hosting cookies, Authorization and forwarding headers are
never relayed. Domain cookies and cross-origin redirects are rejected.
Applications that depend on JavaScript reading an unmodified cookie name are
unsupported by this first profile. Unsafe requests with cookies/link access or
an Origin header require the exact site Origin; public cookie-free server
requests may omit Origin. WebSocket, SSE and long streams are unsupported.

## Storage, lifecycle and bounds

Published data uses the dedicated `hermes-hub-sites-<user>-<env>` volume,
separate from the build workspace. The volume contains an owner marker,
exclusive writer lock, immutable random release directories, durable revision/
operation metadata, pending and retired finalizers, and bounded audit segments.
Unowned nonempty stores are refused. Root-confined descriptor copying rejects
traversal, links/hardlinks, special files, hidden/config/credential files and
source changes detected during the snapshot. Promotion follows durable
preparation; failed replacement leaves the previous release intact.

Defaults: four sites, two dynamic workloads, 512 MiB and 10,000 entries per
store; 128 MiB per release. Staging, retired releases and metadata count;
replacement needs old plus new capacity. Metadata/revocation headroom is
reserved. Audit has a separate 64 MiB budget and seven-day retention; pressure
returns 503 instead of deleting live audit records. Events allowlist identity,
site/revision, operation, outcome, bytes and duration, excluding URLs, headers,
IP addresses, bodies, app output and credentials.

A site lives seven days by default, at most 30 days. Traffic and tunnel renewal
do not extend it. Restart policy is `on-failure` (at most three attempts per revision
with bounded backoff; a stricter lifetime-wide budget than the ten-minute ceiling) or `never`. Unpublish persists a tombstone and
cancels requests before finalization. Failed cleanup remains charged and is
retried; receipts are retained until resources are verified absent.
Publish is limited to six/minute, burst two; serving to 100/s per user and 20/s
per site, 32/8 in-flight requests, 1 MiB request body, 30s deadline and 10 MiB/s.
Dynamic responses are bounded at 8 MiB and relay buffering has two slots.

A per-user `hermes-hub-hosting-zone-<user>` coordination volume fences dev/prod
from simultaneously owning the same external hostnames. It contains only the
address lock. Site data, tokens and controllers remain separate by environment.
Hosting-only controllers share a small `hermes-hub-hosting-fleet` metadata
volume for serialized global reservations: at most eight live workloads,
further limited by detected CPU/RAM headroom, and two per owner across envs.
Pending/replacement resources count. Expired leases release capacity only after
the immutable PID1 deadline plus a drain margin; cleanup receipts are retained.
These coordination volumes contain no app files or credentials.

## Dynamic runtime boundary

The only admitted profile is stateless `go-http`: the committed `/work/app`
static Linux binary listens on `127.0.0.1:8080`. A trusted non-dumpable PID1
executes it directly, reaps children and enforces the fixed expiry. No user
command strings are run.
The controller verifies the complete profile before and after start, executes
an isolation canary and checks application readiness before promotion.

Each workload uses UID/GID 10001, read-only root and release, all caps dropped,
no-new-privileges, the existing seccomp profile, 1 CPU, 256 MiB RAM/swap ceiling,
64 PIDs and 32 MiB noexec/nosuid/nodev tmpfs. It has `network=none`, no secrets,
Docker socket, host mounts, Hub network or persisted stdout/stderr (log driver
`none`). A separate helper-only volume provides the trusted cellinit binary.
The site volume is mounted read-only with the exact release as volume subpath;
a daemon lacking that feature must fail admission.

Ingress uses authenticated private controller RPC and a bounded OCI-exec
stdin/stdout relay to the fixed loopback address inside the workload. There is
no app network listener on the Hub network and no arbitrary relay destination.
Missing runsc/Kata, invalid inspect/canary/readiness or unsupported configuration
fails closed. Broker app-env delivery, persistent writable app data, arbitrary
builds, extra languages and runtime installation are not implemented.

## Recovery and verification

Restart the same generated deployment and volumes; the service acquires zone
and store locks, validates ownership, completes interrupted audit admissions,
revokes expired/disallowed revisions and reconciles desired resources before
serving. Do not delete locks, metadata or fleet reservations to force admission.
Use status and retry unpublish with a new request ID/current revision for a
pending finalizer. A full fleet receipt ledger requires operator investigation,
not broad container/volume pruning. Controller shutdown attempts bounded owned
cleanup; abrupt host loss still relies on persisted receipts and hard expiry.

Offline tests cover store authorization/concurrency, filesystem confinement,
replacement/quota/recovery, share/HTTP gates, safe relay contracts and controller
profile rejection. Real Linux resource exhaustion, runsc/Kata escape canaries,
actual volume-subpath support, sish reconnection/TLS and browser cookies/CSP must
be verified on an authorized isolated stack before deployment acceptance.
See [validation](../validation.md) and [testing](../testing.md). Hosting does not
provide HA, a public identity provider or an uptime SLA.
