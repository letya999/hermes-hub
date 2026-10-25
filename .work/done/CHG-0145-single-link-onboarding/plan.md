# CHG-0145: Single-link onboarding wizard

Owner requirement (2026-10-21, verbatim): one stable link for the whole
connector setup — credentials, confirmation/loading and provider OAuth inside
that one page — a second link only if it is Google's own consent screen, and
3–5 chat messages total. No stale per-step links, no "press confirm and wait"
dead-end pages, no claims about buttons that are not there.

## Problem

Today each human action phase mints its own one-time link:

- `awaiting-credentials` → `/connect/<id>?request=<broker-req>` → broker form,
  whose success page dead-ends ("вернитесь в чат").
- `awaiting-confirm` → `/confirm/<id>?t=<nonce>` → a bare confirm click.
- `awaiting-oauth` → `/connect/<id>?state=<oauth-state>` → interstitial →
  provider → `/oauth/callback`.

Every transition also posts a channel notice and the agent narrates on top —
a retry multiplies all of it. Stale `?state=` links die on TTL rotation and
the workload-internal OAuth URL (`localhost:8000`) once leaked into chat from
the provider's own ACTION REQUIRED output, sending the user to a dead page.

## Design

One wizard URL per onboarding: `/connect/<id>?k=<wizard_nonce>`.

- `api.Onboarding.WizardNonce`/`WizardExpires`: minted when the first
  user-facing link is created (broker request mint in `ensureBrokerRequest`;
  `FormNonce` mint sites for the non-broker path), renewed on each wizard
  access while the onboarding is interactive, cleared on terminal phases.
- `api.ActionURL` returns the wizard URL for `awaiting-credentials`,
  `awaiting-confirm` and `awaiting-oauth` whenever a wizard nonce is armed —
  channel notices and status bodies all converge on the same stable link.
  Onboardings without a wizard nonce keep the legacy per-phase links.
- `GET /connect/<id>?k=` renders the current phase and refreshes it:
  - `awaiting-credentials`: broker hop button (new tab) + auto-poll; or the
    inline ToolHub credential form when no broker request exists.
  - `awaiting-confirm`: auto-advancing finish step (POST carries the live
    confirmation nonce; same validation as `/confirm/`).
  - `awaiting-oauth`: provider button carrying the CURRENT
    `ProviderAuthorizationURL` — a rotated state renders fresh, so the chat
    link never stales — plus the existing paste-the-redirect fallback.
  - `preparing`/`confirmed`/`enabled`/`failed`: status, spinner, result.
- The wizard GET itself syncs the broker request
  (`refreshBrokerRequest`) when the phase is `awaiting-credentials` with a
  live request, and every intermediate page meta-refreshes — so the page
  advances itself when the broker form completes: no second ToolHub link,
  no chat round-trip, no separate poll endpoint.
- `/oauth/callback` success redirects back into the wizard URL so the flow
  ends on the same page lineage.
- Communication layer: a notice whose `form_url` is a wizard link edits
  the existing prepare message in place (`prepareMessageID`) instead of
  stacking a fresh message per phase — one channel message carries the same
  URL through credentials → confirm → OAuth. `link-resent` and one-time
  `/remove/`/`/confirm/`/`?state=` links keep their fresh-message rule.
  Continuation copy treats the wizard URL as the already-sent link.

Security invariants kept: every mutating step still carries its own nonce
(confirm nonce, form nonce, OAuth state) embedded only in pages a `k`-holder
can render; `k` alone is a view/resume capability, loopback-gated, TTL'd and
spent with the onboarding. Stale `?state=`/`?request=`/`?t=` links keep their
existing semantics — the wizard is additive, not a contract break.

## Slices

- wizard-nonce: Onboarding fields, mint/renew/clear, ActionURL preference.
- serve-connect-wizard: GET/POST phase machine + poll endpoint +
  oauth-callback redirect.
- notice-quieting: comm-hub suppression for wizard-covered phases +
  continuation copy.
- regression tests + live verify on the dev stack.

## Validation

Focused wizard tests red→green; `go test ./internal/toolhub/...`; dev-rebuild
toolhub; live: one link → broker form → auto-advance → Google consent →
callback → enabled; chat gets ~3 messages (install-accepted, wizard link,
enabled). Report just check / docker gates honestly.
