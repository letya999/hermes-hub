---
description: Broker-only credential intake — communication drops the legacy credential surface entirely.
last_verified: 2026-10-18
---
# ADR-0049: Broker-only credential intake

Status: Accepted 2026-10-18. Implemented as CHG-0120. Planned as ADR-0047
in the phase plan, renumbered (that number went to the channel contract).

## Context

The gateway carried a second credential path: `HUB_CREDENTIAL_STORE` +
`HUB_CREDENTIAL_KEY_FILE` decrypting an encrypted secrets.Service, a
companion job audit ledger and a local ToolHub registry attach —
duplicating what the Credential Broker and runtime self-env already cover.

## Decision

Communication no longer reads credential/env-store configuration. Secret
intake runs only through the Broker (managed) or runtime `/v1/self-env`
(unmanaged). `/connections` always queries ToolHub over `/v1/connectors`.
Job correlation stays as structured log records; the audit ledger belongs
to ToolHub/runtime.

## Consequences

- One intake path per mode; no parallel trust surface in the gateway.
- The gateway process no longer holds the credential decryption key.
- Deployments that staged secrets through the removed env vars must
  re-intake them through the broker — no data is migrated (the surface was
  deleted intentionally, not relocated).
