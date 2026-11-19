---
description: Identity registry module — SQLite principals, owned agents, scoped administration, narrowing layers and proof-bound channel links; deployment wiring remains in CHG-0331.
last_verified: 2026-11-19
---

# Identity registry module

`internal/identityhub` is the registry foundation for ADR-0106. It implements
the read side of `contracts/identity/v1` and builds `AgentTemplate` DTOs.
Control-process wiring, the gateway cutover and verified OIDC-link import
remain in [CHG-0331](../../.work/in-progress/CHG-0331-identity-hub/plan.md).
The existing optional ZITADEL container is an authentication source; this
module does not replace that deployment. Current gateways still use their
legacy mapping until the cutover is complete.

## State and boundaries

`Open(path)` uses `internal/sqlitedb`, schema 1, a private SQLite file and WAL.
An existing file must carry the registry's `HID1` application ID and schema
version. A read-only probe checks those fixed metadata fields before WAL is
enabled; a foreign SQLite store or a symlink is refused. Registry tables hold
principals, organizations, memberships, owned profiles, space mappings,
layers, import receipts, channel links, administrative grants and audit.

Principal kinds are human, agent and system. An agent has exactly one human
owner and an assigned space; a bare agent cannot impersonate its human or
inherit an administrative role. `CanAct` and `Template` use the same database
predicate, including current org membership. System principals have no owner
credentials or inherited user grants.

`Diagnostics` returns a fixed struct of counts only: schema, humans, agents,
systems, organizations, verified/pending links and audit events. It exposes
no principal IDs, paths, proof material, credential values or credential keys.
Database failures cross the module boundary as typed contract errors.

## Administration and profiles

Roles are `platform-admin`, `org-admin` and `member`; org roles have an exact
organization scope. Import never appoints an administrator. The initial
platform administrator requires an explicit local operator bootstrap; that
method is not an HTTP endpoint. Role and delegation issuance require a human
administrator.

System administration requires an explicit scoped, expiring grant. A grant
also depends on its issuer retaining the relevant human administrative role.
Break-glass grants bind one operation and scope, expire within an hour and
write an audit event on every use. Other administrative grants expire within
24 hours. Audit failure rolls back the authority change. Reasons are bounded
codes, rather than arbitrary text that could contain secrets.

User-owned profiles narrow `platform -> org -> user` constraints. Grants,
skills and memory scopes intersect; guardrails accumulate. Null lists inherit
and empty lists deny all. Modes are bounded by their parent; the default agent
mode is `ask-critical`. A stricter parent immediately narrows existing child
profiles when a template is built. Org company memory names the org's shared
space, which cannot alias a human's private space.

These constraints do not grant permissions: their consumer must intersect
them with the published passport ceiling. Spawn-template consumption and
signed policy publication are not yet wired into the running services.

## Lifecycle tables

| Entity | From | Event | To |
|---|---|---|---|
| Human | active | delete | inactive |
| Channel link | pending | proof verified | verified |
| Channel link | pending | proof rejected/expired | gone |
| Channel link | verified | owner revokes | revoked |
| Channel link | verified | human deleted | gone |
| Admin grant | active | administrator revokes | revoked |
| Admin grant | active | expiry reached | expired |

Inactive principals and terminal links/grants cannot be resurrected. Human
deactivation removes owned profiles and space mappings, disables owned agent
principals, retires pending/verified links and revokes issued/received admin
grants in one audited transaction. Principal tombstones preserve history and
prevent accidental ID reuse; workspace files are not deleted.

Channel challenges last at most ten minutes, store only a SHA-256 proof hash
and are consumed once. Verification binds both proof and the account observed
by verified channel ingress; a caller-supplied display name is never proof.
Only verified links to enabled humans resolve. The unique verified-channel
index prevents concurrent owners claiming one account. Proof expiry is
currently materialized when verification is attempted; the control upkeep
sweep and edge proof transport remain part of the cutover.

## Legacy migration matrix

| Legacy input | Registry mapping | Access change |
|---|---|---|
| User ID / private space `<user>` | human `<user>`, space `<user>` | None |
| Same user | owned default agent `agent:<user>:default` | Constraints inherit the existing passport ceiling |
| Telegram / Slack account | exact verified channel binding | No name/email matching |
| Disabled user | inactive human and owned agent | Channel resolution denies |
| Organization `<org>` | org membership `member`, shared space `<org>` | No administrator or tool grant added |
| State/workspace paths, configured environment, features | Excluded from registry import/digest | Existing runtime/passport owners remain authoritative |

Imports sort and hash only their allowlisted identity inputs. An identical
re-import is idempotent; changed identity inputs or duplicate account ownership
refuse without partial writes. Verified OIDC subjects/provenance and operator
CLI migration are still pending. No live user files were read or migrated to
produce the local synthetic verification receipts.
