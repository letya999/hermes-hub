---
description: Canonical terms — feature, tool, toolset, capability, connector, backend/via.
last_verified: 2026-10-24
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
| `connector` | Broad integration/lifecycle concept. State the actual backend format: MCP, bounded CLI, provider API or hub executor. A definition/catalog entry describes what can be connected; a connection records a configured instance. |
| `backend` / `via:` | Where a tool executes: `native` (in-runtime, no per-call admission), `toolhub` (managed executor — admission, audit, os.Root scopes), `mcp[:server]` (ToolHub-mediated connector), `mcp-raw:name` (owner-defined MCP, org-scoped only in managed mode), `off` (explicit deny; absent = denied). |
| `grant` | A recorded authorization for a principal to call a tool/connector — ToolHub policy object, not the same as a feature "wish" in defaults. Defaults enable wishes; grants come from the owner/org policy. |
| `surface` | The one execution path a capability is reachable through — reserved against owner `mcp_servers` to prevent shadowing (SPEC-0040). |
| `space` | A user/organization directory `spaces/<id>` holding scope config, secrets env files and state. Docker `dev`/`prod` is an environment choice, not a namespace. |
| `context` | A chat/conversation identity that owns a warm runtime (forum topic, DM, …). |
| `ToolHub` | Gateway/control plane for projection, admission, dispatch and connector lifecycle. Its MCP endpoint can expose several backend formats. |
| `agent-tools` | ToolHub transport for reviewed hub operations. `hub-agent-tools` is a definition/group name; `toolset` describes grouping and `native` describes an upstream execution route. |
| `connection` | Owned configured instance with lifecycle state and credential reference. |
| `binding` | Association of a connection's tool surface with principal, context, runtime and policy. |
| `workload` | Execution instance when required by the transport. `shared`, `per-user` and `per-job` are workload classes, independently of principal/context/team ownership. |
| `per-team` | Informal team ownership/sharing description, not a workload-class enum. `shareable: credential` shares credentials across member workloads; `shareable: connection` shares both. |
| `container-mcp` / `remote-mcp` | ToolHub backend transports for managed MCP workloads / remote MCP endpoints. `mcp-raw:<name>` is instead a configuration route. |
| bundled CLI / `bounded-cli` | Bundled describes binary packaging; bounded CLI is a transport with validated arguments and execution limits. Neither proves provider authentication. |

The full agent-facing reference is
[`hermes-hub-system-guide`](../internal/systemguide/SKILL.md). Inventory answers
include all evidenced tool groups for unqualified MCP/connector questions and
label each format; an explicit "only MCP" request narrows the result.

Related: [architecture](architecture.md), [spaces](spaces.md), [boundaries](boundaries.md).
