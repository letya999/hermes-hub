# CHG-0114: Routine destinations

ADR-0044 -> SPEC-0058; preserve dirty branch and prior task changes.

1. Red/green owned destination binding and origin mapping resolution, anchored in
   schedules.go create, job_mapping.go, tasks.go and HTTP create validation.
2. Red/green revision-fenced destination edits and prior audience history;
   routine_management.go/history.go/occurrences.go. Test archive/revocation,
   foreign context, forged fields, concurrent edits and retained old outcomes.
3. Red/green destination discovery/card and public adapter/schema 4.0.0; retain
   five tools, update prompt guidance, HTTP runner per-run origin and fixtures.
4. Update current docs and E2E contracts. Run just check, review assertions with
   retained author context, scan secrets. Rebuild affected dev images through
   hubctl, register current catalog, verify actual MCP and cleanup future fixtures.

No publication, production rollout, new dependency or upstream patch. Separate
automated Bot API transport evidence from actual external Telegram sends. Avoid
an extra external send unless needed and within the owner's sending instruction.
