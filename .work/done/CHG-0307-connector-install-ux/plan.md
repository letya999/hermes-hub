# CHG-0307 — Connector install UX: immediate links, no redundant confirm

## Problem

- Credential notices could be delivered before a usable action URL existed:
  `prepareOnboardingWrite` bumped `NoticeVersion` on every gate change, so the
  first awaiting-credentials notice carried `form_url=""` ("link arrives in the
  next message") and the follow-up could be lost downstream.
- A record awaiting credentials with a live pending broker request never got
  its notice re-delivered: `reconcileBrokerReady` returned early on pending
  requests, so the armed-gate notice stayed undelivered until a state change.
- Install asked for a second protected click (`GateConfirmSetup`) after the
  owner already consented — by submitting credentials, granting OAuth at the
  provider, or issuing a credential-free install. The click added no security:
  permissions are not editable and not readable in that form.

## Changes

- `internal/toolhub/onboarding_state.go`: a human-action notice bumps only
  when `api.NoticeActionURL` resolves a non-empty URL (or the stage is
  terminal). An action URL appearing under an unchanged gate signature also
  triggers the bump.
- `internal/toolhub/api/prepare_followup.go`: exported `NoticeActionURL`.
- `internal/toolhub/control.go`: new `completeSetup` runs the former
  confirm→enable tail (materializeBinding → Enable/EnableReady → live →
  notice) with the same failure handling (OAuth re-route,
  resetSubmittedCredential, persistFailure). It materializes unconditionally —
  a kept BindingID still needs re-binding on rotation — and converts
  `ErrUnauthorized` with no collected credential into the credential-form
  state (fill-or-skip) instead of a dead-end failure. `autoCompleteSetup`
  guards it: never consumes `PendingWrites`/`WritesEnabled` (allow_writes
  keeps its protected gate) and requires collected credentials or a
  credential-free definition.
- Consent audit: `api.HumanConsent.Source` records which signal counted —
  `credential_submission`, `oauth_callback`, `install_request`, empty for a
  clicked confirmation.
- Mutation sites wired to `completeSetup`/`autoCompleteSetup`: broker
  `credential.ready` transition and admission success (`control_broker.go`),
  active-credential reuse, loopback form submit (`control_credentials.go`,
  non-fatal — bind failure persists its own state), OAuth completion
  (`remote_oauth.go`, `prepared_oauth.go`), device authorization completion
  (`remote_oauth.go`), credential-free tails of `finishSelfInstall`,
  `prepareCatalog` and `prepareRemote` (`control_prepare.go`,
  `remote_admit.go`), bounded-CLI installs (`cli_prepare.go`,
  `cli_artifact.go`) and `connector_upgrade` (the upgrade request itself is
  the consent; the credential was already collected).
- `completeSetup` clears `BrokerRequestID`/`BrokerAuthorizationURL` once bound.
- `reconcileBrokerReady` re-delivers the armed credential notice on live
  pending broker requests instead of returning early — a record waiting on
  credentials with a usable URL actually gets its link delivered.
- `ensureBrokerIntake` routes records with `AdmissionPending` back to the
  admission probe instead of early-returning on an existing
  `BrokerCredentialID`.
- `Rotate` on a record already awaiting a broker request is a no-op returning
  status — the pending request is itself the credential step.
- `remove`/`allow_writes`/`PendingWrites` gates are unchanged: destructive and
  privilege-escalating actions keep their protected confirmation.

## Deliberately out of scope

- Observation paths (`refreshBrokerRequest` generic reads,
  `refreshConfirmation`) do NOT auto-complete armed setup gates: completing on
  read consumed the gate for the confirm handler itself and for allow_writes.
  Records armed before this change still finish through the existing
  confirm-click path.
- Drift re-confirmation (`reconcile_seams.go`) keeps its armed gate: an
  externally-mutated connector is re-accepted consciously, not auto-completed.

## Verification

- Updated consent assertions: records now reach `StageLive` with
  `HumanConsent.Source` set instead of `Awaiting(GateConfirmSetup)`.
- `finishAsHuman`/`confirmViaTrustedChannel` are no-ops on auto-completed
  records; they still drive the trusted confirmation on genuinely armed gates
  (allow_writes, legacy records).
- Confirmation-endpoint tests mint the pending record via `newOnboarding`
  directly so the armed gate survives for the path under test.
- The resend/dedup regression (`TestBrokerEventsReconcileSubmissionAndResendExactLink`)
  now exercises the credential gate with a pending broker request.
