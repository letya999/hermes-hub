---
description: Six editable passports are the sole space configuration authority; tool selection and grants are independent.
last_verified: 2026-11-04
---
# ADR-0096: Six authored passports and independent grants

Status: accepted — delivered M32, verified 2026-11-15.
Requirements: [SPEC-0095](../../specs/active/SPEC-0095-authored-passports-rights.md).
Amends ADR-0092 and ADR-0093. Those records remain immutable.

## Context

The six published passports currently materialize from `passport-src`
layers. `space apply` can derive grants from selected toolsets and old
ToolHub state. Managed runtime still reads an `agent.yaml` extension; the
unmanaged path reads legacy settings. The same user intent therefore has
multiple editable sources, and changing tool selection can change authority.

## Decisions

### D1. Six authored inputs

Each `spaces/<user>/passports/` contains exactly the authored
`stack.yaml`, `config.yaml`, `models.yaml`, `workspace.yaml`,
`tools.yaml` and `grants.yaml`. Templates contain the same six documents.
All declared agent configuration fields have exactly one home and a verified
consumer. Apply validates all six, issuer authority, ceilings and cross-file
constraints before atomically publishing one immutable compiled revision.
Services consume that revision only. A missing or invalid set fails startup.

Generated Hermes/Compose files, published revisions, the protected rights
ledger, secrets and runtime state are derived output or protected data. They
cannot serve as alternative editable agent configuration.

### D2. Breaking cutover

Remove `passport-src`, `agent.yaml`, `workspace.yaml`, `settings.yaml`,
`scope.yaml` and other legacy per-space configuration readers and writers
after their fields are mapped into the six passports. Remove migration
commands, automatic conversion and compatibility branches. Existing spaces
need six authored passports before restart; the program reports missing
fields precisely. The cutover never deletes personal workspace or vault data.

### D3. Selection and authority

`tools.yaml` selects and configures the tool surface. `grants.yaml`
records independently issued rights. Selection cannot mint a grant;
revocation cannot erase a selected tool. `tools.use` gates agent tool calls;
`tools.configure` gates edits to tool selection. Each other passport has
its own edit right. `memory.enabled` in `config.yaml` gates capture,
recall, search, write and dreaming independently of tool selection.
Operation grants for memory read/search/remember/edit/dreaming remain separate.
ToolHub evaluates current published rights and enablement on every call and
again before an asynchronous commit.

### D4. No self-granting

An authored `grants.yaml` is an intended rights document, not an
authorization token. Publishing checks the authenticated issuer, applicable
platform/org ceiling and current grant epoch. The agent cannot grant itself
an edit right or bypass the publisher with a file tool. The protected ledger
is updated only after a valid whole-set publish and remains the PDP's source
for per-call decisions.

## Alternatives

Keeping the derived source layers would preserve a second authoring language.
Linking toolset selection to grant derivation would conflate visibility and
authority. Both are retired without a compatibility mode.

## Invariants

The grant ceiling narrows org/platform policy. Revocation is effective before
the next call. No secret value is stored in passports, logs or tool results.
Generated upstream files are outputs and never silently edited as inputs.

## Implementation clarification (2026-11-04)

The protected ledger is issued independently of passport publication.
Publishing `grants.yaml` checks active issued grants and narrows the managed
profile; it never issues or revokes ledger grants. An organization policy's
confirmed `right_ceiling` independently caps profile publication, authored
apply and per-call decisions. Managed calls also bind the policy organization
to the authenticated envelope.
