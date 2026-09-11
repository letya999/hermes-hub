---
description: Stable opaque identifiers for identity, context, runtime and connections.
last_verified: 2026-09-11
---
# ADR-0011: Stable identity and ownership identifiers

Accepted 2026-09-11.

## Decision

Use opaque, immutable IDs assigned by the trusted control plane. A channel or model
cannot supply them. Human identity (`principal_id`) is separate from verified channel
or login links (`external_identity_id`). Work runs in a personal or organization
`context_id`; `runtime_id` identifies one isolated Hermes execution environment within
that context. Conversations, delivery audiences, provider connections, credentials
and tool bindings each have their own owner and identifier.

The private job envelope carries both canonical IDs and the current v1 compatibility
fields. `user_id` and `actor_id` equal `principal_id`; `scope_id=user:<id>` is the
legacy typed reference for the raw personal `context_id=<id>`. The configured runtime
binding supplies `runtime_id`. Telegram chat/update values remain transport metadata,
never principal or context keys.

Raw IDs are lowercase ASCII tokens matching `[a-z][a-z0-9_-]{0,39}`. They are unique
within their record type and are never reused after deletion. Serialized references
may add a type outside the raw ID, for example `principal:artem`, `context:artem` or
`connection:google-work`. `credential_ref` is an opaque reference, not a secret.
`policy_version` is a content-derived immutable version used to authorize a job or
tool call.

Verified external identities are unique by `(issuer, subject)`. Linking requires an
authenticated principal session plus fresh proof from the external issuer; message
text, usernames, email display values and provider content cannot link an account.
Unlinking removes routing but does not delete the principal or silently transfer the
external identity.

## Consequences

One person can keep the same principal while using personal and organization contexts,
and can join two organizations without sharing runtime state. Existing `spaces/<id>`
homes remain valid. Authorization resolves IDs before opening files, selecting a
runtime, loading credentials or choosing a delivery audience and fails closed for
unknown, stale or cross-owner references.

No database or IAM server is added. Durable connection/binding registries are deferred
until ToolHub needs them; the active Telegram path uses the shared Go envelope now.

## Related records

SPEC-0010, ADR-0007, ADR-0009 and CHG-0011.
