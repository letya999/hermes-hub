---
description: Stateless official provider HTTPS calls retain the ToolHub authorization and ciphertext boundary.
last_verified: 2026-09-15
---
# ADR-0018: Stateless personal provider HTTPS calls behind ToolHub

Accepted 2026-09-15.

Calendar's narrow official REST surface does not need an additional SDK or
container. Add provider-api alongside, not instead of, the existing MCP/CLI
transports. Go executes immutable provider URLs inside the existing
AuthorizeProjected and ciphertext-injection fence. This transport permits only
stateless per-user definitions without mounts, requires declared credentials
and egress, and bounds output. Model inputs cannot select authority or endpoints.

Read and write definitions/scopes remain independent. Stateful account MCP
connectors retain their existing isolated workload contract; this decision does
not relax ToolHive enforcement or switch the generated Hermes default.

Consequences: no new Go dependency; direct REST contract regressions are required.
Provider account verification precedes binding. Refresh credentials must be
selected by owner plus connection plus provider, never the first token of an
owner. Local revocation precedes external cleanup to fail closed during outages.

Snapshot writers share an OS file lock and compare the loaded content digest
before replacement. A stale writer returns conflict instead of losing another
process's binding or revoke; reload and retry the operator action. Protected CLI
refresh/revoke additionally serialize token lifecycle on the connection lock.
