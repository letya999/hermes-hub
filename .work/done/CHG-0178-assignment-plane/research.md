# M23 research: standing assignments + lightweight watcher pipeline

Evidence gathered 2026-10-27 from GitHub milestone 23 (#324, #332 open;
#333–#336 closed as consolidated) and the local harness corpus under
`C:\Users\User\a_projects`.

## What already exists in hermes-hub

- `internal/routines` (leaf engine, deps-injected):
  durable `Schedule`/`RoutineOccurrence` under `spool/schedules|occurrences`,
  `CreationReplayLocked` idempotent create with intent-sha conflict detection,
  revision-CAS mutations (`ErrRoutineRevision`), blocked states with deduped
  owner notices, quarantine of unreadable records, missed-occurrence catch-up.
- `internal/routines/watchers.go`: durable `WatchState` (baseline sha-256,
  prev/current value, generation, failure dedup). `probeOne` dispatches
  kinds: `command`/`script` (isolated Starlark via runtime `/v1/script`,
  file access confined to watch root, `needs()` prefetch of read tools),
  `task:` internal, `mcp_tool` (ToolHub), `render`/`service` (registered
  endpoints), plain `http(s)` fetch (1 MiB cap). `.probe-result` receipts
  make settle replay-safe. `notify_mode=agent` enqueues an ordinary job
  carrying `[untrusted watcher evidence]`. Quiet windows + daily caps.
- `internal/communication/events.go`: durable `EventState` seen-set
  (cap 512), overflow counting + connector requeue by native id, per-poll
  fan-out cap 8, daily limit, deterministic delivery ids, `sink=agent`
  bundles events into one untrusted-evidence job.
- `internal/communication/streams.go`: `streamSupervisor` keeps one bounded
  long-poll loop per `delivery=stream` subscription; reloads durable state
  every poll; backoff ≤30s; clean shutdown; stale pause/cancel stops loop.
- Slack: `HandleSlackEvents` verifies HMAC, dedupes by event id, drops bot
  messages, maps sender→user envelope, enqueues ordinary jobs;
  `slackAPI.PostMessage` is the bounded outbound path.
- Control surface: `/v1/routines*` REST (auth = principal header + bearer),
  `internal/agenttools` thin clients (`routine_*`, `watch_*`), chat
  commands via `dispatchChannelCommand` (`/routines`, `/cancel`, ...).
- Spool: single-writer lock (`LockSpool`), `atomicJSON`, `recover()`
  reconciles in-flight work on restart; `Envelope` gates every mutation.

## Prior art survey (15 harnesses scanned; relevant mechanisms)

| Harness | Mechanism | What we take |
|---|---|---|
| iva-agent `scripts/proactive/{precheck,tick}.ts` | Source checks with no model; claim-before-wake; re-check unread state before send; urgent bypasses quiet; failure alerting throttled; injection gate | Strongest blueprint. Already-mapped analogues exist (baseline hash, quiet defer). We add: fence at effect point + urgent flag + filter script. |
| OpenDots `src/server/runner.ts` | Durable claims w/ leases; abort on ownership loss; ownership re-check every 100ms; restart ⇒ running→pending | Revalidation at effect point; our occurrenceCurrent+authorize check is the same idea done once, plus assignment-state fence. |
| open-dot `src/server/scheduler.ts` | Crons rebuilt from durable routines; job re-reads routine before running | We already reload schedule per tick (durable is authoritative). |
| open-duet `backend/app/team/needs_you.py` | "needs human" computed from state; stable keys + content hash; write only on change; no empty cards | Assignment status card derives from links, not stored blobs; dedup of attention items. |
| goclaw-roelfdiedericks `trigger_poller.go` | Single poller; immediate first tick; UNIQUE(scheduled_for) claim; missed-grace skip; outcome then advance | Durable claim ≈ our idempotent occurrence enqueue. |
| goclaw-nextlevelbuilder `migrations/000022` | `agent_heartbeats` table: per-agent interval+prompt, isolated session, active hours, run logs with tokens, `agent_config_permissions` scope | Assignment ≈ typed heartbeat: durable row + bounded config + run journal (our occurrences/mappings already journal). |
| nanoclaw `src/` | Everything-is-message; `on_wake` messages picked only by fresh container; sweep delivers due-message wakes; pending_approvals | Single wake chokepoint; our enqueueLocked is the durable wake seam. |
| osinara `agent-schedule-dispatch.ts` + lib | Minute dispatcher claims due user scenarios → chat turns; runtime admission gate; schedule heartbeat records completed cycles | Admission gate ≈ our budget/authorize seam; assignment tick mirrors dispatcher. |
| pi-mono `packages/durable/harness/scheduler.ts` | Durable task states pending/running/waiting/sleeping; restart ⇒ running→pending; park after repeated no-progress restarts | Assignment "waiting" state for blocked/degraded links; parking ≈ our blocked schedule state. |
| paperclip server | Deferred durable wakes, CAS claim, re-validation at promotion, post-commit effects | Fence at effect point confirmed as the right seam. |
| goose `scheduler_trait.rs` | SchedulerTrait abstracts platform cron | Rejected: our in-spool engine is already portable; no platform adapter. |
| openclaw `eval/qa-scenarios.json` | Heartbeat routing + internal-event subagent spawn scenarios | Semantic reference for sink=agent evidence bundling (exists). |
| eve `prwatch` | Poll-based PR watcher | Covered by existing watcher kinds. |

## Gaps this milestone must close

1. No first-class durable owner-intent record grouping sources+wake+control
   → `Assignment` + plan reconcile (new).
2. Two subscriptions to the same source fetch twice; no conditional HTTP,
   no Retry-After honoring, no cursor/gap bookkeeping → `Acquisition` pool +
   `EventState` cursor fields + conditional fetch (new).
3. Events cannot be filtered deterministically per event before the sink →
   `probe.filter` Starlark verdict (keep|drop|urgent) (new).
4. No push intake for external senders → `kind=hook` + `/v1/hooks/{id}`
   bearer-token endpoint reusing the event pipeline (new).
5. No assignment-level control surface → `/v1/assignments` API +
   `assignment_*` agent tools + `/assignments` chat commands (new).
6. Wake fencing covers schedule deletion but not assignment pause/cancel →
   assignment-state check at settle + job-claim + delivery-claim (new).
