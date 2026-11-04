package catalog

import "github.com/letya999/hermes-hub/internal/toolhub/api"

// RBAC rights vocabulary (SPEC-0092 "Capability vocabulary"). A right is an
// agent permission over a domain — never a tool: toolset members declare
// which capability.operation they require, and ToolHub's single PDP answers
// Decide(envelope, capability, operation, scope) at every action site. The
// emitted rights.yaml ships inside every image and is immutable per release.
// The record types live in internal/toolhub/api because the PDP is ToolHub
// and catalog already imports api — aliases keep the catalog spelling.
const RightsFile = "rights.yaml"

type RightOperation = api.RightOperation
type RightCapability = api.RightCapability
type RightsDoc = api.RightsDoc

var selfIssuers = []string{"platform", "org", "operator"}
var crossIssuers = []string{"platform", "org"}

func rop(subject, scope string, issuers []string, pep []string, effect, description string) RightOperation {
	return RightOperation{Subject: subject, Scope: scope, Issuers: issuers, PEP: pep, Effect: effect, Description: description}
}

// generateRights emits the complete normative domain table: every operation
// an agent can ever be granted. A new right is a catalog release — never a
// space-local invention.
func generateRights() RightsDoc {
	return RightsDoc{Release: Release, DefinitionID: "hub-agent-rights", Capabilities: map[string]RightCapability{
		"workspace": {
			Description: "Rights over the space's /workspace tree: inbox, work, projects, artifacts and outbox paths declared by the workspace passport.",
			Operations: map[string]RightOperation{
				"read":           rop("self", "path", selfIssuers, []string{"file tools"}, "read", "Read files and list directories inside the workspace boundary."),
				"write":          rop("self", "path", selfIssuers, []string{"file tools"}, "write", "Create, modify or delete files inside writable workspace paths."),
				"create_project": rop("self", "path", selfIssuers, []string{"file tools", "project init"}, "write", "Initialize a new project directory — including nested git repositories — under /workspace/projects."),
				"read_other":     rop("cross", "path", crossIssuers, []string{"file tools"}, "read", "Read another space's explicitly shared workspace export."),
			},
		},
		"memory": {
			Description: "Rights over the long-term memory vault: CORE memory, daily logs, cards and summaries.",
			Operations: map[string]RightOperation{
				"read":     rop("self", "path", selfIssuers, []string{"vault tools"}, "read", "Read memory files under /vault/memory."),
				"search":   rop("self", "path", selfIssuers, []string{"vault tools"}, "read", "Run semantic or lexical search across memory entries."),
				"remember": rop("self", "path", selfIssuers, []string{"vault tools"}, "write", "Persist new facts, cards and summaries into memory."),
			},
		},
		"models": {
			Description: "Rights over model routing: which routes the agent may see, call and switch between.",
			Operations: map[string]RightOperation{
				"list":   rop("self", "none", selfIssuers, []string{"model-relay"}, "read", "List the models and routes visible to this space."),
				"use":    rop("self", "model", selfIssuers, []string{"model-relay"}, "write", "Send inference requests to a service the route ladder admits."),
				"switch": rop("self", "model", selfIssuers, []string{"model-relay"}, "control", "Move the session to another model or route inside the granted ladder."),
			},
		},
		"sessions": {
			Description: "Rights over this space's session store: listing, searching, reading, switching and lifecycle of own sessions.",
			Operations: map[string]RightOperation{
				"list":   rop("self", "session", selfIssuers, []string{"session tools"}, "read", "List sessions of this space."),
				"read":   rop("self", "session", selfIssuers, []string{"session tools"}, "read", "Read a bounded transcript of an own session."),
				"search": rop("self", "session", selfIssuers, []string{"session tools"}, "read", "Search own sessions by content or title."),
				"switch": rop("self", "session", selfIssuers, []string{"session tools"}, "control", "Move the active-session pointer inside this space."),
				"manage": rop("self", "session", selfIssuers, []string{"session tools"}, "control", "Archive, restore or delete sessions of this space."),
			},
		},
		"tasks": {
			Description: "Rights over the task graph: scheduled routines, observed watches and delegated assignments.",
			Operations: map[string]RightOperation{
				"schedule": rop("self", "none", selfIssuers, []string{"routine tools"}, "control", "Create and steer routines — recurring or one-shot scheduled work."),
				"observe":  rop("self", "none", selfIssuers, []string{"watch tools"}, "control", "Create and steer watches — event subscriptions over admitted sources."),
				"delegate": rop("self", "none", selfIssuers, []string{"assignment tools"}, "control", "Create and steer assignments — delegated multi-step work with spend grants."),
			},
		},
		"ssh": {
			Description: "Rights over declared SSH hosts: reads, writes, exec, interactive shells and tunnels.",
			Operations: map[string]RightOperation{
				"list":   rop("self", "none", selfIssuers, []string{"ssh tools"}, "read", "List SSH hosts declared in the capability config."),
				"read":   rop("self", "host", selfIssuers, []string{"ssh tools"}, "read", "Read remote files on a granted SSH host."),
				"write":  rop("self", "host", selfIssuers, []string{"ssh tools"}, "write", "Write remote files on a granted SSH host."),
				"exec":   rop("self", "host", selfIssuers, []string{"ssh tools"}, "control", "Run bounded commands on a granted SSH host."),
				"shell":  rop("self", "host", selfIssuers, []string{"ssh tools"}, "control", "Drive an interactive shell on a granted SSH host."),
				"tunnel": rop("self", "host", selfIssuers, []string{"ssh tools"}, "control", "Open or close tunnels through a granted SSH host."),
			},
		},
		"exposure": {
			Description: "Rights over public exposure of space endpoints: listing, opening and closing tunnels.",
			Operations: map[string]RightOperation{
				"list":   rop("self", "none", selfIssuers, []string{"exposure tools"}, "read", "List live exposures and their state."),
				"create": rop("self", "url", selfIssuers, []string{"exposure tools"}, "control", "Open a declared endpoint's public tunnel."),
				"remove": rop("self", "url", selfIssuers, []string{"exposure tools"}, "control", "Close a live public exposure."),
			},
		},
		"research": {
			Description: "Rights over async deep-research jobs of this space.",
			Operations: map[string]RightOperation{
				"create": rop("self", "none", selfIssuers, []string{"deep_research flow"}, "write", "Start an async deep-research job."),
				"read":   rop("self", "none", selfIssuers, []string{"deep_research flow"}, "read", "Read research job status and reports."),
			},
		},
		"browser": {
			Description: "Rights over the browser connector: opening sessions, reading pages, acting and capturing.",
			Operations: map[string]RightOperation{
				"open":    rop("self", "url", selfIssuers, []string{"browser connector"}, "write", "Open a browser session on a URL."),
				"read":    rop("self", "url", selfIssuers, []string{"browser connector"}, "read", "Read a browser session's page content."),
				"act":     rop("self", "url", selfIssuers, []string{"browser connector"}, "control", "Click, type, scroll and otherwise act in a browser session."),
				"capture": rop("self", "url", selfIssuers, []string{"browser connector"}, "read", "Capture a browser session as screenshot or pdf."),
			},
		},
		"platform": {
			Description: "Rights over the pinned platform layer: orientation, own status, onboarding and usage — always subject self.",
			Operations: map[string]RightOperation{
				"guide":     rop("self", "none", selfIssuers, []string{"ToolHub platform ops"}, "read", "Read the space orientation guide."),
				"status":    rop("self", "none", selfIssuers, []string{"ToolHub platform ops"}, "read", "Read own health and allowlisted diagnostics."),
				"usage":     rop("self", "none", selfIssuers, []string{"ToolHub platform ops"}, "read", "Read own consumption against models.yaml limits."),
				"bootstrap": rop("self", "none", selfIssuers, []string{"ToolHub platform ops"}, "control", "Complete pending first-run setup steps."),
			},
		},
		"exec": {
			Description: "Rights over the workspace execution environment: running bounded commands and steering background jobs.",
			Operations: map[string]RightOperation{
				"run": rop("self", "none", selfIssuers, []string{"exec cell"}, "control", "Run a bounded command in the workspace execution environment."),
			},
		},
		"web": {
			Description: "Rights over outbound web access: searching through the configured provider and fetching URLs.",
			Operations: map[string]RightOperation{
				"search": rop("self", "none", selfIssuers, []string{"web provider"}, "read", "Search the web through the space's provider."),
				"read":   rop("self", "url", selfIssuers, []string{"web provider"}, "read", "Fetch a URL's content as bounded text."),
			},
		},
		"context": {
			Description: "Rights over this session's context window: reading state and requesting compaction.",
			Operations: map[string]RightOperation{
				"read": rop("self", "session", selfIssuers, []string{"context tools"}, "read", "Read the session's context control state."),
				"edit": rop("self", "session", selfIssuers, []string{"context tools"}, "control", "Compact the session transcript at a turn boundary."),
			},
		},
		"goals": {
			Description: "Rights over this conversation's persistent goal record.",
			Operations: map[string]RightOperation{
				"read":   rop("self", "none", selfIssuers, []string{"goal tools"}, "read", "Read the conversation's goal record and budget state."),
				"edit":   rop("self", "none", selfIssuers, []string{"goal tools"}, "control", "Set, pause or resume the conversation's goal."),
				"remove": rop("self", "none", selfIssuers, []string{"goal tools"}, "control", "Clear the conversation's goal record."),
			},
		},
		"tools": {
			Description: "Rights over the tool surface itself: discovering and selecting toolsets into the live session.",
			Operations: map[string]RightOperation{
				"list":   rop("self", "none", selfIssuers, []string{"ToolHub projection"}, "read", "Enumerate the toolsets and tools admitted for this space."),
				"select": rop("self", "toolset", selfIssuers, []string{"ToolHub projection", "apply"}, "control", "Invoke a tool from a selected toolset whose scope and rules match."),
			},
		},
		"skills": {
			Description: "Rights over the skills catalog: listing, invoking, drafting and publishing skills.",
			Operations: map[string]RightOperation{
				"list":    rop("self", "none", selfIssuers, []string{"skill tools", "skillstore"}, "read", "List skills mounted in the effective catalog."),
				"invoke":  rop("self", "skill", selfIssuers, []string{"skill tools", "skillstore"}, "write", "Execute a published skill's entrypoint."),
				"draft":   rop("self", "skill", selfIssuers, []string{"skill tools", "skillstore"}, "write", "Author or edit a skill draft inside the space."),
				"publish": rop("self", "skill", selfIssuers, []string{"skill tools", "skillstore"}, "control", "Promote a skill draft into the published catalog."),
			},
		},
		"connectors": {
			Description: "Rights over third-party connectors: discovery, preparation, lifecycle and sharing.",
			Operations: map[string]RightOperation{
				"discover": rop("self", "none", selfIssuers, []string{"ToolHub control ops"}, "read", "Search and inspect the connector catalog."),
				"prepare":  rop("self", "connector", selfIssuers, []string{"ToolHub control ops"}, "write", "Run connector onboarding — credentials, review, materialization."),
				"enable":   rop("self", "connector", selfIssuers, []string{"ToolHub control ops"}, "control", "Enable a prepared connector for this space."),
				"disable":  rop("self", "connector", selfIssuers, []string{"ToolHub control ops"}, "control", "Disable an enabled connector without removing it."),
				"remove":   rop("self", "connector", selfIssuers, []string{"ToolHub control ops"}, "control", "Uninstall a connector and its projected tools."),
				"share":    rop("cross", "connector", crossIssuers, []string{"ToolHub control ops"}, "control", "Share a connector definition to another space or the org catalog."),
			},
		},
		"agents": {
			Description: "Rights over the agent's own runtime and peer agents: self-inspection, restart and monitoring.",
			Operations: map[string]RightOperation{
				"inspect_self":  rop("self", "service", selfIssuers, []string{"supervisor"}, "read", "Inspect own runtime state, sessions and configuration view."),
				"restart_self":  rop("self", "service", selfIssuers, []string{"supervisor"}, "control", "Request a supervised restart of the own runtime."),
				"monitor_self":  rop("self", "service", selfIssuers, []string{"supervisor"}, "read", "Read own health, watchdog and supervision status."),
				"inspect_other": rop("cross", "service", crossIssuers, []string{"supervisor"}, "read", "Inspect another space's runtime state where the org permits."),
			},
		},
		"users": {
			Description: "Rights over space membership and org users — always cross-subject.",
			Operations: map[string]RightOperation{
				"list":   rop("cross", "none", crossIssuers, []string{"hubctl", "communication"}, "read", "List the org's users and their space membership."),
				"invite": rop("cross", "none", crossIssuers, []string{"hubctl", "communication"}, "control", "Invite a user into a space or the org."),
			},
		},
		"stack": {
			Description: "Rights over the space stack: inspecting topology and applying stack changes.",
			Operations: map[string]RightOperation{
				"inspect": rop("self", "none", selfIssuers, []string{"stack_inspect tool"}, "read", "Read the space's service topology, modes and stack passport."),
				"change":  rop("self", "service", selfIssuers, []string{"apply"}, "control", "Apply a stack-affecting change — service modes, exposure, hosting."),
			},
		},
		"config": {
			Description: "Rights over the space configuration passport: reading it and editing own runtime settings.",
			Operations: map[string]RightOperation{
				"read":     rop("self", "none", selfIssuers, []string{"selfsettings"}, "read", "Read the effective configuration of the space."),
				"edit_own": rop("self", "none", selfIssuers, []string{"selfsettings", "apply"}, "write", "Change own settings that the config passport admits for self-edit."),
			},
		},
		"channels": {
			Description: "Rights over communication channels: sending messages and voice out of the space.",
			Operations: map[string]RightOperation{
				"send":       rop("self", "channel", selfIssuers, []string{"communication-hub"}, "write", "Send text and attachments through an admitted channel."),
				"send_voice": rop("self", "channel", selfIssuers, []string{"communication-hub"}, "write", "Send voice messages through an admitted channel."),
			},
		},
		"observability": {
			Description: "Rights over logs and diagnostics of the space's own services.",
			Operations: map[string]RightOperation{
				"read_own_logs": rop("self", "log", selfIssuers, []string{"diagnostics"}, "read", "Read logs, metrics and diagnostics of the own stack."),
				"read_logs":     rop("cross", "log", crossIssuers, []string{"diagnostics"}, "read", "Read logs of other spaces or platform services where the org permits."),
			},
		},
	}}
}
