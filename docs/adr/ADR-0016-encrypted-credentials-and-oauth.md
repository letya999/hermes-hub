---
description: Ciphertext store, ToolHub ownership, OAuth broker and privacy-preserving audit.
last_verified: 2026-09-13
---
# ADR-0016: Encrypted credentials stay behind ToolHub locators

Accepted 2026-09-13.

## Decision

Keep ToolHub as the only ownership and authorization registry. Persist secret
values as AES-256-GCM ciphertext in a separate local store addressed by opaque
locators. The encryption key stays outside the repository, the ToolHub
snapshot and ciphertext backups. A Vault-named backend may later replace the
local files without changing exact-owner checks or `AuthorizeProjected`.

Inject decrypted material only after that fence, and only into the selected
workload. Chat `KEY=value` is intercepted before Hermes. OAuth
authorization-code+PKCE and device flow share one broker; tokens never enter
Hermes. Audit records who, runtime, connection, revision and outcome, not
prompts or secrets.

## Consequences

Generated MCP remains the default. ToolHub stays opt-in through
`HUB_TOOLHUB_*`. Personal-terminal exposure is an explicit reversible flag,
not the managed-secret default. Failed rotates become `degraded` and fail
closed. Live provider login, Vault product deployment, ZITADEL and Hermes
reconnect remain later work.

Related: ADR-0013, SPEC-0017, SPEC-0020, issues #27, #28, #47-#51.
