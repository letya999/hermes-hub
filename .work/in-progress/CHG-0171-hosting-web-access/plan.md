# CHG-0171: Static serving, artifact access and HTTP bounds (M18 #147, H2)

Chain: ADR-0081 D2/D7–D9 → SPEC-0079 R5/R6/R9/R10 → plan → state.yaml.
Previous: CHG-0170 (required). Next: CHG-0172. Evidence:
[Haloy review](../../done/CHG-0169-site-hosting-design/haloy-review.md).

## Goal / prerequisites

Serve only committed immutable static bytes behind a revocable artifact gate,
with separate authenticated control and public data listeners. Read ADR/SPEC,
CHG-0170 store contract, docs/testing.md and existing HTTP auth patterns before
coding. Go stdlib; no framework, Haloy, second TLS manager or Docker operations.
This stage alone is not MVP-S: agent/stack/exposure wiring is CHG-0172.

## Ordered slices (one commit each; red tests first)

### H2.1 Exact-host serving and private control API

New `internal/hosting/http.go`, `control.go`, `cmd/hosting/main.go`.
Use CHG-0170 Store; follow private/public listener separation in
`internal/communication`, inspect actual constructors before reuse. Bind
owner/env outside request bodies, limit body before JSON decode, reject unknown
fields/trailing input. No control routes on data listener. Require exact
allocated Host; malformed/unknown host gives 404 before file/upstream lookup.
Ignore client forwarded headers for identity/routing; sish remains TLS edge.
Static GET/HEAD only; no listing, dotfiles, symlink/special access, open redirects
or CONNECT. Configure explicit time/header/body/response limits from R9.

ADD `TestUnknownHostNoRead`: synthetic other-host request with valid local site
and sentinel; 404 and zero store-open/upstream calls. ADD
`TestHostingListenerSeparation`: every control path on public listener fails;
control missing/wrong token, owner substitution and oversized/trailing JSON
fail before mutation. ADD `TestStaticHTTPContract`: multi-asset index, HEAD,
MIME, traversal/dotfile/directory/unsupported method; exact expected bytes/status.

### H2.2 Share bootstrap, cookie epoch and browser boundary

New `access.go` and hub-owned bootstrap asset. Token >=256 random bits; hash
only at rest. `/#<token>` loads no app assets before hub bootstrap POST exchange
and fragment clear. No token in URLs/logs/redirects/server errors or list/status.
Use host-only Secure HttpOnly __Host- cookie, bounded expiry and rotation
epoch. Replay publish never remints/reveals token. Validate before range/304,
not just first HTML request. Public explicit; private not externally routable.
Set enforced platform CSP/no-referrer/cache rules; never reuse hub auth cookies.
Invalid auth generic 404. Gate exceptions (bootstrap/exchange) must not expose
app bytes. Only same Origin can exchange or issue cookie-authenticated unsafe
requests; validate browser behavior at CHG-0174, not through httptest claims.

ADD `TestShareBootstrapAndEpoch`: wrong/right token, hashed persisted state,
cookie flags, fake-clock expiry, republish/unpublish invalidates old session;
no asset returned before authorization. ADD `TestServeRevokedShareRangeDenied`:
valid ETag/range followed by revoke, then conditional/range requests; 404 without
bytes or 304. ADD `TestSiblingOriginCannotExchange`: valid token/session but
different sibling Origin denied, same-origin positive control. Synthetic
token sentinels absent from journal/list/status/errors and recovered state.

### H2.3 Rate, concurrency, bandwidth and bounded durable audit

New `limits.go`; reuse hosting journal reservations from CHG-0170. Enforce
per-user and per-site admission before file read; reserve completion capacity
before dispatch. Bound in-flight, headers, request/response buffers/deadline
and bandwidth during transfer, including HEAD/range/denied requests. Cancellation
releases reservations/in-flight tokens; no unbounded per-client maps.

ADD `TestHostingServeBounds`: fake clock, exact burst and +1 ->429 Retry-After;
body +1 ->413; stalled backend/deadline ->504; audit capacity ->503 before
read/dispatch. ADD `TestServeCancellationReleasesCapacity`: blocked transfers
cancelled, subsequent valid request succeeds with no leaked reservation.
ADD `TestHostingAuditAllowlist`: injected secrets in path/header/body/errors;
only R10 fields persist. Missing audit capacity must never fall back to logging.

## Acceptance / red team / done

A5/A6/A10 code boundaries: real handlers/temp store, httptest server/client,
fake clock and instrumented read/dispatch only where absence of I/O matters.
KEEP existing unrelated HTTP/exposure tests. All new cases are planned ADD.
Browser module/assets/cookie policy acceptance remains CHG-0174; do not call
httptest browser evidence. Run race/package checks, just check subject to Docker
restriction and own coverage >=85%; report unavailable gates. No production
publication or external sending. Mark implemented/done only with actual evidence;
shared #147 remains open until static wiring plus dynamic/live acceptance.

## Implementation record — 2026-10-26

Source implementation is present in this worktree. Current status and remaining
gates are recorded in state.yaml; offline contract evidence is centralized in
CHG-0174/evidence.md. Native runtime, browser and sish proof is not inferred.

The service entrypoint reuses `hubctl hosting`; the optional hosting-only Go
controller reuses ToolHub admission. Dynamic ingress is a bounded network-none
OCI-exec relay, with a helper-only volume and exact release volume subpath.
Shared fleet/zone metadata locks coordinate limits and address ownership; no
app content or secrets are shared through those coordination volumes.
