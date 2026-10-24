---
description: Aggregate bounded Docker logs and correlate Telegram, Hermes and ToolHub activity.
last_verified: 2026-09-26
---

# CHG-0045 Local diagnostics

## Existing path

The communication hub authorizes Telegram senders, enqueues jobs, then calls
Hermes through the runtime or supervisor. Hermes loads a read-only effective
MCP config pointing to shared ToolHub at `http://toolhub:8090/mcp` on the
shared Docker network. ToolHub authenticates an identity envelope and records
job/run/tool-call metadata in its audit ledger. Container stdout was dispersed
across Docker logs; the audit ledger deliberately had no message bodies.

## Change

- Log accepted ordinary Telegram text after enqueue, plus job/run outcome.
- Mirror ToolHub audit metadata to stdout when its ledger is configured.
- Bound Compose and supervisor runtime Docker logs with the local driver.
- Add an explicit one-file export across all `hermes-*` containers; keep the
  snapshot outside Git and omit credential messages.

## Verification

Run targeted privacy/contract tests and `just check`. Docker export needs a
running Docker daemon; do not restart it before the pending VHDX compaction.
