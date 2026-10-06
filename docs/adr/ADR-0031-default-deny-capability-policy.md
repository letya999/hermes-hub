---
description: Accepted default-deny capability policy, canonical identities and one agent-facing ToolHub boundary.
last_verified: 2026-10-02
---
# ADR-0031: Default-deny capabilities through one ToolHub boundary

Status: accepted, 2026-10-02. Issue 122; owner instructed implementation according
to the ADRs, specifications and plan. Acceptance is not an implementation claim.

## Context

SPEC-0040 describes a baseline with always-on native tools. The owner's clarified
requirement is a zero-capability baseline, followed by an organization default
profile and explicitly authorized user grants. Hiding a tool is insufficient:
direct calls, aliases, composite calls and extension installation must not bypass
the same policy. Current findings and upstream evidence are in the
[audit](../capability-boundary-audit.md).

## Decision

1. Extend the existing Go ToolHub definition, binding, grant and audit machinery.
   Do not introduce another registry service, policy database, queue, or policy DSL.
   Use default deny and explicit-deny precedence, with a small typed policy model.
   Cedar/OPA are references, not new dependencies required by this decision.
2. Start with no agent capabilities, including diagnostics, discovery, file reads,
   skills, terminal, configuration writes and connector lifecycle operations.
   An organization default is an explicit, versioned set of grants within its
   maximum permitted set. User grants may expand the default only within that
   ceiling; user restrictions may narrow it. A standalone deployment uses an
   operator-managed personal profile under the same rules, initially empty.
3. Give Hermes one authenticated ToolHub MCP endpoint for managed tools. Reuse
   existing local handlers and connector adapters behind it. A single endpoint
   does not imply a single executor process or one generic tool schema.
4. Separate a stable logical capability from its selected implementation,
   presentation name, group and provider. Authorization binds the capability,
   reviewed implementation revision, operation, resource and authenticated scope.
   A rename, group membership change or backend priority must not transfer grants.
   Group grants expand to reviewed members at a fixed revision, not future tools.
5. Use one policy evaluator for catalog projection and invocation. Search,
   descriptions, resources, prompts, composite subcalls, background jobs and
   lifecycle operations get equivalent checks. Unknown capabilities or changed
   schemas are quarantined until reviewed, even if their server is already trusted.
6. Operator administration stays outside the agent's MCP projection and bearer
   authority. A grant is not human consent for a specific mutation. Optional
   self-service workflows require both a grant and a separately authenticated,
   request-bound human confirmation; a model-supplied boolean is not confirmation.
7. Keep native Hermes implementations available as an architectural option, but
   enable one only after proving equivalent authorization, scope and revocation
   on the pinned upstream. In the initial managed profile, unproven native paths
   remain disabled. Do not silently patch Hermes or replace its reasoning loop.

The normative contract is [SPEC-0041](../../specs/active/SPEC-0041-default-deny-capabilities.md).
Execution isolation is a separate decision in
[ADR-0032](ADR-0032-capability-execution-isolation.md).

## Alternatives considered

| Alternative | Decision and reason |
|---|---|
| Empty YAML toolset lists and an MCP include filter | Insufficient: platform defaults, extension loading and direct/composite calls are separate paths |
| Keep hub, ToolHub and native tools with independent policies | Rejected as target: duplicates identity, grants, naming and revocation semantics |
| One universal invoke function | Optional presentation choice; not an authorization boundary and not required for consolidation |
| Replace Go ToolHub with current ToolHive vMCP | Deferred: preserve ADR-0024 until its compatibility gates pass; newer documentation is not deployment evidence |
| New Cedar/OPA service | Deferred: reuse the existing Go boundary and adopt policy semantics without a new operational dependency |

## Consequences and precedence

On acceptance this decision replaces SPEC-0040's always-on/default rows and
four-input static-catalog assumption, and SPEC-0023's default-allowed self-install.
It narrows the agent-visible self-service/configuration paths in ADR-0005,
ADR-0008 and ADR-0021; their human control and credential workflows remain useful.
It preserves ADR-0011 identity, ADR-0013 Go authorization, ADR-0016 credential
ownership and ADR-0022 broker separation. Historical records are not rewritten.

Existing deployments need a reviewed migration, not automatic conversion of every
installed tool into a grant. Existing assets remain stored when access is removed.
Native tools may temporarily remain unavailable if upstream cannot meet a gate.
No organization administrator, host root, or container-kernel compromise is claimed
to be constrained by this agent authorization model.

## Community evidence

- [ToolHive filtering and authorization](https://github.com/stacklok/toolhive/blob/main/docs/arch/10-virtual-mcp-architecture.md): advertising filters do not authorize calls; names and backend ownership can diverge.
- [ToolHive policy semantics](https://github.com/stacklok/toolhive/blob/main/docs/authz.md): deny precedence and absence-of-permit denial.
- [MCP security guidance](https://modelcontextprotocol.io/docs/2025-11-25/tutorials/security/security_best_practices): authenticated request boundaries and separate upstream credentials.

Sources were checked through Context7 and GitHub on 2026-10-02. Moving upstream
documentation informs this design; the pinned Hermes artifact determines behavior.
