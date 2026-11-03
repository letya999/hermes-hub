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
