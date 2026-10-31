---
description: Accepted user-scoped static and dynamic hosting behind exposure endpoints, isolated admitted workloads, bounded snapshots and revocable artifact share links.
last_verified: 2026-10-26
---
# ADR-0081: User-scoped site hosting

Status: Accepted 2026-10-26 — owner accepted the researched hosting direction
after the Haloy source review and directed the ADR/SPEC/CHG/state transition
for M18 #147. This records the target architecture, not deployed guarantees.
Evidence:
[CHG-0169 research](../../.work/done/CHG-0169-site-hosting-design/research.md).
Expanded evidence:
[ecosystem / MVP research](../../.work/done/CHG-0169-site-hosting-design/ecosystem-research.md)
(Context7, upstream code, provider architecture and community field reports).
Pinned source follow-up:
[Haloy review](../../.work/done/CHG-0169-site-hosting-design/haloy-review.md).
Consumes ADR-0079/SPEC-0077 and ADR-0080/SPEC-0078.

## Context

The owner needs “build a page and return a link” and small HTTP applications
that survive the chat. Existing exposure declares endpoint targets; DNS supplies
`<site>.<user>.<base>` and per-user wildcard TLS at sish. The base is normally
`hub.<domain>`; hosting never invents another suffix or performs per-site DNS.
Current exposure runs in Hermes and keeps live state in memory. Its in-process
SSH implementation differs from ADR-0079's admitted workload framing, and live
sish acceptance remains pending. Hosting cannot inherit chat lifetime or assert
that these target guarantees have already been demonstrated.

## Decisions

- **D1 — Ownership and surface.** Opt-in hosting is bound to the authenticated
  principal, environment and reviewed DNS/exposure zone. `site_publish`,
  `site_unpublish`, `site_list`, `site_status` act only in that binding.
  Publish accepts a relative workspace source, validated site label, static or
  approved dynamic profile, access intent and expected revision. It accepts
  no owner, domain, host target, image, mount, shell string or arbitrary env.
  A request to create and share a page authorizes link publication; public
  access and Broker grants require their concrete user instructions.
- **D2 — Web tier.** A separate per-user hub-owned Go hosting service survives
  agent shutdown. Its public data listener serves committed static snapshots
  or proxies to one admitted dynamic site through an isolated per-site relay.
  Its private authenticated control listener is never an exposure target.
  sish retains TLS termination. This explicitly adds an application web tier
  behind the primitive, qualifying ADR-0079 D1 without changing its ban on
  exposing hub control/gateway surfaces.
- **D3 — Exposure composition.** A published slot reserves one named endpoint
  under the bound zone. Trusted hosting reconciliation creates a restricted
  endpoint intent whose target is only its hosting listener; generic
  `exposure_open` remains name-only. Reviewed operator policy delegates a
  bounded slot namespace; agents cannot rewrite exposure config. Manual and
  hosting names share a collision/cap check. No wildcard or shared multi-user
  tunnel. Durable intent reopens bounded leases independently of Hermes; site
  expiry/unpublish always wins over reconnect and restart.
- **D4 — Dynamic trust boundary.** One ToolHub-style admitted workload per
  dynamic site, with verified image digest, non-root UID, read-only root/release,
  dropped capabilities, seccomp, no-new-privileges, CPU/RAM/PID/tmpfs limits,
  owned network and inspect receipts. Use approved operator profiles, not
  arbitrary images or Dockerfiles. Reuse admission/finalizers, not ephemeral
  CLI cells or their idle budget. No socket, host networking, hub routes,
  credential stores, full workspace, sibling site or other space mounts.
  Default egress is none; a per-site relay cannot forward arbitrary targets.
  Hosting additionally requires an explicit secure runtime: reference `runsc`,
  verified `kata` alternative; no implicit runc fallback. Existing
  `cell.go:runtimeFlag` recognizes both but does not make them the default.
  runsc's userspace kernel is not a dedicated guest VM; this remains owner
  hosting, not a certification for hostile public compute. Missing runtime,
  enforced controls or isolation canaries deny dynamic publish.
- **D5 — Durable lifecycle.** Site state and expiry are independent of jobs.
  Default lifetime 7d, maximum 30d; renewal is explicit owner publication.
  `never` and bounded `on-failure` restart policies are supported; default
  on-failure is 3 attempts/10min with 1–30s backoff. Restart/tunnel renewal
  cannot extend expiry. Startup reconciliation checks receipts, desired
  revision and tombstones before restoring anything. Unpublish revokes access
  first, drains/cancels within 30s, then finalizes workload/network/tunnel to
  verified zero. Failed cleanup remains durable and retryable.
- **D6 — Immutable storage.** Logical roots are `spaces/<user>/sites/`, backed
  by a dedicated hosting volume per user/env. Publisher copies regular build
  outputs from a confined workspace source into staging and atomically
  commits an immutable release; web tier has read-only release access.
  Publisher never executes build/install hooks. Executable preparation uses
  an admitted build/CLI workload with separate bounds and no inherited secrets;
  unsupported builds fail closed rather than run in the agent process.
  Runtime sees only its release plus bounded tmpfs. Cross-process quota
  reservations include staging and retired releases. Never expose a live
  workspace or writable full-space bind. Named volumes are persistence, not
  disk quotas. Persistent writable app data is deferred until a separate
  hard filesystem quota contract; first dynamic profiles are stateless.
- **D7 — Access.** Default artifact mode is revocable expiring bearer access;
  public mode is explicit, private mode has no external route. An opaque
  random token (at least 256 bits; stored hashed) binds owner/site/revision/
  share epoch, default 24h/max 7d and bounded by site expiry. Fragment bootstrap
  exchanges it for a host-only `__Host-` Secure HttpOnly session cookie before
  site code loads; reusable assets work without per-asset tokens. Every request
  checks epoch/expiry; republish/unpublish invalidates old links and sessions.
  Raw tokens are returned only in publish delivery, never list/audit. This is
  bearer authorization, not visitor identity; visitor login is deferred.
- **D8 — HTTP boundary.** Exact allocated Host and listener binding determine
  site, never client forwarded headers. Strip hosting credentials before
  proxying; disallow parent-domain app cookies and hub credentials on site
  origins. Cookie-authenticated unsafe methods require exact Origin; no
  cross-site CORS default. Static GET/HEAD only, no listings/dotfiles/links or
  special files. Separate hosting domain from hub login where possible.
  Request/response headers, time, body, concurrency and bandwidth are bounded.
  Platform CSP denies service workers and undeclared browser destinations;
  server egress restrictions alone do not constrain visitor-side JavaScript.
  Revoke prevents new server reads, not use of bytes already downloaded.
- **D9 — Budgets and audit.** Trusted policy bounds storage/sites/CPU/RAM,
  publish/serve rates and lifetime; users only narrow it. Contract defaults
  and acceptance boundaries are in SPEC-0079. MVP has one writer/replica per
  user/env; CLI and agent submit through its private API. An exclusive store
  lock fences competing processes; no distributed store/counters. Serialize
  quota and revision changes, reserve overlap during replacement. Emit safe
  publish/unpublish/serve/deny receipts with bounded retention; audit admission
  failure blocks publication/serving. Never log raw URLs, queries, fragments,
  request bodies/headers, app output, credentials or credential dictionary keys.
- **D10 — Secrets.** Dynamic workload env is empty except an allowlisted
  profile baseline. Optional secret env uses explicit per-site/revision Broker
  authorization and existing Acquire/Materialize/Release contracts; no raw env
  tool argument or inherited agent credentials. Workload code can read granted
  secrets: grant is explicit trust in that revision. Republish requires renewed
  grant binding; revocation stops the workload and clears materialized secrets.
- **D11 — Small complete deliveries.** First deliver static hosting end to end,
  including tools, addressing, restart recovery, shares and quotas. Client-side
  HTML/CSS/JS does not need a server workload. Next deliver one approved
  stateless `python-http` profile: preinstalled Python stdlib, fixed entrypoint
  convention and private port, no dependency installation or hot reload.
  Dynamic remains required to finish #147. Multiple languages, visitor OIDC,
  dashboards, autoscaling, persistent writable disk and additional hosting
  providers are deferred. Optional Broker env delivery may follow separately;
  until present, requests for secret-enabled apps fail closed.

## Alternatives

Subprocess in Hermes: rejected, shares authority and chat lifecycle. Deno or
WASI-only backend: deferred, constrains ordinary apps and still needs hosting
lifecycle/budgets. Direct exposure of each app: rejected, bypasses access and
serve controls. Shared daemon mounting all spaces: rejected, unnecessary blast
radius. Self-contained signed URL: no advantage over revocable hashed opaque
tokens with existing durable state. Parent-domain auth cookies: rejected.
Bind-mounted live workspace: rejected, mutable and too broad. Arbitrary writable
volume with periodic size scan: rejected, cannot enforce hard disk bounds.

Ready-made alternatives were investigated, not assumed absent. artifact-site
is the closest standalone static product but adds Next.js/Postgres and lacks
dynamic workloads; Dokku/Coolify/Dokploy add another privileged PaaS lifecycle.
OpenSandbox's Docker backend lacks its K8s-only tenancy/secure-ingress modes.
Caddy can serve bytes, but still needs our identity/quota/share/audit boundary.
workerd explicitly requires an external sandbox for malicious code. nsjail is
a credible OS-process backend but introduces a second confinement integration.
Cloudflare's official publisher/internal-sites templates are a useful managed
alternative if the owner later chooses that provider boundary. Evidence and
specific upstream links are in ecosystem-research.md, not deployment claims.

Haloy was additionally cloned and reviewed at commit `3cd1d500394a81532970dc4cdb57e02d77b2be62`.
An unchanged Haloy behind a restricted API adapter cannot express the required
pre-start workload envelope; private-network discovery, external HTTP serving,
revision-fenced cleanup and share policy also require changes inside its core.
A fork would duplicate existing ToolHub admission while retaining a second
deployment stack. Prefer extending our controller and borrowing snapshot/hash,
reconcile and readiness patterns. No Haloy code is copied or dependency added;
its small internal healthcheck package is only a possible later extraction,
with initial-healthy semantics reviewed before reuse. See haloy-review.md for
source anchors, limits, unit/race results and toolchain-dependent scan findings.

## Consequences and acceptance boundary

Defines a Go hosting service and a persistent workload class for implementation;
optional app images stay outside the base image. No new framework, database or
queue. No upstream Hermes patch. Browser origin boundaries, network isolation,
resource limits and relay behavior need real executor/sish evidence. Existing
DNS/TLS fixtures are not proof of live addressing. SPEC-0079 is frozen with
this acceptance. Implementation is split into CHG-0170 (store), CHG-0171
(serving/access), CHG-0172 (static delivery), CHG-0173 (dynamic admission)
and CHG-0174 (acceptance). No Haloy dependency, new database or queue.
Docker operations remain prohibited by the current owner instruction.

## Amendment 2026-10-26 — Go first dynamic profile

D11's first dynamic profile is `go-http`, not `python-http`: the owner replaced
the Python runtime with the project's existing Go toolchain. The published
release carries a workspace-built static Linux ELF at the fixed name `app`;
the admitted PID1 executes `/work/app`, which listens on `127.0.0.1:8080`.
Dropping the interpreter removes a second runtime class and its image/env
surface for zero contract gain; all other D11 boundaries are unchanged.
SPEC-0079 carries the same amendment.
