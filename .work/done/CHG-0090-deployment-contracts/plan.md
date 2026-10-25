# Deployment contracts for the incidents observed in this session

## Scope

Keep CHG-0088 and CHG-0089 changes and their red-before-fix regressions.
Add missing configuration and startup checks without new dependencies or enabling
optional integrations. Build-time checks use synthetic data; external-account and
model availability claims require live probes, never fixture-only evidence.

| Incident | Required prevention/evidence |
|---|---|
| MCP child loses routine URL, auth and caller | Explicit child env, full identity/policy forwarding, rendered and materialized contract tests, real stdio child canary in CI |
| Compose silently empties supervisor auth | Required interpolation and quiet config validation before builds/up; file/process precedence and missing-value regressions |
| Same bot in two stacks; shared provider alias collision | Read-only startup conflict check; do not stop or modify foreign stacks |
| Configured model absent from provider | Selected-model/catalog contract and actionable live diagnostics |
| Missing optional CLI artifact image | Preserve isolation errors; required base CLI images included in image CI, optional artifacts reported separately |
| Closed approval buttons and cancellation noise | Terminal deduplication, cleanup retries/ownership/file isolation and restart regressions |
| Read-only tree invokes old protected write | Current-request guidance and real listing evidence retained; no approval bypass |
| Reminder loses settings or wakes agent | Native reminder exact-output/once/no-agent regression plus real stdio routine call in CI |
| Dev/prod/personal credentials mix | Mode/profile matrix and no credential value output in diagnostics/CI |

## Research

Docker Compose documents required `${VAR:?message}` interpolation and validation
with `docker compose config --quiet`. MCP Python SDK stdio uses a minimal inherited
environment and explicitly merges server `env`. Research uses Docker and MCP primary
documentation through web and Context7; references go into current operations docs.

## Validation

One observed failing regression before each behavior fix. Full `just check` including
>=85% own Go coverage. Synthetic configuration gate in local checks and image CI;
real stdio child contract on both built targets. Keep the owner's currently running
stack and AI-STP data untouched; no external test messages or provider mutations.
