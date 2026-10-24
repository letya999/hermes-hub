---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0037 ToolHub transport refresh

ToolHive and GitHub fallback name search now return source-only candidates from
their live public APIs; fixture tests cover Smithery Bearer-token search, prepared
priority, deduplication, bounded warnings and a rate-limited registry. Selection
still requires generic source review. A full unprepared registry install and
live Smithery search have not been proven.

The Go gateway now holds a stateful MCP server per authenticated owner token.
An HTTP wire regression confirms that an open client receives
`notifications/tools/list_changed` after disable and re-enable, retains its MCP
session ID, cannot execute a removed tool, and cannot use another owner's
session ID. The ToolHub process polls persisted projection changes; the old
serve-runtime Hermes restart watcher is removed. A separate pinned Hermes
0.21.0 Docker contract used a local read-only fixture and observed tool removal
and restoration, three successful ordinary runs plus an active run that completed
while projection changed, unchanged process PID and session, and a cold ToolHub
endpoint restart. This does not prove production Telegram delivery, every #74
failure/recovery case or a live provider account; #74 remains open.

An unprepared source-only result from live GitHub repository search completed
the generic install lifecycle in the local Docker ToolHub: generated Node recipe,
isolated build, MCP initialize/`tools/list`, projected echo call and cleanup.
The echo tool returned the test string; disable/remove then removed it from the
same open MCP session. The first run exposed two generic defects: Broker-only
admission rejected definitions with no credential fields, and repository
metadata for a pinned subfolder was scoped twice. Regressions cover both fixes.
This does not prove install from an unprepared Official MCP Registry/marketplace
candidate, two-owner Docker/Broker isolation, or Telegram delivery; #110 and
the owner/production acceptance remain open.
