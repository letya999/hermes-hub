---
description: Bounded owner-only JSON session state in dynamically registered Broker contracts.
last_verified: 2026-10-20
---
# ADR-0071: Dynamic owner session contracts

Status: Accepted (implements the owner's dynamic OAuth installation request).

## Context

ADR-0067 deliberately excluded all state from dynamic contracts. Real Workspace
installation exposes that restriction: reviewed OAuth recipes cannot register
their session contract through the normal installer. ADR-0070 alone does not
change the Broker registration boundary.

## Decision

Extend `broker:control` registration only to one JSON session file, at most
65536 bytes, with `allow_shared=false`. Existing contract path validation,
immutable revision/digest, durable audit, owner enrollment, grants, isolated
leases, single-writer state, encrypted checkpoint and restore remain required.
Dynamic raw files, multiple state files, shared state, routes and Broker OAuth
provider registration remain forbidden. No new delivery materializer is added.

ToolHub supplies reviewed recipe metadata; repository content cannot issue a
grant. The owner still submits client credentials through the protected form
and consents to Google access separately. This narrowly extends ADR-0067's
state restriction; its other boundaries remain unchanged.

## Consequences

New compatible OAuth MCPs can install without an operator copying a contract
file or restarting Broker. The registration API accepts more declarative data,
but cannot run a container, infer provider protocols or collect arbitrary state.
Regression tests cover the allowed session and rejected state variants.
