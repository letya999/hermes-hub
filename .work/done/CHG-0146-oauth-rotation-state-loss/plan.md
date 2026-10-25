# CHG-0146: OAuth rotation state loss and broker deny diagnostics

Owner report (2026-10-21): Google Workspace install loops — the Google consent
completes, the callback page says "authorization failed", the chat agent points
at a confirm message and button that do not exist, and confirm intermittently
fails with `credential broker: denied (403)`.

## Root cause

Broker keys file-session state by `credential/context/binding`. A failed
first confirm leaves `BrokerRotateCredentialID` equal to the current broker
credential, and the probe then re-routes to OAuth with that marker still set.
The callback writes the token under the binding of the freshly rotated
credential reference; its follow-up confirm saw `rotation=true` again, revoked
that grant, minted reference N+1 with a new binding ID, and therefore a new,
empty state key. The probe found no token, re-routed to OAuth, and the loop
repeated (references 1→4 observed on `conn-861434…`).

## Changes

- `materializeBinding`: an OAuth rotation onto the same credential whose
  current reference already carries a reusable grant is complete — keep the
  binding instead of rotating again.
- Agent continuation for wizard links (`/connect/<id>?k=`): one text for
  credentials/confirm/OAuth phases — return to the same link, no separate
  messages or buttons exist, resend via `status resend=true`.
- Broker `RevokeCredential` also deactivates the credential's grants
  (`grant.revoked` per grant) so dead grants stop tripping `CreateGrant`.
- Broker `Acquire` denials carry `grant_missing` / `identity_mismatch` /
  `credential_inactive`; the HTTP layer logs `lease denied deny_reason=…
  grant_id=…` while the wire response stays `403 denied`.
- Wizard `awaiting-oauth` GET self-heals a dead in-memory flow (step TTL or
  toolhub restart) by re-arming through `ensurePreparedOAuth` instead of
  fronting a stale provider URL that only fails at `/oauth/callback`; a
  failed re-arm renders the refreshing wait page, never the dead hop. The
  legacy `/connect/<id>?state=` hop redirects a dead flow onto the wizard
  link for the same re-arm. Wizard capability lifetime is `wizardTTL`
  (24h), independent of the 10-minute step TTL that governs flows/forms.
- Communication-hub continuation jobs no longer share the link-scoped
  outcome key: the job's idempotency key carries its own fingerprint, so a
  resend whose payload drifted (policy version, bound task, reworded copy
  across a redeploy) enqueues afresh instead of hitting "idempotency key
  already used with different payload" → HTTP 400. Identical notices still
  dedupe; the message delivery keeps its link-scoped key.

## Validation

- Red→green: `TestMaterializeBindingKeepsMaterializedOAuthRotation`,
  `TestPrepareContinuationWizardPointsAtSameLink`,
  `TestRevokeCredentialDeactivatesGrants`, `TestAcquireDenialReasons`,
  `TestHTTPLeaseDenialLogsReason`, `TestWizardOutlivesStepTTL`,
  `TestWizardOAuthRestartsDeadFlow`,
  `TestConnectStateLinkRedirectsDeadFlowToWizard`,
  `TestPrepareContinuationSurvivesPayloadDrift`.
- `just check` green (86.07%); broker `quality.py check` green in a Linux
  container; toolhub, credential-broker and communication-hub rebuilt with
  `dev-rebuild` and healthy.
- Live: a confirm after the fix re-routed to OAuth without minting a new
  credential reference (stays at revision 4). The Google consent itself needs
  the owner.
