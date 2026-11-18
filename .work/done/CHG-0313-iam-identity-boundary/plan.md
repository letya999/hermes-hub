# CHG-0313: IAM-ready identity — verified-principal boundary, ZITADEL OIDC linking, authz contract

Milestone: M9 — Enterprise Readiness
Issues: #52 → #53 → #54 (strict chain, one commit per issue)
Branch: feat/m9-iam-identity (from origin/dev)
Constraint: conditional pack — no OPA/Cedar, no new deps, single-user mode unbroken.

## Gap analysis (verified against origin/dev)

- #52 envelope + local mapping + authenticated ingress already exist in
  substance (`identity.Envelope`, `communication.users.yaml`, bearer/HMAC
  everywhere). Missing: formal ADR naming the boundary and envelope
  provenance so an SSO-origin claim is distinguishable.
- #53 ZITADEL/OIDC does not exist at all; no user-facing channel-account
  linking (users.yaml is operator-edited).
- #54 `/v1/rights/decide` PDP + `Store.Decide`/`DecideAsserted` +
  call-time `gate().interactive` enforcement already exist. Missing:
  doc/test verification that the contract covers the issue's fields.

## Slices

- S1 #52: ADR-0100 verified-principal boundary + `Envelope{Issuer,Subject}`
  provenance (optional, bounded, omitempty — schema-compatible). Commit 1.
- S2 #53a: `internal/iam` — stdlib-only OIDC (discovery, JWKS w/ kid
  rotation refetch, PKCE auth-code exchange, id_token validation) +
  durable link store (iss+sub→principal, transport bindings, audit jsonl).
  Fixture-IdP tests. Commit 2.
- S3 #53b: communication wiring — `/iam/link/<nonce>` landing,
  `/iam/oidc/start`, `/iam/oidc/callback`, `/iam/unlink` routes;
  `/link` chat command mints a bound expiring nonce; linked transport
  resolves principal when static mapping misses; locales x4; tests.
  Commit 3.
- S4 #54: contract verification — confirm Decide covers
  principal/context/action/resource/connection/audience/revision;
  visibility+execution both gated; ADR-0101 note + regression tests for
  any gap. Commit 4.

## Follow-on: stack-managed identity hub (approved followup slice)

The original slices landed the contract; this follow-up makes the IdP a
stack-managed optional component, per the approved plan (bundled =
ZITADEL on one shared postgres; iam: block in config.yaml; start with the
live Authentik PoC, then stack integration).

- S5 `iam:` passport schema — config.iam{mode,issuer,client_id} classified
  in the layer registry, compile-validated against stack services,
  consumer/effect entries for the diff receipt.
- S6 catalog release 42 — infra services `identity-hub` (ZITADEL) and
  `hub-postgres` (shared postgres, one consumer for now).
- S7 compose render — bundled emits zitadel+postgres on the shared network
  (idp.localhost alias for the derived dev issuer), gateway env projects
  HUB_OIDC_*; external emits env only. identity-hub.auth secrets materialize
  (masterkey 32 chars, never rotates).
- S8 live acceptance — real Authentik PoC done (link/409/410/unlink);
  bundled ZITADEL render+start verified on artem_main dev.
- S9 docs — ADR-0100 appendix (deployment shapes), operations.md iam
  section update, threat-model boundary note.

## Non-goals

- OPA/Cedar, SAML inside Hermes, RBAC roles, org membership enforcement,
  SCIM/group sync, automatic ZITADEL tenant bootstrap (documented manual).
- Authentik stays a standalone PoC — it is never the bundled provider.
