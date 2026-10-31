## Goal

Let each user host sites from their own space: build a page in the workspace,
publish it to a site slot and receive an artifact-style share link. Also support
one small stateless dynamic app profile that survives the chat.

M18 hosting track, dependent on exposure #145 and per-user DNS/TLS #146.
Owner accepted the researched architecture on 2026-10-26. This issue is now an
implementation plan, no longer an unscheduled idea. It remains open: accepting
the design does not implement hosting or establish live guarantees.

## Decision and research

- ADR-0081: Accepted. SPEC-0079: Frozen 2026-10-26.
- CHG-0169: completed design/research only, under `.work/done/`.
- Worktree `hermes-hub-m18-hosting`, branch `feat/m18-exposure-hosting`, based
  on `5c19f3e` from `feat/m18-exposure-dns`; predecessor work is not assumed merged.
- Research covers local contracts, Context7, ready-made solutions and provider
  patterns. Haloy cloned and source-reviewed at
  `3cd1d500394a81532970dc4cdb57e02d77b2be62` (`v0.1.0-beta.73`).
- Use our Go hosting layer plus existing controller/ToolHub admission and
  sish/DNS. Do not adopt or fork Haloy: unchanged API cannot express mandatory
  pre-start isolation/resource controls; private-network discovery, serving
  behind sish and durable lifecycle fences require central changes. Borrow
  snapshot/hash/reconciliation/readiness patterns; no Haloy code/dependency added.
- Documents are currently local to the worktree; no commit/push is implied by
  this issue update. Canonical paths:
  `docs/adr/ADR-0081-user-scoped-site-hosting.md`,
  `specs/active/SPEC-0079-user-scoped-site-hosting.md`,
  `.work/done/CHG-0169-site-hosting-design/{research,ecosystem-research,haloy-review}.md`.

## Accepted scope

- Opt-in per-user/env Go hosting service, independent of Hermes/chat lifetime;
  separate private control and public data listeners. Exact allocated Host only.
- Immutable confined workspace snapshots into a dedicated hosting volume,
  logical roots `spaces/<user>/sites/`. Storage reservations include staging,
  replacement overlap and retired releases; no live full-space web mount.
- Agent `site_publish`, `site_unpublish`, `site_list`, `site_status`; trusted
  owner/env/zone, expected revisions, operation replay and collision checks.
  No caller-selected target/image/mount/shell/env or deploy hooks.
- sish remains TLS edge; reuse the per-user wildcard and exposure names,
  no DNS calls per publish. Shared bounded reservation with manual endpoints;
  persistent tunnel intent/recovery independent of agent process.
- Artifact links default to revocable expiring opaque bearer access, hash-only
  at rest, fragment bootstrap into host-only Secure HttpOnly session; epoch
  checked before every asset/range/conditional read. Explicit public mode and
  private mode; no hub auth cookies on site origins.
- One admitted stateless `python-http` profile after working static delivery:
  pinned image, non-root, read-only root/release, cap-drop/NNP/seccomp,
  CPU/mem/PID/tmpfs bounds, owned private network/restricted ingress relay,
  default no egress/secrets. Explicit runsc reference or verified Kata;
  missing secure runtime fails closed, never implicit runc.
- Secrets only through an explicitly authorized Broker contract. Initial
  profile has no secret delivery; unsupported secret-env requests fail closed.
- Durable revision/expiry/restart/tombstone/finalizer state. Revoke first,
  cleanup scoped resources to verified zero; stale events cannot revive or
  delete a newer revision. No daemon-wide image/volume pruning.
- Per-user/site storage, count, compute, rate, concurrency, body/time/bandwidth
  bounds and allowlisted bounded durable audit, including admission pressure.
  Exact defaults and A1–A11 acceptance are frozen in SPEC-0079.

## Implementation stages and states

Each stage has its own ordered red-first slices, existing code anchors/new
files, regression scenarios, dependencies, acceptance and `state.yaml` under
`.work/in-progress/`. All were `implementation_status: not_started` at allocation; H1–H4 are now in progress and H5 has begun offline verification. No live/runtime/browser acceptance is claimed.

- [ ] H1 / CHG-0170-hosting-store: identity/policy, single-writer slot store,
  confined releases, quotas, operation replay and crash recovery.
- [ ] H2 / CHG-0171-hosting-web-access: static serving, artifact access,
  listener/origin/cookie/CSP boundary, HTTP bounds and audit pressure.
- [ ] H3 / CHG-0172-hosting-static-delivery: agent/stack/exposure wiring,
  durable tunnel recovery; complete static MVP-S, not completion of this issue.
- [ ] H4 / CHG-0173-hosting-dynamic: persistent admitted profile, inspect/canary,
  restricted relay, bounded restarts/expiry and finalizers; dynamic MVP-D.
- [ ] H5 / CHG-0174-hosting-acceptance: actual behavior docs, quality gates,
  A1–A11 code plus real Linux/runtime/browser/sish evidence.

## Implementation progress (local branch, not yet accepted)

The worktree now contains the owner-bound store/snapshot/quota journal, static
share gate and private API, agent/catalog/governance/stack wiring, and a
hosting-only existing Go controller profile for a network-none Python workload.
The fixed loopback relay runs through the trusted helper; no app destination,
image, volume or credentials are caller-selected. Docker remains untouched.
Selected unit/race and HTTP/controller contract tests are being run; quality
gates are still in progress, so stage boxes deliberately stay unchecked.
CHG-0175 separately hardens a pre-existing coverage parser panic and temporary
profile handling without lowering coverage floors.

## Closure criteria and current limits

- [ ] Alice cannot publish/replace/read Bob's site or resources; source races
  cannot copy foreign files; quota/revision races and crash recovery proven.
- [ ] Actual static and dynamic URLs work with correct wildcard TLS after
  chat ends; revoke/expiry and restart/reconnect cannot resurrect access.
- [ ] Real runtime canaries prove resource and file/network/credential isolation;
  actual browser verifies bootstrap/assets/sibling-origin restrictions.
- [ ] Required checks pass, own Go statement coverage >=85%, current docs and
  all CHG states match evidence. Mock tests do not prove integration.

Current instruction: **no Docker operations**. Runtime, image and deployment
acceptance therefore remain not run until a later concrete deployment instruction.
Predecessor sish/DNS live evidence is also pending. Prior full `just check`
was not green: coverage aggregation panicked after package tests, and later
config-check requires Docker. Docs-only acceptance does not waive those gates.

Deferred: persistent writable app disk, arbitrary image/Dockerfile/build hooks,
dependency installers, additional languages, custom domains, visitor OIDC,
dashboard, HA/autoscale, WebSocket/SSE/long streaming. Static-only delivery
cannot close this issue.
