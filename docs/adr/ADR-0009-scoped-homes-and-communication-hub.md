---
description: Use peer organization and user homes with a separate communication-hub.
last_verified: 2026-09-10
---
# ADR-0009: Scoped homes and communication-hub

Accepted 2026-09-10.

## Decision

Use `spaces/<scope-id>` for both organization and user homes. Each home declares its
kind, and IDs are unique across the namespace. For an organization member, the
organization is the shared baseline and the user home is the personal layer. Memory,
skills, sessions, hooks, plugins, connection state and work files may be owned by
either scope; every persisted object has exactly one owner.

User conversations create user-owned state by default. Organization-owned writes run
only under an immutable organization scope selected by the trusted control plane and
authorized by organization policy. Organization skills use Hermes' supported external
skill directories; other shared data is exposed with explicit provenance rather than
by silently patching upstream Hermes.

Rename the channel service to `communication-hub` and deploy it separately from
`hermes-runtime`. The communication service owns channels, identity mapping, queue and
delivery. The runtime owns scoped configuration and Hermes execution. Only the runtime
mounts scope homes and provider credentials; only the communication service receives
the bot token.

Retain a bounded file queue for the single-host release. Move it to a gateway-owned
volume outside scope homes. Migrate current host directories and Docker volumes only
through an explicit, verified command with a dry-run default and recoverable source.

## Consequences

A complete scope can be inspected and backed up from one directory. The same model
supports personal and shared organization agents without mixing ownership. The
container boundary prevents channel credentials and routing configuration from being
available to Hermes.

This decision partially supersedes ADR-0002, ADR-0004 and ADR-0007 where they place
organization files outside `spaces/`, keep mutable scope data only in Docker volumes,
or name the service `hub-communication`.

## Related records

SPEC-0009 and CHG-0009.
