# CHG-0315: SSO-gated bearer links for interactive surfaces

Milestone: M9 — Enterprise Readiness (post-IAM hardening)
Issues: extends #53/#54 — bearer links become principal-bound under IAM.
Branch: feat/m9-sso-gated-links (from origin/dev)
Constraint: single-user/local mode unchanged — when `iam` is off, links
stay pure bearer exactly as today.

## Problem

Every interactive surface the hub mints is a raw bearer URL: whoever
holds the link opens the page. Under SSO-enabled deployment that is too
weak — a leaked onboarding/confirm/memory link is a live capability:

- Connector onboarding `/confirm/onb-*` (credential entry, OAuth
  hand-off) — minted in ToolHub `control_http.go`.
- Device-code pages `/device` — credential flows.
- `memory_open` — one-hour read-only view into the space memory vault.
- Wizard/draft links (skill drafts, prepare-follow-up merges).

Today possession == authorization. Under IAM we know *who* someone is —
the link should only open for the principal it was minted for.

## Goal

When `iam` is enabled and a minted link carries a required principal,
opening it requires a verified SSO session resolving to that principal.
Anyone else (or no session) is redirected through the OIDC round trip;
foreign identities get a deny page. Audit records denials.

## Design

- **Mint-time binding**: the link record gains `required_principal`
  (and optionally `required_subject` for tighter pinning). Default: the
  principal whose run minted the link. Link types decide their own
  policy at mint — the record just stores the requirement.
- **Session proof, not a second OIDC client**: communication-hub already
  owns the OIDC provider. After `oidc/callback` it issues a signed
  session cookie (`hub_sso`, HMAC over `iss|sub|principal|exp|nonce`,
  key materialized in shared auth — `ensureIAMAuth` already fabricates
  masterkey/pg/cookie secrets; add `link-session`). ToolHub verifies the
  cookie with the same key — pure symmetric verification, no network hop,
  no second client.
- **Redirect flow**: page handler without a valid session → 303 to
  `communication-hub/iam/oidc/start?return_to=<url>`; callback binds the
  session then bounces back to `return_to` (same-origin or allowlisted
  ToolHub origin). Session principal ≠ `required_principal` → deny page.
- **Exempt**: `/iam/link/*` enrol/link landing — that IS the door; a
  session cannot be required to create a session.
- **Opt-out per link type**: links intentionally forwardable (e.g. a
  credential form an admin fills for a user) mint with
  `sso_required: none`; sensitive ones default to `principal`. Decide per
  surface in review, do not blanket-gate.
- **IAM off**: `required_principal` ignored, current bearer behavior —
  documented as a downgrade property, not a bug.

## Surfaces (decide gating per type)

| Surface | Mint site | Proposed gate |
|---|---|---|
| Connector onboarding `/confirm/onb-*` | toolhub control | `principal` |
| Device page `/device` | toolhub control | `principal` |
| `memory_open` view | toolhub agent tool | `principal` |
| Wizard/skill-draft links | toolhub control | `principal` |
| Credential form (admin-on-behalf) | broker facade | `none` (forwardable) |
| `/iam/link`, `/iam/oidc/*` | communication | exempt |

## Slices

- S1: session cookie contract — `internal/iam` issue/verify (HMAC, exp,
  nonce), cookie minted in `oidc/callback`, shared `link-session` key in
  `ensureIAMAuth`; unit tests.
- S2: link record `required_principal`/`sso_required` fields + mint-side
  population for each surface; migration-safe defaults (existing links
  stay bearer until re-minted — documented).
- S3: ToolHub page-handler gate — cookie verify, redirect to oidc/start
  with return_to, deny page, audit `link_denied`/`session_denied`.
- S4: tests — gating matrix per surface, foreign-principal deny,
  no-session redirect, IAM-off passthrough, cookie tamper/expiry.
- S5: docs — operations.md (link security under SSO), ADR-0100 appendix,
  locale strings (4 catalogs) for redirect/deny pages.
- S6: live verification on bundled ZITADEL — mint onboarding link, open
  foreign session → deny; open bound session → page; IAM off → bearer.

## Risks

- Session cookie shared between two services → key rotation is a
  redeploy event; document it. Alternatives (introspection hop to
  communication-hub) traded away for zero-latency verify.
- `return_to` must be strictly allowlisted (hub-internal origins) to
  avoid open-redirect.
- Forwardable-link surfaces are a product decision — flag in review, do
  not silently relax.
