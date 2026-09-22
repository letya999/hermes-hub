---
description: Keep direct Hermes MCP after the local vMCP dynamic-rebind failure.
last_verified: 2026-09-11
---
# ADR-0024: Defer ToolHive local vMCP as the stable Hermes endpoint

Status: accepted, 2026-09-11.

ToolHive 0.48.0 local vMCP provides useful aggregation and isolation capabilities, but
the issue-11 probe found stale backend addressing after a remote workload was removed
and recreated, and quick group mode is anonymous. Hermes therefore continues to use
direct `mcp_servers` until the new Go ToolHub is integrated. ToolHive may be used as a
replaceable connector runner, but vMCP is not the production identity or authorization
boundary. The project-owned Go boundary resolves scope and binding while delegating OCI,
protocol-scheme command and remote lifecycle to `thv`.

Reconsider only after the acceptance suite in [the audit](../toolhive-vmcp-audit.md)
passes on the target Linux VPS. This decision requires no migration; rollback is the
unchanged direct Hermes configuration.
