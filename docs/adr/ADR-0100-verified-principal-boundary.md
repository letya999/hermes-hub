---
description: One provider-independent verified-principal boundary at ingress — opaque identity.Envelope keyed on principal/context, trusted local mapping for single-user mode, provenance fields for future SSO issuers; unauthenticated public ingress is always rejected.
last_verified: 2026-11-16
---
# ADR-0100: Verified-principal boundary — provider-independent envelope, trusted local mapping

Status: Accepted 2026-11-16. Implements GitHub issue #52 (M9).
Builds on [ADR-0011](ADR-0011-stable-identity-identifiers.md) stable
identifiers and [ADR-0054](ADR-0054-human-action-surface.md) human-action
delivery.

## Context

Channel adapters resolve transport identities (Telegram numeric id,
Slack team/user pair, mock chat id) into internal users. Downstream
services — ToolHub, supervisor, Hermes runtimes — must never re-derive
"who is calling" from channel-specific facts, and business logic must
never key records on a Telegram id, email, or display name. The
deployment matrix runs from a single-user personal hub to a future
corporate perimeter where the principal arrives via SSO: the contract
must be the same in both, so an identity provider can change without
ToolHub or Hermes changes.

## Decision

- **D1 — The envelope is the only identity crossing.** Every
  ingress path mints an `identity.Envelope` at the verified edge:
  `PrincipalID` (stable opaque user id), `ContextID`, `RuntimeID`,
  `ConversationID`, `DeliveryTargetID`, `PolicyVersion`, optional
  `Organization`, `CapabilityProfile`, `Environment`, `Generation`,
  `Scopes`. Transport ids appear only inside `ExternalIdentityID`
  (`telegram-<id>`, `slack-<team>-<user>`, `mock-<id>`) — opaque, never
  derived from usernames or emails.
- **D2 — Local mode is a static trusted mapping.** The operator-authored
  `communication.users.yaml` binds verified channel ids to principal
  records (`telegram_ids`, `slack_ids`). No IdP is required; a hub with
  one mapped user is the baseline and stays fully supported.
- **D3 — Provenance rides in the envelope, not in the key.**
  `Envelope.Issuer` and `Envelope.Subject` record which authority
  vouched for the principal at ingress (empty for channel-mapped
  traffic today, the OIDC issuer+`sub` once linking is enabled). They
  are audit metadata — never routing keys, never parsed for
  authorization.
- **D4 — Ingress is always authenticated.** Every surface that can mint
  an envelope already requires proof: Telegram updates are bound to the
  configured allowlist (verified `telegram_ids`), Slack events verify
  `x-slack-signature` HMAC, control/gateway APIs require a provisioned
  bearer, and human-action links resolve stored one-time nonces. There
  is no anonymous principal path and none is added.
- **D5 — Provider swap is a mapping concern only.** Adding an issuer
  (issue #53) extends how a transport id resolves to `PrincipalID`;
  the envelope shape, downstream verification and ToolHub policy
  evaluation are unchanged.

## Consequences

- Business logic keys on `PrincipalID`; channel ids are edge facts that
  die at the adapter boundary. Tests that sweep cross-user ids exercise
  the same contract the model sees.
- The envelope's provider-independence is a property test: no field
  carries a provider name that authorization would have to parse.
- `Issuer`/`Subject` are optional and length-bounded; their absence is
  the personal-mode default and must not be treated as anonymous.
- Unauthenticated ingress has no degraded mode: unknown transport ids,
  bad signatures and missing bearers deny, they do not impersonate.

## Appendix A — Deployment shapes (added with the `iam:` stack block)

The boundary is provider-independent; where the issuer lives is a stack
decision the passport now encodes instead of an env convention:

- **`iam:` absent** — D2 stands alone: trusted local mapping, no IdP
  containers, the single-user baseline.
- **`iam.mode: external`** — the issuer is upstream infrastructure the
  operator already trusts (Authentik, Keycloak, ZITADEL Cloud, Entra
  ID). The passport carries `issuer` + `client_id`; no IdP container
  renders. Declaring the bundled services in this mode is a compile
  error — two issuers for one stack is never coherent.
- **`iam.mode: bundled`** — the optional in-stack issuer: `identity-hub`
  (ZITADEL v4, digest-pinned) plus `hub-postgres`, both declared
  `mode: shared` and rendered only on the infra-owning space. The shared
  postgres has exactly one consumer today; widening it is a separate
  decision, not a side effect of this block. A bundled issuer must be
  host-level — ZITADEL serves issuers without a path, and the derived
  dev default `http://idp.localhost:<port>` rides a network alias plus
  `*.localhost` loopback so browser and container agree.

A bundled deployment tolerates an empty `client_id` during first boot —
the gateway env renders empty and stays in local mode until the operator
finishes bootstrap and republishes. It never starts half-configured:
issuer without a client would fail startup, so the render emits neither.
The `ZITADEL_MASTERKEY` materialized into `identity-hub.auth` encrypts
the IdP store and must never rotate or be lost.
