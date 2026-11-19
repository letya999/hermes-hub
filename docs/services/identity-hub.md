---
description: Control-zone identity registry — principals, profiles, narrowing layers, verified channels and explicit legacy migration.
last_verified: 2026-11-20
---

# Identity registry module

`internal/identityhub` implements ADR-0106's registry in the supervisor/control
process. `contracts/identity/v1` exposes principal/channel reads, owned-agent
templates and a separate verified-ingress link interface. Local verification
and remaining acceptance are tracked in
[CHG-0331](../../.work/done/CHG-0331-identity-hub/plan.md).
The existing optional ZITADEL container is an authentication source; this
module does not replace that deployment. Gateway configuration retains routing
paths and passport metadata; its channel lists are migration inputs, never a
fallback after registry refusal.

## State and boundaries

`Open(path)` uses `internal/sqlitedb`, schema 4, a private SQLite file and WAL.
An existing file must carry the registry's `HID1` application ID and schema
version. Earlier registry schemas upgrade transactionally without rewriting
identity/import receipts. A read-only probe checks fixed metadata before WAL is
enabled; a foreign SQLite store or a symlink is refused. Registry tables hold
principals, organizations, memberships, owned profiles, space mappings,
layers, shared-space ACLs, exact OIDC subjects, import receipts, channel links,
administrative grants and audit.

Principal kinds are human, agent and system. An agent has exactly one human
owner. It can act in its assigned private space, explicit shared-space ACLs,
or its organization's company space with current membership. A bare agent
cannot impersonate its human or inherit an administrative role. `CanAct` and
`Template` share the same database predicate. System principals have no owner
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

Supervisor resolves the template before admission/spawn and records its digest
beside the runtime generation. A changed digest replaces the stale generation;
drift during materialization refuses the spawn. Skills narrow the published
readiness/pin ceiling. Narrowed profiles use separate generated view/meta/SOUL
paths, including environment and passport revision; parallel profiles cannot
replace each other's mounted skill view. Post-publish rebuild replays the same
nullable skill ceiling: null inherits, empty denies all.

Grants, guardrail references, memory scopes and action mode narrow the
published passport/PDP ceiling. The registry uses the same effective template
when deriving a signed policy for an acting agent.

## Control and edge wiring

`Serve` owns `<StateDir>/identity.<dev|prod>.sqlite`; the default state directory
is `<spaces>/.control`. Listener failure occurs before registry creation or
worker startup. Shutdown joins upkeep/reconciliation and the HTTP server before
closing the owned registry; injected registries retain caller ownership.

Read routes use the control service bearer: `POST /v1/identity/resolve`,
`GET /v1/identity/principals/<id>`, `GET /v1/identity/bindings/<id>` and
`POST /v1/identity/template`. Binding results contain verified links only;
OIDC provenance is specific to the resolved channel.

Control initializes separate Ed25519 keys for run, link, service and policy
signatures. `GET /v1/identity/keys` returns public keys, purpose, key ID and
the verification cutoff for retired keys. The control-only
`POST /v1/admin/identity/keys/rotate` requires the separate admin credential
and the current environment. Rotation retains the old public keys for one
maximum run budget and records an audit event. Run-token issuance caps expiry
at the run deadline and the maximum run budget.

The control registry stores immutable canonical policy JSON by digest. The
six compiled passports must agree on space, environment and revision.
Activation compares the previous digest before changing the active policy
for a space and environment. A run policy derives from that publication and
the acting agent's current registry template. `GET /v1/identity/policies/<digest>`
returns the signed bytes for independent verification. Key rotation can renew
the signature without changing the content or digest.

An operator with `HUB_SUPERVISOR_ADMIN_AUTH` publishes through `space apply`.
The publisher connects to the local control endpoint on port 8876.
`HUB_PASSPORT_PUBLISHER_URL` can select another loopback port. The generated
`passports/current` file carries the signed digest and the apply journal
restores the control digest and generated view together. An unsigned offline
apply can prepare a new space. The deployed supervisor requires an active
signed control publication before it dispatches a run.

The `task/v1` verifier compares the token with the supervisor's durable job
record, current generation, deadline and revocation state. The supervisor
revokes an already bound actor on registry drift, and revokes a signed
admission if its base policy is replaced. After a durable Hermes admission
the private runtime delivery route receives separate audience tokens. The
read credential for relay verification exposes signing keys, policy bundles
and job authorization records. It cannot dispatch jobs or change policy.
The automation and memory-capture issuers derive separate proofs from
`HUB_COMMUNICATION_SERVICE_AUTH`. Only supervisor and Communication receive
this independent credential. Compose provisions a private
`communication-service.<env>.auth` in the infra space. A host supervisor must
use the credential for its own environment. Helm reads `communication-service-token`
from the control Secret. Signed `hubctl routine` uses the same environment variable.

ToolHub requests a per-body dreaming assertion from
`POST /v1/identity/dreaming-model-token`. The supervisor accepts the independent
`HUB_DREAMING_SERVICE_AUTH` credential only for its own environment and checks
that the caller's published digest is active for an enabled personal space.
Compose provisions `dreaming-service.<env>.auth` in the infra space for
supervisor and ToolHub. Helm reads the optional `dreaming-service-token` key
from the control Secret. Configure the same environment-specific credential in
both services when enabling dreaming.

The remaining service-token and deployment acceptance work was parked in
[CHG-0332](../../.work/done/CHG-0332-run-tokens/plan.md) (deferred
2026-11-20); v1 redoes run tokens on the new services in M40.14
([ADR-0106](../adr/ADR-0106-identity-layers-run-tokens.md)).

`POST /v1/identity/links/oidc` and `/v1/identity/links/revoke` require the
separate `HUB_IDENTITY_LINK_AUTH`, which must differ from service-control.
Browser-origin requests refuse. Only edge ingress after the existing
nonce/PKCE/code/ID-token verification may assert OIDC proof; these routes are
not agent tools. Subjects match exactly; account names/email never match.
An already mapped channel can acquire provenance for the same owner only.
Issuer and subject are each limited to 256 bytes, matching the request
envelope's provenance fields.

`hubctl user disable` installs an ingress hold, then calls
`POST /v1/admin/identity/deactivate` on supervisor. The route requires
`HUB_SUPERVISOR_ADMIN_AUTH` distinct from both service-control and verified
ingress credentials and refuses browser origins. Deactivation is global
within the selected environment and is safe to retry. Subsequent revocation
stages clean up the selected context. ToolHub pauses schedules by their
enrolled owner/context binding, so retired channel links do not block cleanup.

Supervised gateways use their control URL/bearer for reads by default. Explicit
`HUB_IDENTITY_URL`/`HUB_IDENTITY_AUTH` support other topology selections; missing
authority refuses startup. OIDC additionally requires the separate ingress
credential. Telegram, Slack, group ACLs, reverse lookups, owner notices and
ToolHub automation routing consult the registry. Registry outage/revocation
never falls back to old channel arrays or the automation routing file.

## Lifecycle tables

| Entity | From | Event | To |
|---|---|---|---|
| Human | active | delete | inactive |
| Channel link | pending | proof verified | verified |
| Channel link | pending | proof rejected/expired | gone |
| Channel link | verified | owner revokes | revoked |
| Channel link | verified | human deleted | gone |
| Channel link | verified | same-owner OIDC proof | verified, provenance attached |
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
index prevents concurrent owners claiming one account. Proof expiry
materializes on verification and in bounded control upkeep batches. Reproof
does not resurrect a terminal link; a fresh verified flow creates a new row.

## Legacy migration matrix

| Legacy input | Registry mapping | Access change |
|---|---|---|
| User ID / private space `<user>` | human `<user>`, space `<user>` | None |
| Same user | owned default agent `agent:<user>:default` | Constraints inherit the existing passport ceiling |
| Telegram / Slack account | exact verified channel binding | No name/email matching |
| Disabled user | inactive human and owned agent | Channel resolution denies |
| Organization `<org>` | org membership `member`, shared space `<org>` | No administrator or tool grant added |
| Dedicated group space | Shared resource with exact existing members | No invented human owner |
| Explicit group-only account | Opaque channel-only human and owned agent | No private home or org membership |
| Group with no explicit members | Snapshot of enabled legacy Telegram users | Later links/users do not widen membership implicitly |
| Disabled/retired account referenced as a new group guest | Import refuses atomically | No resurrection through a guest alias |
| Provisioned OIDC subject / verified legacy link | Exact issuer+subject ownership; per-link provenance | No fuzzy merge or new IdP federation |
| Verified legacy OIDC link targeting a disabled user | Entire import refuses atomically | Operator must exclude the stale link from the selected migration input before retrying |
| State/workspace paths, configured environment, features | Excluded from registry import/digest | Existing runtime/passport owners remain authoritative |

Imports sort and hash only their allowlisted identity inputs. An identical
re-import is idempotent; changed identity inputs or duplicate account ownership
refuse without partial writes. Disabled users' legacy channel arrays grant no
link ownership. Shared membership changes require scoped administration and
take effect at the next authorization check.

Run the explicit local operator command before starting consumers:

```text
hubctl identity migrate --config <legacy-communication.yaml> --control-dir <control-state> --env dev --import-id <receipt-id> [--oidc-links <legacy-iam-links.json>] [--apply]
```

Without `--apply`, it parses selected inputs and prints source counts only;
it does not open the registry or promise a conflict-free import. Apply checks
the current registry transactionally. `--legacy-env-user <id>` explicitly
imports only `TELEGRAM_ALLOWED_USERS`/`SLACK_ALLOWED_USERS` for a configured
private user whose channel arrays are empty. No auth/secret file is opened.
Sources are bounded regular files; symlinks refuse. Old OIDC JSON is read
without opening its writer/audit journal. Output is fixed counts, never IDs,
subjects, paths or credential dictionary keys. Source files and session IDs
are not rewritten. No live user files were read or migrated for local receipts.
