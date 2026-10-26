---
description: Frozen acceptance contract for user-scoped static and dynamic sites, isolated workloads, bounded storage, lifecycle and artifact share links.
last_verified: 2026-10-26
---
# SPEC-0077: User-scoped site hosting

Frozen: 2026-10-26. Decision: ADR-0079 Accepted.
Amended 2026-10-26: the MVP-D profile is `go-http` — a workspace-built static
Linux ELF at `app` run on `127.0.0.1:8080` — per the ADR-0079 amendment;
`python-http` wording below is superseded, all other requirements unchanged.
Design: CHG-0169; implementation: CHG-0170–0174; issue #147 (M18).
Requirements below define the accepted target, not current behavior.

Delivery order: MVP-S is complete static hosting with agent/stack/exposure
wiring and recovery; MVP-D adds one stateless `python-http` profile with a
fixed stdlib runtime, entrypoint convention and private port. #147 is not done
at MVP-S. Unsupported dynamic profiles/builds/secret delivery return a named
error rather than silently weakening their execution boundary. Source:
[ecosystem research](../../.work/done/CHG-0169-site-hosting-design/ecosystem-research.md)
and [pinned Haloy review](../../.work/done/CHG-0169-site-hosting-design/haloy-review.md).
Use the existing Go controller/admission plus ready secure runtime components;
no Haloy deployment stack or lifecycle fork is selected.

## Requirements

- **R1 Identity / delegation.** Opt-in hosting derives owner, env and zone
  from authenticated identity and reviewed binding. All operations, paths,
  quota records, workload labels and endpoint reservations include that
  binding. Reject cross-user requests before filesystem/provider access.
  Site labels follow the existing exposure endpoint grammar, additionally
  rejecting trailing hyphens and reserved control labels. No custom FQDN.
  A manual exposure name collision is `conflict`; never replace it.
- **R2 Agent contract.** `site_publish` takes site, workspace-relative source,
  mode (`static`/`dynamic`), access (`link` default / `public` / `private`),
  expected revision (zero means create), request id and optional approved
  profile/lifetime within policy. No caller-supplied owner/target/image/mount/
  arbitrary env. Result: site, revision, state, expiry and URL only when ready.
  An explicitly requested link is delivered once; duplicate request id returns
  the same operation/revision without minting another token, exposing no raw
  token after initial delivery. Lost link recovery is an explicit republish
  with a new revision; it revokes old access. `site_list`/`site_status` return
  safe metadata and canonical origin, never link tokens or provider handles.
  `site_unpublish(site, expected_revision, request_id)` is idempotent for an
  already removed revision; stale revisions cannot remove a newer one.
  Reusing a request id with different input returns `conflict`. An operation
  may return pending with an operation id; readiness is observable in status,
  never inferred from a successful workload create call.
- **R3 Snapshot / publication.** Build remains in workspace. Root-confined
  copy rejects absolute/escaping paths, symlinks, devices/FIFOs/sockets and
  hidden/config/credential files; explicit safe asset filenames are allowed
  by trusted profile, never by disabling confinement. Copy regular bytes,
  not hardlinks. Enforce byte/file limits while copying and reject source
  races that invalidate the snapshot. Atomically promote a complete immutable
  release only after authorization, quota, audit and route/workload readiness.
  Failure leaves previous published revision available; staged/new objects
  are finalized. No partial release is served.
  No install/build/post-publish hook executes in publisher, web tier or Hermes.
  Required executable builds use an admitted build/CLI boundary, independently
  limited and without inherited secrets. MVP accepts prebuilt files; it does
  not add an arbitrary dependency installer or execute user Dockerfiles.
- **R4 Storage.** Dedicated volume per user/env; logical namespace under
  `spaces/<user>/sites/`. Publisher has only selected workspace read access
  plus hosting-store write access; web tier gets committed releases read-only.
  App gets only its release read-only and bounded tmpfs. Host control state
  and other releases are never mounted into an app. Cross-process atomic
  reservations include staging, committed/retired releases and metadata.
  Crash recovery reconciles reservations with actual files before admission;
  disk-full fails without damaging previous release. Persistent writable
  app data is unsupported in this version, not silently unbounded.
- **R5 Serving / addressing.** Hosting survives Hermes shutdown. Reuse bound
  `PublicName`/`BindName` and existing DNS wildcard/TLS, with no per-publish DNS
  call. Each external slot consumes one exposure reservation. A trusted
  generated endpoint targets only hosting data listener, never control API,
  gateway, Broker or arbitrary address. Unknown/malformed Host returns 404
  without upstream/filesystem access; strip untrusted forwarded headers.
  Static GET/HEAD only, disable listings/dotfiles/traversal; dynamic routes
  only to the admitted revision through a per-site restricted relay.
  Redirects never use untrusted Host; no generic forward proxy or HTTP CONNECT.
- **R6 Access / browser.** Link mode implements ADR-0079 D7: >=256-bit opaque
  secret, hash at rest, owner/site/revision/epoch/expiry binding, fragment
  bootstrap with POST exchange, host-only Secure HttpOnly `__Host-` cookie,
  no-referrer and no-store for bootstrap/access responses. Clear fragment
  before loading site content; bootstrap is hub-owned, never site-provided.
  Verify session epoch/expiry per request and before cache/304/range responses.
  Strip hosting cookies upstream; reject parent-domain Set-Cookie and bound
  headers. Exact Origin required for session-authenticated unsafe methods;
  do not trust SameSite for sibling sites. Public needs explicit intent;
  private creates no external exposure. Missing/invalid/expired access gets
  generic 404; no reusable public cache for protected content. Site code is
  untrusted; no hub auth APIs or parent-domain authentication on site origins.
  Enforce platform CSP: `object-src 'none'`, `base-uri 'none'`,
  `worker-src 'none'`, same-origin form actions and trusted resource allowlists.
  Static browser network connections default to none; dynamic permits its own
  origin only. App code cannot broaden that policy. Module loading, relative
  assets and share-cookie compatibility require browser acceptance; do not
  claim opaque-origin iframe compatibility without testing it. Revocation
  prevents new server reads and cannot erase downloaded/cached local copies.
- **R7 Dynamic admission / credentials.** Only approved digest-pinned profiles
  and structured entrypoints; no shell-string execution. Verified non-root,
  read-only root/release, no-new-privileges, dropped caps, seccomp, CPU/RAM/PID/
  tmpfs limits and owned network. No Docker socket, host PID/net, hub networks,
  full workspace/space mounts or inherited credentials. Default egress none;
  reachability includes only the authenticated restricted ingress relay.
  Explicit hosting runtime is `runsc` (reference) or verified `kata`; missing
  runtime or runtime-specific isolation/resource canary fails closed. Never
  inherit an empty/default runc tier from the existing CLI configuration.
  Missing inspect/canary proof or required control returns `unavailable`,
  never weaker execution. Broker-only opt-in secret grants bind site/revision,
  lease and delivery contract; revocation stops app and releases secrets.
  App output and request-derived errors are never echoed into diagnostic logs.
  Disable workload stdout/stderr persistence by default, including Docker's
  log driver; opt-in diagnostics require a separate bounded safe-field policy.
- **R8 Lifecycle / concurrency.** Persist desired revision, operation id,
  expiry, restart counters, endpoint ownership, cleanup progress and tombstone.
  Reconcile before restoring on restart. Bounded exposure lease renewal and app
  restarts do not renew site lifetime. Stop at expiry; returning traffic never
  renews a lease. Unpublish commits access revocation before cleanup; after
  acknowledgement no new serve is admitted, existing work is cancelled/drained
  within 30s. Retry finalizers to verified zero; retained metadata stays charged
  until removed. Serialize competing publish/unpublish/reaper/reconnect across
  processes and fence every route swap by revision/epoch. Replacement reserves
  old+new storage and compute; insufficient headroom rejects without downtime.
- **R9 Bounds.** Operator policy is authoritative and strictly parsed; user
  can only narrow. Defaults below apply unless operator narrows/explicitly
  changes within validated deployment capacity. Reject inconsistent policies.
  Enforce before expensive work, include aggregate and fleet workload limits,
  and enforce one writer/replica per user/env via an exclusive store lock.
  CLI/agent mutations go through the private service API; a second writer
  fails before any mutation. No multi-replica distributed counters in MVP.
  Cross-process fencing remains necessary for startup/recovery. Quota errors are
  `quota_exceeded`, revision/name races `conflict`, access failures `denied`,
  absent resources `not_found`, failed dependencies `unavailable`; sanitize
  details. Serve rate returns 429 + Retry-After; body overflow 413, deadline
  504, audit pressure 503. No provider response text or secret-bearing keys.
- **R10 Audit.** Durable allowlisted publish/unpublish/expire/restart/deny
  events plus serve admission/completion with operation id, owner/site/revision,
  status, byte counts and duration. Never URL/path/query/fragment/IP/headers/
  bodies/app stdout/credential values or dictionary keys. Reserve bounded
  journal capacity before publication/serve; unavailable capacity fails closed.
  Interrupted admissions get a recovery completion event. Default audit budget
  64 MiB/user and retention 7d, charged separately from the 512 MiB site quota;
  delete only expired records, backpressure if non-expired records fill budget.
  Bound metadata/logs/tmpfs too; process memory and HTTP limits are not disk quotas.

## Contract defaults

| Budget | Default / ceiling |
|---|---|
| Sites / dynamic sites per user | 4 / 2; effective external count also bounded by exposure policy |
| Stored bytes / files per user | 512 MiB / 10,000 including staging and retired releases |
| Single release | 128 MiB; replacement overlap counts |
| App CPU / RAM / PIDs / tmpfs | 1 CPU / 256 MiB / 64 / 32 MiB |
| User aggregate app CPU / RAM | 2 CPU / 512 MiB; include replacement overlap |
| Site lifetime | 7d default / 30d maximum |
| Link expiry | 24h default / 7d maximum and <= site expiry |
| Session expiry | 1h maximum and <= link/site expiry |
| Restart | never or on-failure; default 3 attempts/10min; backoff 1–30s |
| Publish rate | 6/min/user, burst 2 |
| Serve rate | 100/s/user, 20/s/site; bursts 100/20 |
| In-flight / request body / deadline | 32/user, 8/site / 1 MiB / 30s |
| Response bandwidth | 10 MiB/s/user; bounded buffering and headers <=32 KiB |
| Cleanup drain / reconcile retry | 30s / <=30s |
| Audit budget / retention | 64 MiB/user / 7d; admission backpressure |

Limits are contract defaults, not benchmarked SLOs. Hosting is HTTP request/response;
WebSocket/SSE/long streaming and persistent writable app data are out of scope.
Host remains one failure domain; no HA claim or uptime SLA.

## Acceptance design (not executed)

All cases below are ADD: hosting boundaries do not yet exist. Existing exposure
and DNS tests remain KEEP for their contracts, not hosting coverage. Real Go
store/handler/controller boundaries use temp dirs, fake clocks, httptest and
scoped fake executor; only kernel/sish/browser assertions need live surfaces.
Existing examples: `TestLoadRejects`, `TestManagerOpenUnknownDenied`,
`TestManagerOpenCapBusy`, `TestManagerReapLifetime` in `internal/exposure`,
and `TestRecordNameAndFQDN` in `internal/dns`. None asserts hosting behavior.

| ID / contract | Setup and action | Required observable result / distinguishing fault |
|---|---|---|
| A1 / R1–R2 | Alice publishes same label as Bob; then crafts Bob owner/path/zone and manual endpoint collision | Own slot independent; forged fields denied before I/O; collision conflict; Bob bytes/revision/route unchanged |
| A2 / R2–R4 | Publish v1; copy v2 fails midway; retry same id; submit stale expected revision | v1 still complete; no duplicate charge/token/workload; stale conflict; staging reclaimed |
| A3 / R3–R4 | Traversal, encoded traversal, Windows paths, symlink swap, hardlink, FIFO, hidden secret sentinel; source contains install/build hooks | No read/serve outside selected root; no hook executed by publisher/Hermes; regular copy independent of later mutation; invalid nodes fail before activation |
| A4 / R4,R9 | Byte/file quota at limit and +1; concurrent API requests reserve last slot; start second writer; replacement needs overlap; crash/disk full during commit | Exactly bounded admission; one reservation winner; second daemon denied before mutation; previous release intact; restart reconstructs charges, no orphan quota bypass |
| A5 / R5–R6 | Wrong Host/forwarded Host/control path; link valid/missing/expired/revoked; range/HEAD/304; multi-asset page | No wrong-site or control access; expired/revoked fails on every path; valid page and assets work; no protected cache bypass |
| A6 / R6 | Real browser: fragment exchange, module/relative assets, app sees headers, sibling app tries parent cookie/cross-origin POST, service worker and undeclared network request | Assets work; fragment cleared before scripts; hosting cookie not upstream; parent auth override and sibling writes denied; worker registration/network attempt blocked |
| A7 / R7 | Fake executor reports wrong digest/mount/caps/net/limits; Broker absent/revoked | Deny before serve; no weak fallback; revocation stops workload; no credential in safe receipts |
| A8 / R7,R9 | Real runsc/Kata executor: CPU/memory/PID/tmpfs/inode exhaustion and reads/dials toward sibling, host, metadata, hub, Broker; runtime missing | Limits enforced on selected runtime; forbidden reads/connects fail; unrelated site/control remains responsive; missing tier refuses without fallback; inspect receipts alone insufficient |
| A9 / R8 | End chat; restart hosting; crash during promotion/finalizer; race unpublish/reconnect/restart/expiry | Correct desired revision restored only while live; tombstoned/expired never resurrects; no admitted request after removal; cleanup <=30s drain then verified zero |
| A10 / R9–R10 | Boundary rates and burst+1, large body, slow app, audit budget full; injected secrets in path/errors/stdout | Correct 429/413/504/503; bounded queues/memory/logs; safe receipt fields only; denied serve not dispatched |
| A11 / R5,R8 | Real sish+DNS/TLS: publish static and app, stop Hermes, reconnect tunnel, replace/unpublish | Exact hostname/cert/Host routing; URLs work after chat; manual names untouched; revoked route never bypasses hosting gate; no DNS calls per publish |

Future implementation must run just check (own Go coverage >=85%), and security
for dependency changes. Real executor/browser/sish checks are separate evidence;
mock tests do not certify isolation or deployed integration. This design pass
performs no Docker operations. ADR acceptance freezes this contract; execution
and live acceptance are tracked separately in CHG-0170–0174. Plans or mocked
executor results cannot satisfy native runtime/browser/sish acceptance.
