---
description: Frozen M32 requirements for six authored passport inputs and independent tool/grant authority.
last_verified: "2026-11-15"
---
# SPEC-0095: Authored passports and independent rights

Frozen: 2026-11-04. Implementation in progress.
Decision: [ADR-0096](../../docs/adr/ADR-0096-authored-passport-authority.md).
Supersedes SPEC-0091/0092 source-layer, migration and legacy-reader clauses.
Their six body names, catalog validation and immutable publication contract
remain where consistent with this specification.

## R1. Exactly six authored inputs

- **R1.1** A space's editable agent configuration is exactly
  `spaces/<user>/passports/{stack,config,models,workspace,tools,grants}.yaml`.
  Templates have the same six. Every existing legacy config field is assigned
  to one schema with a consumer or removed as obsolete; unknown fields fail.
- **R1.2** Apply reads only the six files, validates cross-file constraints,
  issuer and ceilings, then atomically publishes one immutable revision.
  Build, render, spawn, ToolHub, relay and services consume that revision
  alone. Partial apply and mixed revisions fail closed.
- **R1.3** Missing, malformed or unpublished passports prevent startup with a
  specific error. There is no legacy fallback, automatic conversion,
  `passport-src` authority or two-way sync. Generated config, secrets and
  protected ledgers are not agent-authored inputs. User data is retained.
- **R1.4** Agent edits require a distinct current right per passport and a
  verified publisher action. Generic file or terminal writes cannot make
  configuration effective. Issuers cannot exceed platform/org ceilings.

## R2. Independent tools, grants and memory

- **R2.1** `tools.yaml` alone selects tools; `grants.yaml` independently
  records intended rights. Apply never grants from selected toolsets or
  erases selections on revocation. The protected ledger accepts only
  issuer-validated grants.
- **R2.2** `tools.use` gates model calls; `tools.configure` gates edits to
  selected tools. Revoking either leaves the other unchanged. Each other
  passport has an independent edit right.
- **R2.3** `config.memory.enabled` gates capture, recall, memory tools and
  dreaming. Memory search, remember, edit and dreaming grants are separate.
  A selected memory tool is inert when memory is disabled or its grant revoked.
  ToolHub checks each call and asynchronous commit.
- **R2.4** Disabling `tools.use` blocks direct and indirect model tool calls.
  Memory capture and dreaming depend on memory enablement and service grants,
  independently of model tool visibility.
- **R2.5** Projection and admission share the published revision and revoke
  epoch; stale sessions cannot use revoked tools.

## R3. Cutover

- **R3.1** Old config readers/writers, migration commands and fixtures are
  deleted in the same release. There is no compatibility path. Existing
  spaces require six authored files before restart; workspace/vault data stay.
- **R3.2** Agent tool contract changes include description, prompt contract,
  docs/tools.md, docs/routines.md and bumped immutable catalog in one
  changeset, followed by rebind and respawn of managed spaces.
- **R3.3** User-visible strings have locale keys in en/ru/kk/es. Diagnostics
  expose allowlisted safe fields only.

## Acceptance

- **A1:** Template init creates six authored passports. Tool selection changes
  without changing grants; unauthenticated grant edits have no effect.
- **A2:** Revoke `tools.configure`: already selected tools still work. Revoke
  `tools.use`: direct/indirect calls stop. Disable memory with its tools
  selected: capture, recall, calls and dreaming stop.
- **A3:** Every old config reader refuses an absent six-file set. Old
  `agent.yaml` or `passport-src` cannot affect behavior. Render, spawn,
  runtime and ToolHub use one published revision.
- **A4:** Self-grant through files, terminal, passport mutation or stale
  session fails at publisher and per-call gate.
- **A5:** `just check` passes with own Go coverage >=85%; relevant Docker
  and two-user live gates prove managed behavior.
