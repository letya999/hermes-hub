---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0016 scale-to-zero runtime contracts

ADR-0014, SPEC-0012 and SPEC-0013 replace the proposed permanently resident
per-user compute with a warm scale-to-zero runtime per active context. The contracts
define host-side Docker authority, per-context leases, a five-minute default idle TTL,
safe reaping, durable hub-owned routines and explicit native-cron migration. Issues
#14-#19, #33, #46 and #55 plus the shared context in the remaining open implementation
issues were aligned to this decision.

On 2026-09-12, the initial implementation added the host-side Go supervisor, authenticated
context proxy, pinned Hermes Gateway startup/readiness contract, persistent `/v1/runs`
execution, typed leases, scoped mounts, resource limits and lease/TTL reaping. Unit and
race tests cover concurrent start deduplication, path/symlink isolation, authorization,
resource arguments, warm reuse and safe reaping.
`just check` passed with 85.06% Go statement coverage. `just security` passed with no Go
or browser dependency vulnerabilities. `just docker-check` passed the standalone runtime
smoke and the real pinned Hermes 0.21.0 sessions/idempotency/SSE/stop/approval/cron/restart
contract probe. The integration contract also runs a real host-supervisor Docker smoke
covering warm reuse, idle reap, cold restore and logical runtime identity. Durable hub
job/session mappings, external event/approval forwarding, routines and migration remain
tracked in #15-#19 and #33.

An additional real-image probe on 2026-09-12 started `hub-runtime serve` with
`HUB_PERSISTENT_HERMES=true`, waited for authenticated `/readyz` backed by the
upstream API, checked `/healthz`, and removed the container; it passed with no live
provider call.

On 2026-09-12, communication rollout verification (CHG-0018) passed `just check`
with 85.06% own Go statement coverage and the final `just docker-check`
(image `7a3f67d90dba`, pinned Hermes 0.21.0). The complete production Telegram-adapter
gateway/worker/spool/HTTP supervisor path executed pre-rollout queued input once,
kept two verified users on separate exact homes/workspaces and sessions/runs,
reused a warm session/generation, settled terminal leases and automatically stopped
both contexts after five real minutes. Neither context stopped before its own idle
deadline. A later Alice task cold-started a replacement with the original session;
Bob stayed stopped. Telegram and model endpoints were local fixtures, while actual
upstream Hermes execution and Docker lifecycle were real. No live account or
external provider/send success is claimed.

The final combined gate also passed native approval/cancellation/restart, automatic
recovery, admission/reaper race, operator pin, idle no-wake, orphan ownership,
due routine wake/final/handoff/sleep and preservation checks. Native crashed turns
retained their original run while the upstream lease expired; input was not replayed
and upstream locks were not removed. Migration preflight uses the pinned native cron
SDK against a disposable snapshot and verifies the selected gateway's actual spool.
`just security` passed with no Go/browser vulnerabilities. Issues 16–18 are closed;
issue 19 retains its compatibility-release/legacy-deletion gate. Changes remain local.
