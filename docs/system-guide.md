---
description: Mandatory English system reference, delivery paths and evidence boundaries.
last_verified: 2026-10-24
---
# System guide

The canonical English skill is
[`internal/systemguide/SKILL.md`](../internal/systemguide/SKILL.md), embedded by
`internal/systemguide/guide.go`. Edit that source rather than maintaining copies
of the glossary in several skills. It contains no deployment data or credentials.

## Delivery

| Surface | Implementation |
|---|---|
| ToolHub | Every authenticated MCP projection includes static read tool `system_guide`, even without connector control grants. The tool does not read account state or call a provider. Projection refresh preserves it. |
| Unmanaged Hermes | `MaterializeGlobalSkills` adds the exact embedded skill to `generated/skills/hermes-hub-system-guide/SKILL.md`; Compose and supervisor mount the filtered catalog read-only. The native search path includes `/opt/hub/skills`. |
| Managed Hermes | Uses ToolHub's reviewed reference surface. Native skills remain disabled; no general loader or plugin permission is added. |
| Session instruction | `ChannelContract` routes terminology/inventory/access questions to the guide and forbids readiness claims without evidence. `MaterializeSoulEffective` appends it to the owner persona for both environments/modes. Supervisor config materialization refreshes it at spawn. |

The mandatory reference does not depend on feature flags or a custom skill
catalog. The reserved `hermes-hub-system-guide` directory in an optional catalog
is skipped entirely, so it cannot add instructions or replace the reference.
Optional catalog failures are still reported. Existing running processes
need the updated binaries and normal render/restart or next supervised spawn;
changing repository files does not itself update live sessions. Live deployment
acceptance (new supervised spawn on the current dev image, real model answers to
the five behavioral scenarios) was verified on 2026-10-24 in CHG-0149.

## Behavioral contract

An unqualified question about MCP/connectors includes adjacent tool formats,
clearly labelled; explicit MCP-only filters are honored. Catalog, operation
history, onboarding and current connections remain separate. `done`, `active`,
admitted/projected, workload ready and provider verified are separate facts.
Missing inventory fields must be reported as unknown. This guide cannot repair
incomplete inventory APIs by itself; those changes are tracked in SPEC-0069.

The guide describes safe diagnosis, not a promise of implemented automatic
recovery. A scoped Broker 404 is ambiguous. Restoration must preserve ownership,
environment, revocations and current authorization. Backup/recovery implementation
and live incident remediation remain separate acceptance requirements.

## Verification

- `internal/toolhub/system_guide_test.go`: two authenticated users without
  connector grants can read the reference; no provider dispatch occurs.
- Existing control/managed revocation tests require that only this public
  reference remains when application permissions are removed.
- `internal/stack/system_guide_test.go`: native materialization, custom/missing
  catalogs, read-only mounts, managed loader exclusion and persona preservation.
- `internal/supervisor/system_guide_test.go`: existing normal/managed contexts
  receive the current generated contract during spawn preparation.

Rules/decisions: ADR-0072. Requirements: SPEC-0069. Progress and limitations:
[CHG-0149](../.work/done/CHG-0149-connector-recovery-and-guide/plan.md).
