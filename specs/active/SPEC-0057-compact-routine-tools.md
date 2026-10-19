# SPEC-0057: Compact routine tools

Frozen: 2026-10-18. Amend SPEC-0056 public tool naming only; its lifecycle,
isolation, recurrence and delivery guarantees remain required.

- R1: Current public MCP and ToolHub catalogs advertise exactly five routine_*
  tools: list, get, create, edit, delete.
- R2: edit requires ID, expected_revision and an explicit action. update requires
  a nonempty patch; pause/resume reject patch/until; snooze requires until and
  rejects patch. Unknown actions fail before HTTP. Existing HTTP revision fences
  and error propagation remain intact. Delete remains separately authorized.
- R3: get accepts include=[history,next_runs], history_limit (1..100, default 10),
  history_cursor and preview_limit (1..20, default 5). No include means card only.
  Reject unknown includes and invalid limits before requests. History uses its
  existing page shape; next_runs contains existing preview state/timezone/times.
- R4: list accepts optional case-insensitive query on ID and input, combined with
  kind/state and stable ID pagination inside the authorized context.
- R5: Catalog version 3.0.0 is immutable; migration never silently broadens a
  managed profile. Old private operations continue to serve pinned old catalogs.

Acceptance: MCP advertisements and dispatched arguments; invalid action and
read options cause zero requests; all four edits retain revision conflicts;
combined get propagates component failure; scoped search filters before paging;
just check >=85 percent own Go coverage; deployed dev catalog/executor smoke.
