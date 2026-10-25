---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0014 threat model

The personal and future-organization threat model records the authenticated channel,
Hermes, Go ToolHub, internal vMCP, ToolHive Runtime and provider boundaries. Every
required abuse case maps to a fail-closed control, non-secret audit evidence and an
implementation issue that owns the regression test. ADR-0013 makes Go authoritative
for identity, current binding/policy and credential ownership; no ToolHive or provider
integration success is claimed by this documentation change.

On 2026-09-11, `just check` passed with 85.03% original Go statement coverage,
including race tests, formatting, vet, staticcheck, documentation and workflow checks.
No dependencies or runtime behavior changed, so security and Docker gates were not
required for CHG-0014.
