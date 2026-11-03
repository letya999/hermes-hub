package catalog

import (
	"fmt"
	"slices"
	"strings"
)

// Member requires (SPEC-0092 R2.1, SPEC-0093 R2): every toolset member
// declares the RBAC operations a caller must hold to invoke it. capRights
// spells the mapping out capability id by capability id — a wrong right is
// a security bug, so nothing here is derived by string tricks. An id
// absent from the table resolves to "" and fails catalog generation:
// unmapped members are never emitted with a substitute floor right.
var capRights = map[string]string{
	// files: workspace paths; knowledge/archive roots share the same right.
	"files.list":    "workspace.read",
	"files.read":    "workspace.read",
	"files.search":  "workspace.read",
	"files.write":   "workspace.write",
	"files.edit":    "workspace.write",
	"files.mkdir":   "workspace.write",
	"files.archive": "workspace.write",
	"files.remove":  "workspace.write",
	"inbox.fetch":   "workspace.read",
	// documents
	"documents.read":    "workspace.read",
	"documents.create":  "workspace.write",
	"documents.edit":    "workspace.write",
	"documents.convert": "workspace.write",
	"documents.remove":  "workspace.write",
	// media
	"media.inspect":    "workspace.read",
	"media.convert":    "workspace.write",
	"media.generate":   "workspace.write",
	"media.edit":       "workspace.write",
	"media.fetch":      "workspace.read",
	"media.transcribe": "workspace.read",
	"media.speak":      "channels.send_voice",
	// terminal: workspace-adjacent execution right.
	"terminal.exec": "exec.run",
	"terminal.job":  "exec.run",
	// ssh
	"ssh.list":   "ssh.list",
	"ssh.read":   "ssh.read",
	"ssh.write":  "ssh.write",
	"ssh.exec":   "ssh.exec",
	"ssh.shell":  "ssh.shell",
	"ssh.tunnel": "ssh.tunnel",
	// exposure
	"exposure.list":   "exposure.list",
	"exposure.create": "exposure.create",
	"exposure.remove": "exposure.remove",
	// routines / watches / assignments → tasks.*
	"routines.list":      "tasks.schedule",
	"routines.read":      "tasks.schedule",
	"routines.create":    "tasks.schedule",
	"routines.edit":      "tasks.schedule",
	"routines.remove":    "tasks.schedule",
	"watches.list":       "tasks.observe",
	"watches.create":     "tasks.observe",
	"watches.edit":       "tasks.observe",
	"watches.remove":     "tasks.observe",
	"watches.sample":     "tasks.observe",
	"assignments.list":   "tasks.delegate",
	"assignments.read":   "tasks.delegate",
	"assignments.plan":   "tasks.delegate",
	"assignments.create": "tasks.delegate",
	"assignments.edit":   "tasks.delegate",
	"assignments.remove": "tasks.delegate",
	// goals / context — self-scope domains.
	"goals.read":    "goals.read",
	"goals.edit":    "goals.edit",
	"goals.remove":  "goals.remove",
	"context.read":  "context.read",
	"context.edit":  "context.edit",
	"settings.read": "config.read",
	"settings.edit": "config.edit_own",
	// sessions
	"sessions.list":   "sessions.list",
	"sessions.search": "sessions.search",
	"sessions.read":   "sessions.read",
	"sessions.switch": "sessions.switch",
	"sessions.edit":   "sessions.manage",
	"sessions.remove": "sessions.manage",
	// models
	"models.list":   "models.list",
	"models.switch": "models.switch",
	// skills
	"skills.invoke": "skills.invoke",
	"skills.search": "skills.list",
	"skills.edit":   "skills.draft",
	// connectors — control surface rights.
	"connectors.search":  "connectors.discover",
	"connectors.list":    "connectors.discover",
	"connectors.status":  "connectors.discover",
	"connectors.install": "connectors.prepare",
	"connectors.edit":    "connectors.enable",
	// web
	"web.search": "web.search",
	"web.fetch":  "web.read",
	// sites — publishing is a stack change.
	"sites.list":    "stack.inspect",
	"sites.publish": "stack.change",
	"sites.remove":  "stack.change",
	// research
	"research.create": "research.create",
	"research.read":   "research.read",
	"research.list":   "research.read",
	// browser
	"browser.open":    "browser.open",
	"browser.read":    "browser.read",
	"browser.act":     "browser.act",
	"browser.capture": "browser.capture",
	// platform — pinned self layer; tool_call keeps the honest tools.select
	// right (its admission replays the callee's own requires at the PEP).
	"platform.guide":     "platform.guide",
	"platform.status":    "platform.status",
	"platform.bootstrap": "platform.bootstrap",
	"platform.usage":     "platform.usage",
	"platform.search":    "tools.list",
	"platform.call":      "tools.select",
}

// connectorRights maps the curated connectors-family members onto
// connectors.* operations.
var connectorRights = map[string]string{
	"connector_search":  "connectors.discover",
	"connector_list":    "connectors.discover",
	"connector_status":  "connectors.discover",
	"connector_install": "connectors.prepare",
	"connector_edit":    "connectors.enable",
}

// RequiredRight is the exported form of rightForCap for contract surfaces
// that must declare a tool's RBAC requirement (agent-tools ToolSpec). An
// unmapped capability id returns "" — callers must refuse to emit a spec
// rather than substitute a floor right.
func RequiredRight(id string) string { return rightForCap(id) }

// rightForCap resolves one tool-level capability id to its required RBAC
// operation. Unknown ids resolve to "" — an unmapped capability fails
// closed (SPEC-0093: no floor right substitutes for an explicit mapping).
func rightForCap(id string) string {
	return capRights[id]
}

// annotateToolsets attaches actions — the tool-level capability ids the
// family carries (R2.1: capability ids become toolset actions) — and
// requires — the RBAC operations every member demands — to each generated
// toolset. A member no mapping admits fails generation: cataloging a tool
// without its right would either over-admit (floor right) or under-admit
// (empty requires the PEP reads as uncallable).
func annotateToolsets(out map[string]Toolset, caps CapabilitiesDoc) error {
	memberCaps := map[string][]string{}
	for id, cap := range caps.Capabilities {
		for _, tool := range cap.Tools {
			memberCaps[tool] = append(memberCaps[tool], id)
		}
	}
	var unmapped []string
	for name, ts := range out {
		family := strings.TrimPrefix(name, "hub:")
		var actions []string
		for id := range caps.Capabilities {
			if strings.SplitN(id, ".", 2)[0] == family {
				actions = append(actions, id)
			}
		}
		requires := map[string][]string{}
		for _, member := range ts.Members {
			rights := map[string]bool{}
			if op, ok := connectorRights[member]; ok {
				rights[op] = true
			}
			mapped := true
			for _, id := range memberCaps[member] {
				op := rightForCap(id)
				if op == "" {
					mapped = false
					continue
				}
				rights[op] = true
			}
			if len(rights) == 0 || !mapped {
				unmapped = append(unmapped, name+"."+member)
			}
			list := make([]string, 0, len(rights))
			for op := range rights {
				list = append(list, op)
			}
			slices.Sort(list)
			requires[member] = list
		}
		ts.Actions = sorted(actions)
		ts.Requires = requires
		out[name] = ts
	}
	if len(unmapped) > 0 {
		slices.Sort(unmapped)
		return fmt.Errorf("unmapped toolset members have no RBAC right: %s", strings.Join(unmapped, ", "))
	}
	return nil
}
