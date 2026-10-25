---
description: Human connector actions use one-time `?t=` links on a restricted surface, delivered on the channel the request came from; public exposure is opt-in via HUB_PUBLIC_ORIGIN and serves only human-action paths.
last_verified: 2026-10-19
---
# ADR-0054: Channel-agnostic human action surface — one-time links, device flow, transactional credential save

Status: Accepted 2026-10-19. Implements GitHub issue #283 (M13 P2).
Builds on [ADR-0053](ADR-0053-recipe-source-ledger.md) recipe resolution.

## Context

Connector onboarding needs moments only a human can complete: entering a
credential, confirming a review, approving a removal, finishing OAuth. The
original surface was a loopback credential form plus a confirm path, each
with its own token style, hard-wired to the Telegram channel, and OAuth
assumed a browser on the same machine as the callback listener. A phone or
a headless host could not finish an install, and the gateway rejected any
non-loopback link even when an operator deliberately published one.

## Decision

- **D1**: One link-token scheme across all human-action pages. Every
  action URL carries `?t=<token>`; the token resolves the onboarding's
  stored nonce for that operation only — a `/confirm` token never opens a
  `/credentials` form. Spent or expired links answer `410 Gone`, POSTs
  without the token are rejected, and ownership is re-checked against the
  onboarding's principal/context/runtime on every page.
- **D2**: Public exposure is opt-in and restricted. `HUB_PUBLIC_ORIGIN`
  (https only) makes link generation use that origin; `HUB_PUBLIC_LISTEN`
  binds a second listener serving only the human-action paths —
  `/credentials`, `/confirm`, `/remove`, `/connect`, `/device` and
  `/oauth/callback`. Control and gateway routes answer 404 there. The
  communication gateway accepts an https `form_url` only when its host
  equals the configured origin exactly.
- **D3**: Delivery follows the origin channel. The prepare-outcome notice
  routes by the channel of the message job that initiated the onboarding
  (persisted as the prepare origin) rather than assuming Telegram.
  `Caps().Actions` marks channels that can render actionable affordances;
  adapters without it receive the same plain-text link — the fallback is
  the contract. Telegram declares `Actions`.
- **D4**: Dead-redirect OAuth gets two alternatives on the `/connect`
  interstitial: an RFC 8628 device flow (`/device/<id>` renders user code
  and verification URI; polling maps `authorization_pending`,
  `slow_down`, `expired_token`, `access_denied`) and a paste-the-redirect
  form that validates the pasted `state` against stored state and the
  redirect's registered origin before completing through the shared PKCE
  callback path.
- **D5**: The credential form is rendered from the recorded recipe and
  ledger — connector name/kind/`source@commit`, trust tier, egress hosts,
  per-field source and rank, HTTPS-only `token_url` help links. R6-ranked
  (unverified suggestion) fields render distinctly and are never required.
  Read and write tools are listed separately; writes are marked disabled
  by default.
- **D6**: Credential save is transactional. Nothing is persisted — no
  ciphertext, Broker credential or connection — until a read-only
  `tools/list` probe of the submitted values succeeds; the admission path
  probes exactly once. Probe failure re-renders the form with the entered
  values page-side and a public error.
- **D7**: `hubctl connector link --onboarding <id>` prints the action URL
  (origin: `--origin` flag → `HUB_PUBLIC_ORIGIN` → the loopback listener);
  `--confirm` completes an awaiting-confirmation onboarding through the
  same control op for scripts.

## Consequences

- Models never receive link tokens, nonces, OAuth state, device codes or
  submitted credential values: a property test sweeps every control
  operation over every onboarding phase for stored sentinel material.
- The link itself is the secret — sent over the channel the request came
  from, one-time, expiring. Channels without action affordances still
  deliver a working plain link.
- Public exposure cannot widen silently: an unset or http
  `HUB_PUBLIC_ORIGIN` mints loopback links only, and the gateway's
  accepted-link check pins the exact origin host.
- Live acceptance (public-origin install, device flow against a real AS,
  `/confirm` from another device) still needs a deployed stack and an
  outbound tunnel — tracked under M18 #145/#146.
