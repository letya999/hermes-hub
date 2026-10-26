# CHG-0174: Hosting acceptance and operating contract (M18 #147, H5)

Chain: ADR-0081 → SPEC-0079 R1–R10/A1–A11 → plan → state.yaml.
Requires CHG-0170–0173; predecessor live exposure/DNS evidence must be checked.

## Goal / prerequisites

Record real static and dynamic hosting acceptance and document actual behavior.
Only this end-to-end gate can justify closing #147. Current owner instruction
prohibits Docker operations. No deployments, real account operations, issue
closure or evidence claims are authorized by this plan alone. Later concrete
deployment instruction is needed for deployment-dependent checks; missing
inputs remain not_run, not a mock-based substitute.

Read docs/validation.md, docs/testing.md, all predecessor state.yaml, accepted
ADR/frozen SPEC and design research. Use synthetic owners/content/secrets only;
diagnostic artifacts stay under ignored .work/tmp after git check-ignore.

## Ordered slices

### H5.1 Code regression, gates and current documentation

Verify R→A→test/evidence mapping in predecessor states; locate each real test
and ensure exact boundaries from SPEC. KEEP predecessor exposure/DNS tests.
No redundant test for prose-only edits; ADD live probes only for assertions
unprovable by real Go store/handler/controller tests. Update
`docs/architecture.md`, `operations.md`, `threat-model.md`, `tools.md`,
`spaces.md`, new `docs/services/hosting.md` and docs/index.md for deployed
opt-in contracts, failure modes, renewal/unpublish and recovery procedure.
Do not put changing task progress or raw acceptance journals in current docs.

Run just check (own Go statements >=85%; per-package floor maintained), relevant
image/runtime CI gates per AGENTS.md, just security only if dependencies changed.
Current no-Docker restriction excludes config-check and Docker gates; preserve
that result honestly. Existing coverage parser failure is not waived or green;
if still present, repair under a separately scoped change before claiming gate.

### H5.2 Static, shares, browser and exposure acceptance

On an authorized isolated dev stack with actual sish/domain/wildcard cert,
synthetic alice/bob publish the same site label with different assets. Verify
exact FQDN, TLS and original Host, no DNS calls per publish, manual endpoint
collision denied and other owner's bytes unchanged. HTML/CSS/JS modules and
relative assets load after fragment bootstrap; raw token absent from server
logs/list/status, cookie host-only and hub auth absent at site origin.
Sibling site cannot set/use another site's hosting session or submit unsafe
cross-origin calls. Explicit public and private modes have distinct outcomes.
Revoke/republish invalidates range/ETag/session access. Browser CSP denies
workers/undeclared connections without breaking approved assets.

Stop Hermes, restart hosting, drop/reconnect SSH and interrupt a publish;
old or new complete revision only, no resurrection after unpublish/expiry.
Evidence A1–A6/A9–A11: safe metadata/status and checksums, no content/tokens/IP.
Each assertion records date, actual revision/runtime and observed result.

### H5.3 Dynamic native controls and closure audit

Use admitted go-http app under explicit runsc or verified Kata with no
grants. Attempt file/network/credential cross-boundary access using synthetic
sentinels; expected denial plus foreign state unchanged. Exhaust CPU, memory,
PIDs and tmpfs/inodes; verify exact configured limits without host compromise.
Inspect runtime/profile before/after start, log driver none. Unknown Host and
relay target injection must not reach workload/control. Kill app repeatedly,
restart controller, advance actual test lease: bounded restart and expiry
survive recovery. Revoke while serving and interrupt finalizers; access stops
within contract and owned resources eventually zero, other projects untouched.
These are A7/A8 plus dynamic A9–A11, not inferred from inspect fixtures alone.

Close only when all A1–A11 have appropriate code/live evidence, gates are green
and documentation matches running behavior. Optional Broker env is not shipped
until separately implemented/authorized; unsupported secret requests fail closed.
Any required failed/unavailable acceptance keeps issue open and state in_progress.
Update issue evidence by explicit owner instruction/session authorization,
move completed CHGs to done, repair links and indexes. Never close for MVP-S only.

## Out of scope

No HA/SLA/load benchmark promises, OIDC, dashboard, autoscale, extra languages,
websocket/streaming, writable persistent disk, dependency installers, arbitrary
Dockerfiles/images or provider setup. No Docker action while owner ban remains.

## Implementation record — 2026-10-26

Source implementation is present in this worktree. Current status and remaining
gates are recorded in state.yaml; offline contract evidence is centralized in
CHG-0174/evidence.md. Native runtime, browser and sish proof is not inferred.

The service entrypoint reuses `hubctl hosting`; the optional hosting-only Go
controller reuses ToolHub admission. Dynamic ingress is a bounded network-none
OCI-exec relay, with a helper-only volume and exact release volume subpath.
Shared fleet/zone metadata locks coordinate limits and address ownership; no
app content or secrets are shared through those coordination volumes.
