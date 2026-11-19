# CHG-0331: Extract the control-zone identity registry (M40 stage 3)

Chain: ADR-0106 D1-D3/D8 -> SPEC-0100 R4/R6.4-R6.5 -> this plan ->
state.yaml. Issue #575. Previous: CHG-0330. Next: CHG-0332.

## Goal

One SQLite registry in hub-control owns human, agent and system principals,
organizations, scoped memberships, user-owned agent profiles, narrowing
layers and proven channel links. Existing user/space IDs and sessions stay
stable; migration grants no new permissions. The optional ZITADEL issuer
remains an authentication source, not the registry.

## Slices

### S3.1 Principals, organizations and minimal membership roles

Anchors: new internal/identityhub registry and contracts/identity/v1.
Human, agent and system kinds are explicit. Roles are platform-admin,
org-admin and member with exact scopes. No first-user administrator default.
SQLite uses internal/sqlitedb and transactional writes.

### S3.2 Owned profiles and explicit administration

An agent belongs to exactly one enabled human and can only act for that
owner in its assigned space. System principals need explicit grants for
administration. Break-glass grants are scoped, expiring and audited on use.
No user credentials enter this database or its diagnostics.

### S3.3 Narrowing layers consumed by spawn templates

Platform -> org -> user -> agent restrictions narrow the published passport
ceiling. Skills, guardrails and memory scopes have explicit composition;
the org company brain uses the org's shared space. Existing passports remain
the permission ceiling during migration.

### S3.4 Proven channel identity links

Central transition table: pending -> verified/gone; verified -> revoked/gone.
Only verified links to enabled humans resolve. Proof challenges are bounded,
single-use and hashed at rest. Duplicate channel ownership and fuzzy name
matching deny. Existing verified OIDC links retain provenance.

### S3.5 Migration, contracts and control-zone wiring

Import legacy communication users once, preserving human/space IDs and
creating an owned default agent. Re-import is idempotent only for identical
allowlisted inputs; drift requires an explicit migration. Route channel
lookups through identity/v1; no static fallback after registry refusal.
Mount the registry in the control process and document the migration matrix.

Owner delegated the legacy-group policy on 2026-11-19. A dedicated group
space remains a shared resource with its existing Group.Members ACL, not an
invented human owner. Empty member lists freeze the existing enabled Telegram
membership. Explicit channel-only guests receive no private home or org role.
The verified human and owned agent stay separate from the shared resource;
CHG-0332 binds that distinction into signed run claims. Existing session IDs
and the shared-space passport/credential namespace remain unchanged.

## Tests and acceptance

Red first: TestLegacySpaceMapsToOwnedAgent,
TestAgentPrincipalCannotImpersonateHuman, TestChannelLinkRequiresProof,
TestSimilarNamesNeverMerge. Add duplicate-link, forbidden FSM transition,
system-without-grant, break-glass audit, layer-narrowing, persistence and
safe-diagnostic regressions. Both transports run the identity conformance
suite against the real registry. Use temporary stores only.

Run scoped named tests/vet while editing, then just check and affected
just test. Local synthetic verification does not prove live migration or
remote acceptance. Owner commands are required for live deployment and
publication; neither is part of this local execution.

## Out of scope

Team layers, new IdP federation, replacement of the bundled ZITADEL
deployment and signing tokens/bundles (CHG-0332).
