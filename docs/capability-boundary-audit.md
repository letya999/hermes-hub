---
description: Evidence behind the proposed default-deny capability contract; current gaps, source provenance and proof limits.
last_verified: 2026-10-02
---
# Capability boundary audit

Scope: issue 122, branch `feat/capability-profile-122`, code baseline `114b458`.
Hermes source pin: `869228cab4a8276d3b4c78da9d9939670c47bd0f`.
This document records observed behavior and design evidence. It does not assert
that [ADR-0031](adr/ADR-0031-default-deny-capability-policy.md),
[ADR-0032](adr/ADR-0032-capability-execution-isolation.md),
[SPEC-0041](../specs/active/SPEC-0041-default-deny-capabilities.md) or
[SPEC-0042](../specs/active/SPEC-0042-capability-isolation-and-proof.md) is implemented.
Preparation/implementation state belongs to
[CHG-0065](../.work/in-progress/CHG-0065-capability-enforcement/plan.md).

## Owner intent and previous scope

The required baseline is zero capabilities, followed by an organization default
profile and explicit per-user grants. Config edits, native tools, installation,
extensions and indirect execution are all included. Logical tool replacement,
clear naming, separate file operations/scopes, strongly isolated terminal execution
and auditable human-owned grants are first-class requirements.

CHG-0064/SPEC-0040 documented 19 capability rows and added native server-name
reservation plus a browser feature dependency check. Those changes do not deliver
the clarified default-deny contract. Earlier design options were a hub facade over
ToolHub, moving everything into ToolHub, or preserving separate servers with a
common policy. ADR-0031 selects one ToolHub facade while ADR-0032 preserves separate
execution trust domains. A common facade must not inherit every backend's privilege.

## Current code evidence

| Finding | Source at the audited baseline | Limit of the conclusion |
|---|---|---|
| Native terminal/file/web/skills/todo/cron/messaging/memory/session groups are assembled unconditionally | [Config](../internal/stack/render.go), `Config`, toolsets construction | Availability checks can suppress individual schemas; this is not default-deny policy |
| Only CLI/Telegram platform toolsets are rendered; jobs use API `/v1/runs` | [render.go](../internal/stack/render.go), [server.go](../internal/runtime/server.go), `admitHermesRun` | API fallback verified below; not a production exploit |
| Local hub registers file, lifecycle, routine and document operations together | [tools.go](../internal/agenttools/tools.go), `Server` | Existing scoped file operations are useful; registration lacks uniform per-operation profile gating |
| Self-install is allowed absent a denying grant | [grants.go](../internal/toolhub/grants.go), `RequireSelfInstall` | Explicit existing behavior, also described by SPEC-0023 |
| Infra-owner runtime and control services share the runtime state root | [render.go](../internal/stack/render.go), `compose`, `stateVolumes`, `stateBind` | Mount topology is proven statically; key theft or grant modification was not attempted |
| Effective config is read-only, while Hermes extension directories are created under mutable state | [render.go](../internal/stack/render.go), [runtime.go](../internal/runtime/runtime.go) | YAML protection already exists; extension isolation is incomplete |
| Name reservation does not define capability/implementation identity | [mcp.go](../internal/stack/mcp.go), `validateMCP` | Do not describe reserved server names as proof of semantic deduplication |
| Authorization and isolation primitives already exist | [gateway.go](../internal/toolhub/gateway.go), [scope.go](../internal/stack/scope.go), [tools.go](../internal/agenttools/tools.go) | Reuse per-call ToolHub checks, org narrowing, audit, broker delivery and `os.Root`; coverage is not whole-system proof |

## Pinned-image probes from this audit

Probes ran in disposable `hermes-hub:0.3.0-dev` containers whose upstream HEAD
matched the pin above. Containers used `--network none`, `--read-only`, a bounded
`/tmp` tmpfs, uid/gid 10001 and no host/space/credential mounts. They imported the
real Hermes toolset resolver and `get_tool_definitions`, without a model request
or provider mutation. These are resolver/schema probes, not end-to-end dispatch
or containment tests. The local image tag is not an immutable image receipt.

| Synthetic input | Observed result |
|---|---|
| Empty CLI/Telegram lists; missing API override | API resolved 13 groups and 25 schemas, including `terminal`, `write_file`, `execute_code`, `delegate_task`, `skill_manage` |
| Explicit empty API list, no configured MCP/plugins | Zero groups and zero schemas |
| Explicit empty CLI list | `kanban` group recovered; its availability checks suppressed the tools in this ordinary mode |
| Empty CLI plus explicit `agent.disabled_toolsets: [kanban]` | Zero groups and zero schemas |
| Empty CLI plus a synthetic configured MCP URL | Resolver included that MCP server as well as `kanban`; no connection attempted |

`just check` at the audited code baseline passed with 85.05% own Go statement
coverage. It does not currently exercise the new T01-T22 contract. There is no
production isolation, hostile-shell or real-provider acceptance claim in this audit.

## Context7 and GitHub research, 2026-10-02

Context7 queries used `/nousresearch/hermes-agent`,
`/modelcontextprotocol/modelcontextprotocol` and `/stacklok/toolhive`. Relevant
excerpts were checked against original GitHub files or official documentation.
Context7's Hermes result points to moving `main`, whose resolver already differs
from our pin; its empty-list validator warning is not sufficient evidence of the
runtime tool surface. Latest ToolHive/MCP features are not assumed installed here.

| Source | Version/provenance | Adopted lesson |
|---|---|---|
| [Hermes platform resolver](https://github.com/NousResearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/hermes_cli/tools_config.py#L550-L659) | Pinned source; GitHub file blob `63c751b5926de4fb3e2a20361303e82d893541cb` | Explicit platform coverage; MCP merge and recovered/plugin groups need separate consideration |
| [Hermes tool selection](https://github.com/NousResearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/model_tools.py#L280-L334) | Pinned source | Composite deny semantics differ from banning all leaves; inspect final schemas and dispatcher |
| [Hermes gateway hooks](https://github.com/NousResearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/gateway/hooks.py#L23-L95) | Pinned source; blob `c0a84aa67aa295f3ae0faaacfd164c3fe701c29d` | Valid files placed in the hook directory are imported at startup |
| [Hermes plugins](https://hermes-agent.nousresearch.com/docs/user-guide/features/plugins/) and [security](https://hermes-agent.nousresearch.com/docs/user-guide/security/) | Current official docs, not a pin-specific receipt | General plugins are opt-in; hooks/providers and environment filtering have distinct boundaries |
| [ToolHive vMCP architecture](https://github.com/stacklok/toolhive/blob/main/docs/arch/10-virtual-mcp-architecture.md) | Moving main, observed file blob `e1b8ab6b96e3c0a49967f1ee42e890b1ef7bbaaa` | Advertising filters leave routable hidden tools; identity must not transfer with backend/name priority |
| [ToolHive authorization](https://github.com/stacklok/toolhive/blob/main/docs/authz.md) | Context7 source reference, moving main | Default deny and deny precedence; reuse semantics without a new policy service |
| [MCP tool contract](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2025-11-25/server/tools.mdx) | Published 2025-11-25 spec path, observed blob `66512fa45b74f05eefb20f3821b481f3a4f702d8` | Tool annotations are hints, not trusted authorization; unique presentation names are not ownership identities |
| [MCP security guidance](https://modelcontextprotocol.io/docs/2025-11-25/tutorials/security/security_best_practices) | Official published guidance | Authenticate requests independently of session IDs; do not forward a caller token to upstream providers |
| [Go os.Root](https://go.dev/blog/osroot) | Official Go explanation | Reuse traversal-resistant handles; separately constrain mount topology and execution privileges |

The earlier broad search also covered X, Reddit, Hacker News, Medium, DEV,
Substack and Habr. Author discussions such as
[ToolHive on DEV](https://dev.to/stacklok/secure-by-default-authorization-for-mcp-servers-powered-by-toolhive-1hp6),
[Permit on Reddit](https://www.reddit.com/r/mcp/comments/1rx81kx/permit_mcp_gateway_authorization_consent_and/)
and [PortcullisMCP on Substack](https://paclabs.substack.com/p/portcullismcp-an-open-source-human)
helped identify patterns; product claims and examples are not security acceptance
for this repository. No external framework, provider API or current ToolHive upgrade
is selected solely from those posts.

## Unresolved proof obligations

The implementation must demonstrate the supported Hermes admission/dispatch seam,
native revocation behavior, private executor transport, network enforcement and
stop acknowledgement under failure. Until demonstrated, affected capabilities
stay disabled. ADR-0024's ToolHive vMCP no-go is not reversed by newer source docs.
The detailed proof ownership and order are in CHG-0065; the acceptance matrix is
SPEC-0042. No raw conversations, credentials or deployment files are included here.

The pinned-image probe also verified the on-disk hook startup path: a synthetic
`HOOK.yaml` and `handler.py` under `HERMES_HOME/hooks` executed at discovery even
with `hooks: {}` in the zero profile. The managed topology now mounts empty
read-only tmpfs roots over skills, hooks, plugins, skill-bundles, scripts,
bin, node and lsp, with no host extension
source to replace. A Docker canary verified denied writes, symlinks and mount
replacement while Hermes state stays writable. Actual managed-process lifecycle
and other launch paths still need P2 proof before launch is allowed. The same
canary now runs the pinned Hermes `HookRegistry` in two fresh Python processes:
an unapproved hook planted in writable state is not imported from either one.
The pinned source also discovers bundle YAML and webhook/cron scripts outside
the first three roots; the expanded canary checks those real scanners and
preflight rejects alternate bundle directories or project-plugin discovery.
This is loader-path evidence, not a full managed gateway restart.
The managed Go wrapper now validates the complete mounted effective YAML against
its reviewed model identity, zero native profile and single ToolHub endpoint
before touching runtime state. Malformed YAML cannot reach Hermes' permissive
config fallback through this wrapper. The managed launch guard remains active;
the full runtime lifecycle is not yet proven. A Docker canary with the built
`hub-runtime` confirms the valid config reaches the isolation refusal while
malformed YAML is rejected by preflight before Hermes can start.
The disposable agent-network canary also observes no IPv4 or IPv6 default route
inside the internal Docker network. Control and sibling fixture addresses remain
unreachable while the two narrow relays work; full T22 egress enforcement for
all future executors remains open.

CLIProxy's [management API documentation](https://github.com/router-for-me/CLIProxyAPIDocs/blob/main/docs/en/management/api.md)
places configuration and credential mutation routes on the same HTTP listener
as model traffic. A managed `model_url` ending in `/v1` therefore does not
restrict what else a compromised runtime can request on that listener. The
managed topology candidate now uses a path-limited Go model relay and removes
direct CLIProxy attachment from the agent network. The relay is an initial
chat-only route; broader model methods require separate review and tests.
