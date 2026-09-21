# CHG-0032 — Credential Broker direct form and expired-link regeneration

## Problem

The connect flow required a channel-binding approval (`/credentials <id> <code>`
in the trusted chat) before the credential form opened. Users hit `denied`
repeatedly (unapproved or rotated browser session, stale CSRF), and a broker
request expiring after 15 minutes pinned the ToolHub onboarding forever because
`BrokerRequestID` was never regenerated.

## Change

- `broker.Config.DirectForm` (opt-in): browser sessions are pre-approved, so
  `/connect/<id>` renders the credential form on first visit. TLS, Origin/CSRF,
  request TTL, one-shot session consumption and audit are unchanged. Enabled in
  this deployment via `BROKER_DIRECT_FORM=1` on the broker container (render.go),
  also available as `direct_form` in the broker config.json. The strict
  channel-bound flow remains the library default and is documented in
  services/credential-broker/docs/operations/configuration.md.
- ToolHub regenerates expired or canceled broker requests on the next
  `status`/`required_credentials` poll (new idempotency key per attempt), so a
  dead connect link self-heals instead of bricking the onboarding.

## Verification

- broker: TestDirectFormSkipsChannelApproval (form+submit without Approve).
- toolhub: TestCredentialBrokerExpiredRequestRegenerates (expired -> fresh link).
- Live: rebuilt image, compose restart, /connect/<id> shows the form directly,
  PAT submit through the real form path marks the request ready and advances the
  onboarding to awaiting-confirmation.

## Follow-on runtime fixes (found during live verification)

- `generic_fallback.go`: `os.CreateTemp` left the generated squid.conf at 0600;
  `docker cp` preserved it and the egress proxy (UID 31) could not read its own
  config. Sidecar staging files now go through `writeWorldReadableTemp` (0644),
  shared with the bridge config.
- Container mode (`HUB_CONTROLLER_REMOTE=1`): the relay publishes on the
  daemon's loopback, unreachable from inside the controller container, so the
  ToolHive remote URL now uses `host.docker.internal`; the ToolHive proxy binds
  0.0.0.0 and the advertised endpoint is rewritten to `workload-controller`
  (overridable via `HUB_CONTROLLER_ADVERTISE_HOST`). `ValidateBackendEndpoint`
  allows the `workload-controller` compose service name.
- Orphaned `.toolhive-<id>` state (e.g. controller killed mid-admit) is now
  reclaimed via the same deterministic cleanup before refusing admission,
  instead of permanently wedging the workload.
- `remove`/`revoke` now stop the connector workload through the controller
  `/release` endpoint (`ControlPlane.Release` + `releaseBindingWorkloads`),
  so a cut connector does not keep a materialized credential alive until the
  idle TTL. Recorded workload instances are released first; the deterministic
  WorkloadInstanceID recompute covers admissions whose store record is gone.
