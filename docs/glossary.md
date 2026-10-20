---
description: Canonical terms — feature, tool, toolset, capability, connector, backend/via.
last_verified: 2026-10-20
---
# Glossary

Terms that historically drifted across README, `workspace.yaml`, scope.yaml and docs.
The column on the left is the config-facing name; the rest is what it means in code.

| Term | Meaning |
|---|---|
| `feature` | Declarative flag in `workspace.yaml` (`features:`) that renders mounts, env and toolset wiring (e.g. `browser`, `hh`). Older docs say "integration"; `scope.yaml` "remove features" means the same flags. |
| `tool` | One callable function the model can invoke (e.g. `file_read`, `shell`). Enforced per tool by ToolHub admission. |
| `toolset` | A group of tools granted as a unit. Native (upstream Hermes) toolsets are granted whole — per-tool `only`/`access` cannot apply on `via: native`. |
| `capability` | The user-visible ability a feature/tool-set provides. Exactly one surface per capability (native toolset, hub MCP entry, owner `mcp_servers`, or ToolHub projection). |
| `connector` | An MCP server or prepared package managed through ToolHub (catalog/`.mcpb`/recipe). Connectors are configured as `via: mcp[:server]` or `mcp-raw:name`. |
| `backend` / `via:` | Where a tool executes: `native` (in-runtime, no per-call admission), `toolhub` (managed executor — admission, audit, os.Root scopes), `mcp[:server]` (ToolHub-mediated connector), `mcp-raw:name` (owner-defined MCP, org-scoped only in managed mode), `off` (explicit deny; absent = denied). |
| `grant` | A recorded authorization for a principal to call a tool/connector — ToolHub policy object, not the same as a feature "wish" in defaults. Defaults enable wishes; grants come from the owner/org policy. |
| `surface` | The one execution path a capability is reachable through — reserved against owner `mcp_servers` to prevent shadowing (SPEC-0040). |
| `space` | A user/organization directory `spaces/<id>` holding scope config, secrets env files and state. Docker `dev`/`prod` is an environment choice, not a namespace. |
| `context` | A chat/conversation identity that owns a warm runtime (forum topic, DM, …). |

Related: [architecture](architecture.md), [spaces](spaces.md), [boundaries](boundaries.md).
