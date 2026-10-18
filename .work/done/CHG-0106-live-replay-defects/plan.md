---
title: Live-replay defect follow-up (watchers, broker contracts, session leak, retry, latency)
status: in-progress
created: 2026-10-16
---

# CHG-0106 — live Telegram replay defect follow-up

Continuation of the live Telegram replay after CHG-0105. Six defects found in
the real conversation (germi group topic 66 / DM):

1. Watchers silently dead:
   - 1.1 agent created watchers that could never work and reported success.
   - 1.2 watcher on `artemletyushev.notion.site/...` and github/vc/habr/dzen
     never fired despite page changes.
   Root causes found:
   - `fetchWatch` capped bodies at 256 KiB; real pages are 300-520 KB → every
     probe failed (`probe body exceeds size limit`).
   - `watchValue` returned capture group 1 unconditionally — an alternation
     regex matching a later group produced an empty value forever
     (baseline became sha256("")).
   - `settleProbe` recorded the constant `probe failed; baseline unchanged`
     instead of the real error → invisible in `routine_list`.
   - Notion public pages serve a 20 KB JS shell — HTML probes can never see
     content edits; agent must use `probe_kind=mcp_tool` or a static marker.
2. `google_workspace_mcp` failed with `no reviewed credential broker contract
   covers google_workspace_mcp credentials` — message named no credentials and
   conflated contract-miss with other `selfInstallUsable` failures.
3. Async `prepare_source` notifications leaked to the owner DM: principal-only
   `/v1/prepare-outcome` callbacks synthesize a DM envelope. Fix: persist the
   originating message mapping keyed by onboarding id (`spool/prepares/`),
   route notices/continuations to the origin chat+topic with
   `trigger=prepare` preserved.
4. `notion-mcp-server`: definition pins contract `notion-token` rev 1 which is
   missing from the local broker catalog → `CreateRequest` →
   `invalid_request (400)` on every status call. Fix: restore the contract
   file in `spaces/local/broker/contracts/` and name the rejected contract in
   the surfaced error. ("ToolHub unreachable" after the form was a transient
   MCP call failure during a busy build window; no durable defect found.)
5. `run.failed` for `telegram-837845489` after upstream 503s: a plain failed
   message job is retried once (`-r1` id, fresh idempotency key, old status
   message closed); uncertain/cancelled/interrupted and non-message triggers
   are never retried.
6. Latency: supervisor `/v1/jobs` runs take 30-190 s (model + tool time);
   hub adds only 1-5 s poll cadence. No hub-side defect beyond that.

## Done-when
- New regression tests green; `just check` passes.
- Local stack rebuilt via `hubctl build`/`hubctl up`; live replay confirms:
  group-topic prepare notices stay in the topic, github-issues watcher probes
  succeed, notion onboarding mints a broker request, a failed message is
  retried once.
- Broker catalog `notion-token` restored in `spaces/local/broker/contracts/`.
