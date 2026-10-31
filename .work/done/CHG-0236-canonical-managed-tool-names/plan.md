# CHG-0236 — Canonical managed tool names (inbound fetch + catalog apply)

## Problem

Two naming defects broke the file/document pipeline on managed spaces:

1. `internal/runtime/inbound.go` resolved the inbox tool via
   `api.ProjectedToolName` (`hub-hub-agent-tools-inbox-fetch-…`), while managed
   capability profiles expose `inbox_fetch` under its canonical name — any
   republished selection made the runtime admission fetch fail with
   `unknown tool`.
2. `internal/toolhub/catalog_operator.go` `EnableCatalog` wrote selection
   `Name = definitionID_toolName` (`hub-agent-tools_assignment_progress`), so
   every catalog-apply landed tools under names nothing can call.

## Fix

- `fetchInbound` calls canonical `inbox_fetch` first and falls back to the
  legacy projected name for unmanaged/stale projections.
- `EnableCatalog` publishes `Name = tool.Name`.
- Regression tests: canonical lookup, legacy fallback, catalog-apply name
  parity (`Name == ToolName`).

## Live remediation (same changeset, deployment state)

- `hub-agent-tools` 5.9.0 registered + active bindings for `artem_two`,
  `artem_main`; profiles republished with all 83 catalog selections under
  canonical names, full allow set, `connectors: personal+team`; policy ceilings
  extended to every selected tuple.
- MCP write enablement confirmed through the human confirmation flow for both
  `artem_two` onboardings; 59 write-effect tools now project live.
- Runtime/toolhub dev overlays rebuilt; `artem_two` respawned on
  `hermes-hub-dev-artem_two-hermes-runtime:local` (gen-479).
- Verified live: `tools/list` 164 tools, zero mangled managed names;
  `inbox_fetch` end-to-end (stage → materialize `/workspace/inbox/<id>/files`
  → consume); `document_create/edit/extract/convert`; `POST /v1/artifact` 200;
  `context_status` dispatch.
- 8 orphaned inbound spool records (pre-migration `user=local`, Oct 13)
  quarantined to `inbound-quarantine-2026-10-30/` in the gateway data volume.
