---
description: v1 identity - identity-hub owns principals, orgs, roles, channel links and run tokens; layers platform -> org -> user -> agent live in the six passports held by spawn-controller; one signed run token per run with an aud list.
last_verified: 2026-11-20
---
# ADR-0106: Identity, layers in passports, one run token per run

Status: proposed. Rewritten 2026-11-20 for the simplified v1 plan (M40.1
#599, CHG-0334); direction agreed with the owner on 2026-11-20, record text
awaits owner review. The 2026-11-19 version (layers, profiles and
break-glass inside identity-hub, signed policy bundles) was never merged to
`dev` and is replaced.
Delivery: M40.5 (identity-hub, #603), M40.3 (layer passports in
spawn-controller, #601), M40.14 (run tokens, #612).
Builds on ADR-0011 (stable identifiers) and ADR-0100 (verified-principal
envelope); both stay in force.

## Context

- Downstream calls are trusted by headers: the runtime MCP proxy stamps
  `X-Hub-Job-ID`/`X-Hub-Run-ID` and ToolHub looks the run up in the
  supervisor's job store (`RunAuthority`, #397). Services that cannot reach
  that store cannot verify a caller, and a shared relay cannot tell tenants
  apart (#401).
- Principals lived in three places (`communication.users.yaml`,
  `TELEGRAM_ALLOWED_USERS`, the optional ZITADEL issuer). CHG-0331 built a
  control-zone registry and cut the gateway over to it.
- The first token work (CHG-0332) signed run tokens, policy bundles and
  several per-purpose service tokens inside the v0 topology (identity in
  the supervisor, `cmd/hub`). It is parked: committed slices stay on this
  branch, the unfinished part is commit `6e6b53c6` on
  `chore/park-chg-0332-run-tokens`. M40.14 redoes it on the v1 services and
  reuses its tests and verifier.

## Decisions

1. **identity-hub owns who is who.** Principal (human, agent, system),
   Org, roles, ChannelLink, signing keys and run tokens, in `identity.db`.
   `communication.users.yaml` and `TELEGRAM_ALLOWED_USERS` are import
   sources, read once by a migration, then deleted. An OIDC issuer is an
   authentication source linked to a principal, not the registry; the
   bundled ZITADEL container is renamed `oidc-provider`.
2. **Layers live in passports.** Layers are `platform -> org -> user ->
   agent` (a special agent or a subagent). Each layer carries all or part
   of the same six passports (`stack`, `config`, `models`, `workspace`,
   `tools`, `grants`); a lower layer only narrows. spawn-controller stores
   the layer passports and computes the effective spec with the existing
   passport compiler, plus a digest. identity-hub only knows membership and
   roles. Editors: platform-admin, org-admin, the user, and the user for
   their own agents. The layer, profile and break-glass APIs of the
   CHG-0331 registry are deleted in M40.5.
3. **Principal kinds.** *human* (stable opaque id, ADR-0011); *agent*,
   owned by one human, never holding credentials of its own; *system*
   (`system:<service>`) for platform-initiated work, holding no user
   credentials. Group-chat participants other than the space owner are
   *guests*: opaque ids, untrusted input, never approvers, no grants.
4. **Channel links need in-channel proof.** A link is `pending` until the
   observed sender echoes the code in that channel; only then is it
   `verified`. An OIDC callback leaves the link pending. Enrolment hints are
   sent only in private chats. Only `verified` maps a transport id to a
   principal (state machine: [state-machines.md](../state-machines.md)).
5. **One run token per run.** identity-hub signs (Ed25519, compact JWS) one
   token per run with claims `sub`, `agent`, `space`, `run`, `parent_run`,
   `gen`, `policy_digest`, `origin` (`owner|unprompted`), `aud` (the list
   of services this run may call) and `exp`. task-engine requests it at
   admission, spawn-controller delivers it to the runtime session, the
   runtime attaches it and never mints one. A subagent run gets its own
   token with `parent_run`.
6. **Verification.** Each verifier (tool-hub, memory-hub, model-gateway,
   channel-hub, egress-gateway, proactive-engine) checks the signature, its
   own name in `aud` and expiry locally, then generation and revocation at
   task-engine (cached per run). Missing, forged, expired, wrong-audience,
   foreign-space, run-aliased, stale-generation or policy-mismatched tokens
   are refused before any backend. There is no header identity and no
   unsigned admission.
7. **No signed policy bundles.** A policy slice is read by digest over an
   authenticated call to spawn-controller; the digest in the token binds
   the run to it. The signed-bundle machinery of CHG-0332 is not carried
   into v1.
8. **Key custody.** Signing keys live in `identity.db` and never leave
   identity-hub; public keys are published through the identity contract
   and cached by verifiers. Keys rotate with an overlap of one maximum run
   budget.
9. **Administration.** Roles `platform-admin`, `org-admin`, `member`,
   scoped to their platform or org. Operator deactivation goes through an
   audited role, never a bearer bypass.

## Boundary crossings

| Crossing | Allowed | Refused |
|---|---|---|
| channel -> identity-hub | verified transport id resolves to one principal | unknown id, pending or revoked link, display name or email as key |
| runtime -> any verifier | calls with a valid run token naming that verifier in `aud` | everything in D6 |
| agent -> rights | the narrowed agent layer of its user's passports | widening, another user's credentials |
| system principal -> work | jobs for a user through a routine or assignment the user owns, `origin=unprompted` | acting with user credentials, appearing as the user in `sub` |
| guest -> decisions | messages as untrusted data | approving, granting, raising a mode |

## Alternatives

- **Header assertions backed only by `RunAuthority`.** Rejected: services
  without the job store need a signed carrier; RunAuthority remains the
  generation and revocation check at task-engine.
- **Several per-purpose service tokens and token exchange.** Rejected for
  v1: one run token with an `aud` list covers every caller; token exchange
  and `act` chains are not needed without a second use.
- **Signed policy bundles.** Rejected: digest plus authenticated read gives
  the same integrity with less machinery.
- **Layers inside identity-hub.** Rejected: layers are passports; keeping
  them in two places is the drift the passports removed.

## Preserved v0 invariants

- Default-deny: tokens carry no grants; grants come from the effective spec.
- Fail-closed passports: a missing or foreign digest denies.
- Uncertain outcomes are never retried; a token does not authorize replay.
- Credentials never enter the model plane: tokens carry ids, never secrets.
- ADR-0100 D1: transport ids never become routing or authorization keys.
