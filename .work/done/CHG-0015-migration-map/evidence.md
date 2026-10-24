---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0015 migration and deprecation map

The current-to-target map assigns the live execution, generated MCP, connector catalog,
self-service, credential, state and ToolHive paths one `keep`, `adapt`, `replace` or
`retire` decision. Each row records coexistence, rollback, an evidence-based deletion
gate and its downstream implementation issue. Rollout is per runtime/connector and
never performs startup-time data migration or dual tool execution.

On 2026-09-11, the documentation and workflow gate passed after the map was added. Two
subsequent full `just check` attempts did not complete because `go test -race` stalled
without output and were stopped; they are not claimed as passing. The immediately prior
CHG-0014 full gate passed in the same worktree with 85.03% coverage, and CHG-0015 changed
documentation only. No dependency/runtime change required security or Docker gates.
