# CHG-0099 research — what already exists vs. what milestone 13 adds

Date: 2026-10-14. Base: origin/dev @ f06d6ea. Source issues: GitHub milestone
13 (M12 — Proactive Automation), issues #247 #248 #250 #251 #257.

## Existing machinery (verified in code)

- `internal/communication/schedules.go`: `Schedule` with
  `job_kind` ∈ agent|script|reminder|watcher|check|digest; `once:`/`after:`/
  cron expressions; `catch_up_window` default 1h; snooze/pause/delete;
  creation replay via `intent_sha256`.
- `internal/communication/occurrences.go`: two-phase dispatch —
  `pending` -> (reminder: direct Delivery; watcher/check: `probing`;
  script: `scripting`; digest: aggregate; agent: job admission). Occurrence
  id = sha256(schedule+revision+due) — dedup across restarts and workers.
- `internal/communication/watchers.go`: http(s) GET probe (256 KiB cap, 10 s,
  no-cache), regex first-capture value, `watchers/<id>.json` baseline +
  prev/current + last_error; `github_issue` probe kind. `check` anchors the
  baseline at creation inside `handleRoutines` (502 on unreachable source).
- `internal/communication/digests.go`: owner-scoped aggregation of changed
  watch states into ONE delivery; `digests/<id>.json` cursor; unavailable
  sources marked, errors deduped.
- `internal/communication/communication.go`: outbox re-validates occurrence
  currency + `authorizeOccurrence` at delivery time (`OccurrenceID` check ->
  `UncertainDelivery`); `outbox/sending` crash recovery -> failed.
- `internal/agenttools/routines.go`: `routine_create/list/update/snooze/
  pause/delete` -> `/v1/routines` control API (`X-Hub-Principal`).
- `internal/toolhub/gateway.go`: `CallAuthorized` exists (identical authz/
  audit fences as MCP `tools/call`) but there is NO HTTP endpoint for
  control-plane tool calls — only `/v1/connectors` and
  `/v1/cli-jobs/release`. `connectorAuth` already implements the
  bearer + `X-Hub-Principal` principal-selection contract.
- `internal/toolhub/prepared/catalog.json`: telegram entry `read_tools` does
  not include the fork's event tools yet (`wait_for_new_message` etc.);
  `stateful: false`.

## Issue-to-code gap analysis

- #247 reminders: implemented (SPEC-0048). Gap = evidence test + documented
  overdue/uncertain policy. -> S1.4.
- #251 follow-ups: `check` covers deterministic predicates. Gaps = bounded
  retry on probe error (today: straight `failed`, and `once:` makes that
  terminal), verdict text, hub-verifiable task predicate. -> S1.1-S1.3.
- #248 watchers: deterministic mode exists. Gaps = quiet hours, daily cap,
  semantic escalation (`notify_mode=agent`), connector sources. -> CHG-0100,
  CHG-0101.
- #250 brief: `digest` covers deterministic aggregation. Gap = opt-in LLM
  synthesis at the slot, task snapshot. -> CHG-0102.
- #257 event subscriptions: nothing. Gap = the whole sink fan-out, mcp_tool
  probes, watch_* surface, telegram event tools. -> CHG-0101, CHG-0103.

## Prior art named by the issues

- Iva `remind.ts`/`fire.ts`, `precheck.ts`, `Watch`/`Brief` skills:
  github.com/smixs/iva-agent @ ef7966c. Iva fires the LLM per reminder; the
  hub deliberately does not.
- telegram-mcp fork event tools: delivered outbox + `requeue_event`,
  settle-window debounce, `TELEGRAM_EVENT_FEED` — pinned source
  letya999/telegram-mcp @ 26f1632.
