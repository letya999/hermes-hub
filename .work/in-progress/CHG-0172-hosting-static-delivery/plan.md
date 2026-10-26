# CHG-0172: Agent workflow, exposure and static recovery (M18 #147, H3)

Chain: ADR-0079 D1–D3/D5/D11 → SPEC-0077 R1/R2/R5/R8/R9 → plan → state.yaml.
Requires CHG-0170/0171 and exposure/DNS code from CHG-0166/0168.
Next: CHG-0173; release acceptance CHG-0174.

## Goal

Complete MVP-S wiring: build files in workspace → publish slot → return ready
URL; list/status/unpublish work; hosting/tunnel outlive Hermes. No success URL
until actual route readiness. Static delivery is useful but does not close #147.
Read accepted ADR-0079/frozen SPEC-0077, predecessor plans and Haloy review.

## Ordered slices (red then green; one commit each)

### H3.1 Agent and service contracts

Existing anchors: `internal/agenttools/exposure.go:exposureDispatch/registerExposure`,
`internal/agenttools/tools.go`, `tools_api.go`, `internal/identity/identity.go`.
New `internal/agenttools/hosting.go` and hosting private API client. Register
site_publish/unpublish/list/status consistently in MCP and dispatch schemas,
manifest/capability grants, feature filtering and reserved tool-name checks.
Use trusted envelope, not owner from input; no images/env/host/mount/hook fields.
Mode/profile/access/lifetime/expected revision/request id only per SPEC.
No generated raw URL in errors; link delivered once, status pending explicit.

ADD `TestSiteToolsBoundToPrincipal`: both MCP and direct dispatch reject foreign
owner or extra forbidden fields before API request; feature off hides/denies
all four tools. ADD `TestSitePublishReplayNoTokenLeak`: duplicate operation,
list/status and lost response do not recover raw token or mint another share.
ADD `TestSitePendingNeverReturnsReadyURL`: delayed route ack returns pending;
status changes only after revision-matching readiness.

### H3.2 Bounded delegation and shared endpoint reservation

Existing anchors: `internal/exposure/config.go:Config/Endpoint/PublicName/BindName`,
`service.go:Manager.Open/Close/Reap`, `sish.go` provider; inspect callers first.
Introduce trusted hosting slot namespace/cap reservation shared with manual
endpoints. Model cannot edit exposure YAML or choose target. Generated endpoint
targets only hosting data listener; control/Broker/gateway never valid. Keep
existing name-only generic exposure tools. No DNS API calls during publish.
Persist endpoint intent/revision/lease ownership; tunnel renewal bounded by
site expiry. A static site must survive agent process exit; current in-process
exposure manager alone is insufficient. Move/reuse provider behind independent
hosting reconciliation without silently changing unrelated manual exposure.

ADD `TestHostingManualEndpointCollision`: manual reserved name cannot be replaced
or deleted by hosting; shared cap cannot be exceeded. ADD
`TestHostingDelegationTargetDenied`: crafted target/zone/user fails before provider
open; approved data listener positive control. ADD `TestUnpublishFencesReconnect`:
pause Open/reconnect, commit unpublish, resume; no stale ready route and new
requests denied even while finalizer retries. Denied cleanup stays durable.

### H3.3 Opt-in stack, mounts and independent recovery

Existing anchors: feature registry in `internal/stack/config.go`, `compose.go:Compose`,
`doctor.go:Doctor`, `hermesconfig.go`, `tools.go`; locate actual registration
and managed runtime mounts before editing. New hosting service feature off by
default, dedicated per-user/env volume, private control auth, isolated data
listener, no agent access to store/control secrets. Build optional binary via
project build paths; never mount spaces into development service or expose
Docker socket to agent/web tier. No Docker commands during current owner ban.
Fail doctor if bindings/runtime prerequisites unavailable; static needs no
dynamic runtime. Use no new queue/DB. Startup validates store lock/receipts and
restores committed intent, never tombstones or expired sites.

ADD `TestHostingComposeOptInAndMountIsolation`: render only, two synthetic spaces
and dev/prod; correct unique volumes, access modes/networks, no control ingress
and no foreign/full-space mounts. ADD `TestSiteExpirySurvivesRestart`: fake clock,
real store/controller restart; valid static remains, expired/revoked not restored.
ADD `TestHostingShutdownIndependentOfHermes`: process/client lifecycle boundary,
not a fake assertion that Docker actually keeps running. Real end-to-end proof
is acceptance below. KEEP existing manual exposure and DNS contract tests.

## Acceptance and red team

A1/A2/A5/A6/A9/A10 plus A11-static. Code tests use temp state, httptest and scoped
fake sish; they prove wiring/revision logic only. Live: synthetic static page
with CSS/module/assets on actual wildcard host, verify TLS/Host, stop Hermes,
restart hosting, interrupt SSH, republish then revoke; no old bytes/new requests
after acknowledged revoke, no per-publish DNS mutations, manual names untouched.
Browser sibling-cookie check required. Execute live only under later concrete
deployment instruction; current Docker ban leaves that evidence pending.

## Done / exclusions

Document actual opt-in behavior/tools/operations when implemented. Package/race,
own coverage >=85%, just check and required runtime/image gates need honest
results. State can be implemented with pending live evidence, never done/accepted
from mocks. No dynamic code, arbitrary builds, custom domains, websocket or
dashboard. Full issue remains open; proceed to CHG-0173 and CHG-0174.

## Implementation record — 2026-10-26

Source implementation is present in this worktree. Current status and remaining
gates are recorded in state.yaml; offline contract evidence is centralized in
CHG-0174/evidence.md. Native runtime, browser and sish proof is not inferred.

The service entrypoint reuses `hubctl hosting`; the optional hosting-only Go
controller reuses ToolHub admission. Dynamic ingress is a bounded network-none
OCI-exec relay, with a helper-only volume and exact release volume subpath.
Shared fleet/zone metadata locks coordinate limits and address ownership; no
app content or secrets are shared through those coordination volumes.
