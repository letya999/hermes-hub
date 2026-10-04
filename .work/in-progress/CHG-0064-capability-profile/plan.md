---
description: Standard user-scoped capability profile for Hermes — frozen matrix and catalog invariants.
last_verified: 2026-10-02
---

# CHG-0064 Capability profile

Issue 122.

## Change

- Freeze the per-capability matrix (availability, surface, read/write,
  credential, state, network, resource weight, revocation) in SPEC-0040.
- Reserve native toolset names in `validateMCP` so an owner-configured
  `mcp_servers` entry cannot publish a second surface for a native
  capability (web, terminal, file, skills, todo, cronjob, messaging,
  memory, session_search, google_meet, vision, image_gen).
- One proof test locks the catalog invariants: deterministic render, a
  single surface per capability, opt-in entries absent until enabled and
  dropped on revoke, feature dependency closure.
- Operator/user docs stay in the spec plus the README capabilities table;
  no second catalog is introduced.

## Contract

[SPEC-0040](../../../specs/active/SPEC-0040-capability-profile.md).
Enforcement points already live in SPEC-0032 (SSH), SPEC-0033
(document/image), SPEC-0034 (diagnostics), SPEC-0023 (ToolHub control
plane) and the org overlay (SPEC-0004); this change does not move them.

## Verification

Targeted `go test ./internal/stack/` for the new profile test plus the
existing render/scope suites, then `devcheck docs` for the index updates.
No live provider evidence is claimed; catalog invariants are host-render
properties.
