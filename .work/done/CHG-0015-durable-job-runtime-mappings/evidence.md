---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0015 durable job/runtime mappings

The communication spool now persists job and conversation mappings with immutable
fingerprints, Hermes session/run metadata and explicit terminal/uncertain states. The
host supervisor persists runtime generations and leases and rejects stale lease
release after a generation change. Unit and race tests cover duplicate keys, legacy
mapping recovery, corrupt state, restart restoration, stale leases, terminal status
metadata and Hermes stop requests. These tests do not claim a second-host or live
provider integration; the Docker contract gate remains required for that evidence.

On 2026-09-12, `just check` passed with 85.06% Go statement coverage, including race
tests, formatting, vet, staticcheck, documentation and workflow checks. `just security`
passed the Go vulnerability scan and browser npm audit. `just docker-check prod` built
image `3248ea8a01bd`, passed the standalone runtime smoke and passed the real pinned
Hermes 0.21.0 sessions/idempotency/SSE/stop/approval/cron/restart contract plus the
host-supervisor warm/reap/cold-restore smoke. No live provider call or external message
was made.
