---
description: v1 identity - identity-hub owns principals and channel identities, layers platform -> org -> user with agent profiles as user sub-entities and system principals, signed run tokens on every agent call, signed policy bundles as the only authority.
last_verified: 2026-11-19
---
# ADR-0106: Identity, layers, run tokens and policy bundles

Status: accepted direction (M40, owner issue #572). Date: 2026-11-19.
Delivery: CHG-0331 (identity-hub), CHG-0332 (run tokens and policy bundles).
Builds on ADR-0011 (stable identifiers) and ADR-0100 (verified-principal
envelope); both stay in force. Revisits the "no new tokens" scope recorded in
#397 (D4 below); required by #401 (tenant identity propagation).

## Context

- The verified edge mints `identity.Envelope` (ADR-0100), but downstream
  calls are trusted by headers: `internal/runtime/mcp_proxy.go` stamps
  `X-Hub-Job-ID`/`X-Hub-Run-ID`, and ToolHub verifies them by looking up the
  supervisor's durable job record (`api.RunAuthority`, #397). That lookup
  works only for a caller that can reach the job store synchronously; the
  model-gateway, memory-hub and egress-gateway of ADR-0105 cannot, and a
  shared relay cannot tell tenants apart by which container received the
  request (#401).
- Principals live in three places: `communication.users.yaml`,
  `TELEGRAM_ALLOWED_USERS` and the optional bundled ZITADEL issuer
  (`identity-hub` container, ADR-0100 appendix A).
- Policy authority is a set of generated files on a shared volume selected
  by `generated/passports/current`; every reader re-validates digests on its
  own.
- Proactive work (routines, assignments, watchers) runs "as the user" with
  no record that the platform, not the user, initiated it.

## Decisions

1. **identity-hub owns principals and channel identities.** One registry in
   the control zone holds principals, layers, agent profiles and verified
   channel identities. `communication.users.yaml` and
   `TELEGRAM_ALLOWED_USERS` become import sources, read once by a migration,
   then retired. The bundled or external OIDC issuer (ADR-0100 appendix A)
   is an authentication source that links to a principal; it is not the
   registry.
2. **Three layers, narrowing only.** `platform -> org -> user`. The platform
   layer (operator) publishes catalogs and ceilings; an org layer narrows
   them; a user layer narrows the org. Effective rights are the intersection;
   no lower layer can widen a higher one. A user without an org inherits the
   platform layer directly. Each layer supplies skills, guardrail policies
   and memory scopes to spawn-controller templates. The company brain belongs
   to the org layer and its shared space, under the same narrowing rules.
3. **Principal kinds.**
   - *human* - a user principal (stable opaque id, ADR-0011).
   - *agent profile* - a named sub-entity of exactly one user (for example
     `research`, `inbox`), with its own action-mode default and a grant set
     that is a subset of the user's. It never exists without its user and is
     deleted with it. Credentials stay the user's; the profile only narrows.
   - *system principal* - `system:<service>` for platform-initiated work
     (proactive-engine clock, watchdog). A system principal holds no user
     credentials and no user grants. Work done for a user is executed as that
     user's agent profile; the initiating system principal is recorded as
     `initiator`, never as the acting principal.
4. **Run tokens replace header trust.** The token service in identity-hub
   signs tokens (Ed25519, compact JWS) for an admitted run. Token kinds are
   `run`, `link` and `service`, with distinct verification purposes. Run
   claims are `sub`, `act`, `org`, `space`, `conversation` or `assignment`,
   `task`, `run`, `gen`, `policy_rev`, `origin`, `mode` and `exp`; issuer,
   audience and key id bind the token to its issuing service, recipient and
   signing key. Expiry is bounded by the run deadline. The runtime receives the token
   through its private `/v1/jobs` contract and attaches it to every agent
   call; it never mints or edits one. Verifiers (tool-hub, memory-hub,
   model-gateway, egress-gateway) verify signature, audience and expiry
   locally. RunAuthority remains the check for live generation and
   revocation, reached through `task/v1` rather than direct store access.
   The durable job record stays the authority for run state; a signed token
   cannot keep a terminated, revoked or superseded run alive.
5. **Refusals happen before any backend.** A missing, unsigned, expired,
   wrong-audience, cross-space, run-aliased (token `run` differs from the
   call's run) or stale-generation token is refused with a typed error and an
   allowlisted audit record. There is no fallback to headers and no default
   tenant.
6. **Signed policy bundles.** The publisher compiles the six passports plus
   the org and platform layers into one immutable bundle: canonical JSON,
   digest = policy version, signed with the hub-control policy key. Every
   consumer loads a bundle by digest, verifies the signature and refuses an
   unknown, unsigned or mismatched bundle. `generated/passports/current`
   becomes a pointer to a bundle digest; the shared-volume files stay a cache,
   never authority.
7. **Key custody.** identity-hub's token service generates and retains
   run, link, service and policy signing keys in the control zone. Only public keys
   leave hub-control (identity contract, key id in every token). Keys rotate
   with an overlap window of one maximum run budget. They are never delivered
   to a runtime, a worker or the model; credential-broker remains the only
   authority for user and connector credentials (ADR-0105 D7).
8. **Explicit membership and administration.** Minimal roles are
   `platform-admin`, `org-admin` and `member`, scoped to their platform or
   organization. System principals receive rights only through explicit
   grants. Super-admin authority is an audited, scoped break-glass grant,
   never a principal kind or an implicit authorization bypass.

## Lifecycles

Channel identity link (identity-hub):

| From | Event | To |
|---|---|---|
| - | owner starts a link (code, OIDC, operator import) | pending |
| pending | proof verified (channel echo, OIDC `sub`) | verified |
| pending | proof expired or rejected | gone |
| verified | owner or operator revokes | revoked |
| verified | principal deleted | gone |

Only `verified` maps a transport id to a principal; `revoked` and `gone`
deny and are kept for audit.

Run token (per run): `issued -> presented* -> expired`; a generation bump
or run termination makes every outstanding token stale at once.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| channel -> identity-hub | verified transport id resolves to one principal | unknown id, revoked link, display name or email as key |
| runtime -> agent zone | calls with a valid run token for that audience | everything in D5 |
| agent profile -> grants | subset of its user's effective grants | widening, another user's credentials |
| system principal -> work | creating jobs for a user through an assignment the user owns | acting with user credentials directly, impersonating the user as `sub` |
| principal -> administration | current scoped membership or explicit audited break-glass grant | system principal without a grant, implicit super-admin bypass |
| consumer -> policy bundle | signed bundle by digest | unsigned file, pointer without bundle, foreign space bundle |

## Alternatives

- **Header assertions backed only by `RunAuthority` (#397).** Rejected:
  shared relays need a signed identity carrier (#401). RunAuthority is kept
  for generation and revocation; it does not replace signature verification.
- **HMAC tokens with a cluster key.** Rejected: every verifier would hold a
  signing secret; asymmetric keys keep signing in hub-control.
- **mTLS per space as tenant identity.** Deferred: it proves the network
  peer, not the run; it can be added under tokens later without contract
  change.
- **ZITADEL as the principal registry.** Rejected: an external issuer is
  optional (ADR-0100); the registry must work in local mode.

## Preserved v0 invariants

- Default-deny: tokens carry no grants; grants come from the bundle.
- Fail-closed passports: a missing or foreign bundle denies.
- Uncertain outcomes are never retried; a token does not authorize replay.
- Credentials never enter the model plane: tokens carry ids, never secrets,
  and runtimes cannot mint them.
- ADR-0100 D1: transport ids never become routing or authorization keys.

## Appendix A: Legacy shared-space migration (2026-11-19)

Legacy dedicated group spaces remain shared resources and preserve their
explicit member ACLs. Empty member lists freeze the existing enabled Telegram
membership. Channel-only members receive no private home or org role.
Disabled or retired identities cannot reappear through a guest alias. Tokens
distinguish the verified human `sub`, its owned agent `act`, and the shared
target `space`. The shared passport and credential namespace and existing
session IDs remain stable.
