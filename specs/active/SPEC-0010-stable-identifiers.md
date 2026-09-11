---
status: active
title: Stable identity, context and ownership identifiers
---

# Identifier contract

| Record | Cardinality and owner | Lifecycle | May cross |
|---|---|---|---|
| `principal_id` | One per human; control-plane owned | Immutable; never reused | Gateway, policy and runtime envelope |
| `external_identity_id` | Many per principal; `(issuer, subject)` unique | Verified link; independently revoked | Ingress and identity registry only |
| `context_id` | Personal or organization context; members may share an organization context | Immutable; deletion tombstoned | Gateway, runtime, ToolHub and audit |
| `runtime_id` | One logical runtime binding per `(principal_id, context_id, mode)` | Stable across restart/rebuild; replaced only with the binding | Supervisor, runtime and ToolHub |
| `conversation_id` | One normalized thread owned by a context | Stable across messages; channel deletion may tombstone it | Gateway and runtime envelope |
| `delivery_target_id` | One verified audience owned by a conversation | Rotated when channel routing changes | Gateway only |
| `connection_id` | One provider account/workspace owned by a principal or context | Stable across credential rotation | Control plane and ToolHub |
| `credential_ref` | One secret reference owned by a connection | New revision on rotation; never contains secret material | Credential broker and connector workload |
| `tool_binding_id` | One authorized definition/connection/context binding | Replaced when binding changes | Policy resolver, ToolHub and audit |
| `policy_version` | One immutable effective-policy digest | Recomputed when effective policy changes | Every authorized job/tool call and audit record |

All persisted owner-bearing records contain their owner type and ID. Memory, sessions,
workspace and browser state are owned by `context_id`; personal credentials are owned
by `principal_id` through `connection_id`; shared credentials are owned by the
organization context. A conversation owns its delivery audience but never grants a
provider connection. Tool list and tool call use the same binding and policy version.

Deletion writes a tombstone before owned storage is removed; raw IDs are never
reassigned. Credential rotation keeps `connection_id` and replaces `credential_ref`.
Reauthorization creates a new binding when its owner, connection or tool revision
changes. Completed provider mutations remain historical facts after unlink/revocation.

# Trust and validation rules

1. The authenticated transport resolves `external_identity_id` to `principal_id` and
   allowed contexts before accepting work. Payloads and model arguments cannot set or
   override identity, context, connection, credential, binding or policy IDs.
2. Account linking requires both an authenticated principal session and fresh issuer
   proof (Telegram login/bot challenge, Slack/OIDC authorization code with state and
   PKCE where supported). Email text or matching display names are insufficient.
3. Each service validates type, syntax, existence, owner relationship and current
   policy at its boundary. Unknown, stale, malformed and cross-owner IDs fail closed.
4. Logs may contain typed IDs and policy versions but not channel payloads, issuer
   tokens, credential values or provider content by default.
5. Raw IDs match `[a-z][a-z0-9_-]{0,39}`, are opaque, canonical lowercase, unique per
   record type and never reused. A typed reference is `<type>:<raw-id>`; the colon and
   type are serialization, not part of the raw ID.

# Service-boundary envelope

| Boundary | Required IDs | Rejected before |
|---|---|---|
| Channel → gateway | issuer/subject plus channel conversation/audience | principal lookup and job creation |
| Gateway → runtime | identity schema, principal, external identity, context, runtime, conversation, delivery target and policy version | filesystem selection or Hermes execution |
| Runtime → ToolHub | principal, context, runtime, tool binding and policy version | tool resolution or credential lookup |
| ToolHub → connector | connection, credential reference, binding and policy version | workload start or provider call |
| Gateway → delivery | conversation and delivery target | external send |

The current gateway/runtime boundary is schema 1. Missing, malformed, stale or
mismatched canonical fields are rejected; compatibility fields must resolve to the
same principal and context.

# Current compatibility mapping

The current single-user deployment needs no data migration:

| Current value | Canonical meaning |
|---|---|
| `spaces/artem/scope.yaml` ID `artem` | personal space key and `principal_id=artem` |
| job `user_id=artem`, `actor_id=artem` | authenticated `principal_id=artem` |
| job `scope_id=user:artem` | typed compatibility reference for raw `context_id=artem` |
| configured runtime for `artem` | raw `runtime_id=artem`, derived from deployment binding, not prompt |
| Telegram numeric sender/chat/update IDs | external subject, delivery target and idempotency metadata; never primary keys |

A later registry may assign different opaque principal/context IDs while retaining
`spaces/<id>` as an immutable storage key. That is an additive mapping, not a rewrite
of homes, sessions, memory or credentials.

The current `scope.yaml.organization` selects one deployment context; it is not the
future membership record. Multi-organization membership is a separate relation from
one unchanged principal to multiple existing context IDs, so it does not rename the
principal, contexts or their homes.

# Examples

- Personal Telegram DM: verified issuer/subject `telegram_bot`/`12345` becomes raw ID
  `telegram-12345` for the external identity, conversation and delivery target of
  principal and personal context `artem`.
- Slack DM: verified issuer/subject `slack`/`t01-u02` links to the same `artem`; it gets a distinct
  conversation and delivery target but the same personal context.
- Google OAuth: connection `google-work` is owned by `artem`; `credential_ref` points
  to its refresh-token revision and is usable only through an authorized tool binding.
- Company work: `artem` in context `org-acme` uses a separate runtime, memory, sessions,
  workspace, policy version and organization-owned connections.
- Two organizations: the same `artem` principal may enter `org-acme` and `org-beta`,
  but each tuple resolves to different runtime and tool bindings; neither context can
  reference the other's memory, credentials, conversation or delivery target.

# Verification

Contract and implementation tests must reject malformed IDs, forged external links,
cross-context connection IDs, stale policy/binding versions and runtime/context
mismatches before filesystem or credential access. Restart and rebuild must preserve
the logical runtime ID; replacing its binding may assign a new ID while context-owned
data remains unchanged.

# Out of scope

This contract does not add ZITADEL, RBAC administration, PostgreSQL, multiple active
organizations, Slack transport or a v2 public API.
