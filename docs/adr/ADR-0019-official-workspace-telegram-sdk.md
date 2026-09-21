---
description: Official Google remote MCP and owner-approved narrow Telegram Python SDK workload.
last_verified: 2026-09-15
---
# ADR-0019: Official Workspace MCP and Telegram SDK workload

Status: accepted (owner instruction, 2026-09-15).

Use Google's official remote Workspace MCP services for opt-in read grants.
OAuth stays in Go; account subject or verified email is checked before binding.
Discovery snapshots schemas only for a reviewed read-tool allowlist; unknown
tools and provider instructions cannot expand authorization. Calendar mutation
receipts retain the independently verified REST grant, not guessed MCP output.
Google's services are Developer Preview; Cloud project/API/preview eligibility
is an external setup prerequisite, not something fixtures can establish.

The owner explicitly approved Python/Telethon inside ToolHub. A narrow account
MCP uses the existing pinned Telegram upstream virtualenv, without editing
Hermes or upstream source. Go still owns authorization, deployment admission,
credential injection, isolation, lifecycle and audit. No Celery dependency is
needed for this account MCP. Bot tokens, groups and raw Telegram API tools are
excluded. Send/reply return Telethon's Message.id; deletion acknowledges pts.

Per-user workload identity and paths also include principal and connection.
Injection is serialized across processes until execution and credential wipe
finish. Existing controller plans now include the exact workspace_path; the
controller must mount it at the declared connection-state target and enforce
the immutable resource/egress plan. No controller means no account execution.
Session revoke cuts local calls; provider-wide invalidation of an imported
session is an explicit Telegram Devices action. No login or send is automatic.

This supplements ADR-0003/ADR-0013/ADR-0018; it does not change default runtime
selection or claim live ToolHive/account evidence from fixtures.
