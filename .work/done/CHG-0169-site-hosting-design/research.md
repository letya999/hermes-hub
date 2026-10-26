# Research: user-scoped site hosting (#147, M18)

Date: 2026-10-26. Base: `5c19f3e`; branch `feat/m18-exposure-hosting`.
Design evidence only; no Docker, provider mutations or live hosting probes.
Issue scope comes from the owner's task; the remote issue was not fetched.

Follow-up requested by the owner:
[ecosystem-research.md](ecosystem-research.md) records Context7, ready-made
GitHub solutions, provider architecture and the requested community searches.
Its MVP sequence and secure-runtime recommendation refine the initial findings
below; ADR-0079 and SPEC-0077 have been reconciled with that follow-up.

## Existing contracts and observed gaps

Read first: ADR-0077/SPEC-0075, CHG-0165/0166; ADR-0078/SPEC-0076,
CHG-0167/0168. Then README, docs/index, architecture, cell-proxy and code.

- `internal/exposure/config.go`: reviewed endpoint names, max 16 declared,
  default 4 live/max 8, default lifetime 8h/max 7d. Host comes from
  `PublicName`/`BindName`; targets are reviewed, never chosen by the agent.
- `internal/agenttools/exposure.go:openExposure` creates the manager inside
  the agent runtime. `internal/exposure/service.go` holds live state in RAM;
  `sish.go` dials SSH directly and splices TCP. Thus chat shutdown and process
  restart cannot currently preserve/reconcile hosting tunnels.
- CHG-0166 explicitly records deviation from admitted tunnel workloads and
  deferred idle reaping. Neither it nor CHG-0168 proves real sish deployment.
  DNS/wildcard TLS are consumed contracts, not live evidence in this task.
- `internal/dns/config.go` confines records to a base zone; per-user binding
  is an additional caller responsibility. Hosting must derive owner/zone from
  trusted identity and reviewed bindings, never accept an arbitrary FQDN.
- `internal/toolhub/cell_lifecycle.go:createCell` already performs pre/post
  inspect and canary checks. CLI cells and `api.WorkloadBudget` are ephemeral,
  idle-reaped/in-memory; directly reusing their job lifecycle would kill sites.
- No hosting slot store, persistent lifecycle, HTTP access gate or writable
  filesystem quota is established by those primitives.

## Dynamic backend fork

| Candidate | Boundary and compatibility | Cost / failure mode | Recommendation |
|---|---|---|---|
| Subprocess inside Hermes runtime | Shares its filesystem/network authority; CPU timeout is not isolation | Dies with runtime; inherits secrets unless meticulously scrubbed | Reject |
| ToolHub-style admitted workload per site | Existing digest/profile/receipt/finalizer pattern; ordinary HTTP apps | Requires durable site lifecycle and isolated ingress; containers share host kernel | Select; separate from job cells |
| Deno permission sandbox | Deny-by-default filesystem/network/env permissions; JS/TS compatibility | Subprocess permissions can escape the Deno sandbox; still needs OS budgets and independent supervision | Optional later profile inside workload |
| Wasmtime/WASI | Capability-based guest interface | Requires WASI-compatible apps and HTTP host integration; not a transparent Python/Node host | Defer; no new runtime dependency |

Evidence checked 2026-10-26:
[Docker security](https://docs.docker.com/engine/security/),
[resource constraints](https://docs.docker.com/engine/containers/resource_constraints/),
[Deno security](https://docs.deno.com/runtime/fundamentals/security/),
[Wasmtime security](https://docs.wasmtime.dev/security.html).
Inference: reuse the existing admission boundary, not its ephemeral lifecycle.
Follow-up: this rejects an unconfined subprocess, not all process-based
sandboxes. nsjail/Sandlock are evaluated in ecosystem-research.md. Dynamic
hosting now proposes explicit runsc/Kata; existing CLI's implicit runc is not
the hosting isolation tier.
This remains an owner deployment with shared-kernel residual risk, not a
hostile public compute platform. Unsupported isolation/resource controls deny
dynamic publish; there is no subprocess fallback.

## Serving / exposure fork

| Option | Result |
|---|---|
| Each app directly exposed as an endpoint | Few pieces, but app can bypass share validation, HTTP limits and serve audit; static still needs a server |
| Separate per-user hub-owned Go hosting service behind named exposure endpoints | One gate for static and dynamic, independent of Hermes lifetime; private control listener and per-site isolated upstream relays |
| Multi-user central daemon mounting all spaces | Broad blast radius; unnecessary authority for this deployment |

Select per-user hosting service. Each admitted slot reserves one endpoint in
the existing exposure namespace; its target is a generated hosting listener,
not an agent-supplied port. No wildcard tunnel or DNS write per publication.
Exposure and hosting reserve the same name/cap atomically; a pre-existing
manual endpoint is a conflict, never overwritten. Hosting owns durable tunnel
intent and reopens bounded tunnel leases until the site expires. This adds a
private web tier behind sish, not TLS termination or a hub public control proxy.
It is an explicit scoped addition to ADR-0077 D1's no-public-proxy wording.

[sish docs](https://docs.ssi.sh/) document HTTP forwarding, but do not prove
this deployment preserves original Host headers. Exact Host routing, unknown
host rejection, HTTP-to-HTTPS behavior and relay logging need live acceptance.
If Host is rewritten, use a per-slot target listener bound to the same expected
hostname; never trust client-supplied forwarded identity headers.

## Public / authenticated / artifact access

Public sites require explicit public intent; hidden is not authenticated.
Default is an expiring bearer share link, reusable for HTML assets and revisits
(unlike the existing one-shot human-action links). Select opaque random tokens
with stored hashes, not self-contained signed URLs: durable slot state already
exists and revocation is immediate without a signing-key distribution scheme.
Token is scoped to owner/site/revision/share epoch and expires in 24h by
default, at most 7d and never after site expiry. URL fragment bootstrap keeps
the token out of the initial request/referrer; hub-owned bootstrap exchanges it
by POST for a short host-only HttpOnly Secure session cookie, then clears the
fragment before loading site code. Never put raw links in audit/list output.
Link access is bearer authorization, not visitor identity. Full identity-based
visitor login is deferred; private/offline slots have no external route.

Sibling sites are distinct origins but can be same-site. Hosting credentials
use `__Host-` cookies; no parent-domain cookies or hub authentication on site
origins. Strip the hosting session cookie before proxying to app; reject
parent-domain Set-Cookie from apps and bound outbound headers. Require exact
Origin for cookie-authenticated unsafe methods; do not rely on SameSite alone.
Prefer a hosting base domain separate from authenticated hub applications.
[Cookie guidance](https://developer.mozilla.org/en-US/docs/Web/Security/Practical_implementation_guides/Cookies)
supports host-only cookie scoping; cross-origin trust policy is our proposal.

## Storage fork

`spaces/<user>/sites/` is the logical namespace. Select dedicated per-user,
per-env hosting volume for committed releases/control state: Hermes writes
only build outputs in workspace; publisher copies through confined APIs.
Web tier sees only immutable releases read-only; dynamic workload sees exactly
its release read-only and a size-limited tmpfs. Neither sees the full space.
Bind mounts match existing space ergonomics but expose mutable host paths and
make safe snapshots harder; a named volume alone does **not** enforce quota.
[Docker volumes](https://docs.docker.com/engine/storage/volumes/) describes
persistence, not a portable per-volume byte limit.

Count bytes/files during copy under a cross-process per-owner quota reservation;
count staging, active, retired releases and bounded metadata. Reject symlinks,
special files, traversal, secret/config dotfiles; copy regular files rather
than preserve hardlinks. Source mutation must fail or yield a validated immutable
snapshot, never expose live workspace. Confine opens with
[Go os.Root](https://go.dev/blog/osroot), not prefix checks or check-then-open.
`net/http` static serving requires a wrapper: prevent directory listings and
dotfile access, bound requests, allow only GET/HEAD.
[Go HTTP reference](https://pkg.go.dev/net/http#FileServer).

Writable persistent app storage needs a separate filesystem-quota contract;
defer it explicitly. Stateless HTTP apps persist as services across chats and
host restarts, but tmpfs data does not. No claim of portable disk enforcement
for an arbitrary writable Docker volume.

## Proposed bounds and failure policy

Defaults are proposed product choices, not measured capacity: 4 sites/user,
2 dynamic, 512 MiB committed+staged bytes, 10,000 files/user, 128 MiB/release;
1 CPU/256 MiB RAM/64 PIDs/32 MiB tmpfs per app; 7d site lifetime (max 30d);
3 failure restarts/10min, exponential 1–30s backoff, no restart after expiry.
Owner aggregate cap: 2 CPU/512 MiB app RAM, in addition to operator fleet cap.
Admission reserves the minimum of hosting and exposure capacity.

Publish: 6/min/user (burst 2), serve: 100 requests/s/user and 20/site
(burst 100/20), 32 in-flight/user and 8/site, 1 MiB request body, 30s request
deadline and 10 MiB/s/user response budget. HTTP only initially; WebSocket/SSE,
streaming uploads and app-managed TLS deferred. Limits live in trusted operator
policy; users may narrow them. Startup validates cross-field consistency.

Audit publish/unpublish/deny and every admitted/completed serve with safe IDs,
status, bytes, duration only; no path/query/headers/body/IP/URL fragments.
Reserve bounded audit capacity before serving; backpressure returns 503 rather
than silently dropping required events. Retention and disk bounds are part of
the operational budget, not unbounded JSONL growth.

## Design outcome / remaining evidence

Acceptance update 2026-10-26: following the research below and pinned Haloy
review, owner accepted the direction. ADR-0079 is Accepted, SPEC-0077 Frozen;
implementation plans/states are CHG-0170–0174. Historical proposal wording below
records the research stage, not the current acceptance status. No implementation
or live guarantee is claimed by completing this design CHG.

Propose ADR-0079 and draft SPEC-0077; freeze only after owner acceptance.
Implementation stages: slot/snapshot/quota → static + access gate →
agent/exposure reconciliation (complete static MVP) → admitted dynamic
lifecycle → live isolation acceptance. One writer/replica and one initial
dynamic profile keep the first delivery bounded; dynamic still completes #147.
No Docker actions in this design pass. Blocking future checks: sish Host/name
claims/logging, network canaries, hard limits on the actual Linux executor,
crash recovery and isolation. No integration success inferred from fake tests.
